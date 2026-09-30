package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/credential"
	"github.com/open-e2ee/oe/internal/envfile"
)

// twoEnvironments answers a project with Sandbox and Production active, and
// records the environment of each notifications read.
func twoEnvironments(read *[]string) *fakeAPI {
	return &fakeAPI{
		getProject: func(_ context.Context, _ control.CredentialRequest, slug string) (control.Project, error) {
			return control.Project{
				Slug: slug, Writer: "config",
				Sandbox: projectEnvironment(sandboxRelayURL, "3"), Production: projectEnvironment(productionRelayURL, "7"),
			}, nil
		},
		notifications: func(_ context.Context, _ control.CredentialRequest, _, environment string) (control.NotificationConfiguration, error) {
			*read = append(*read, environment)
			return control.NotificationConfiguration{Environment: environment, ConfigurationVersion: 1}, nil
		},
	}
}

func TestEnvFlagSelectsTheEnvironmentAndDefaultsToSandbox(t *testing.T) {
	directory := initializedProject(t, "env-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	var read []string
	dependencies := Dependencies{API: twoEnvironments(&read), Store: store, WorkingDir: directory}

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"project", "connection"}, sandboxRelayURL},
		{[]string{"project", "connection", "--env", "production"}, productionRelayURL},
		{[]string{"project", "connection", "-e", "production"}, productionRelayURL},
		{[]string{"project", "connection", "-e=production"}, productionRelayURL},
		{[]string{"--env=production", "project", "connection"}, productionRelayURL},
		{[]string{"-e", "sandbox", "project", "connection"}, sandboxRelayURL},
	} {
		exit, stdout, stderr := run(t, dependencies, test.args...)
		if exit != 0 || stdout != test.want+"\n" {
			t.Fatalf("%v printed exit=%d %q %q, want %s", test.args, exit, stdout, stderr, test.want)
		}
	}

	for _, args := range [][]string{
		{"--json", "notifications", "status"},
		{"--json", "notifications", "status", "-e", "production"},
	} {
		if exit, stdout, _ := run(t, dependencies, args...); exit != 0 {
			t.Fatalf("%v failed: %s", args, stdout)
		}
	}
	if strings.Join(read, ",") != "sandbox,production" {
		t.Fatalf("notifications read the wrong environments: %v", read)
	}

	exit, stdout, _ := run(t, dependencies, "--json", "project", "show", "--env", "production")
	project, _ := decodeEvent(t, []byte(stdout)).Data["project"].(map[string]any)
	environments, _ := project["environments"].(map[string]any)
	if _, sandbox := environments["sandbox"]; exit != 0 || sandbox || environments["production"] == nil {
		t.Fatalf("project show --env production did not narrow to Production: %s", stdout)
	}
	exit, stdout, _ = run(t, dependencies, "--json", "project", "show")
	project, _ = decodeEvent(t, []byte(stdout)).Data["project"].(map[string]any)
	environments, _ = project["environments"].(map[string]any)
	if exit != 0 || len(environments) != 2 {
		t.Fatalf("project show without --env did not show both environments: %s", stdout)
	}

	exit, stdout, _ = run(t, dependencies, "--json", "project", "connection", "--env", "stage")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitUsage || failure.Code != "USAGE_ERROR" || failure.Error != "--env must be sandbox or production" {
		t.Fatalf("--env stage was accepted: exit=%d %s", exit, stdout)
	}
}

func TestOEEnvSelectsForReads(t *testing.T) {
	directory := initializedProject(t, "variable-chat")
	if err := writeRelayEnvironment(directory, ".env.production.local", envfile.DefaultVariable, productionRelayURL); err != nil {
		t.Fatal(err)
	}
	store := credential.NewMemory()
	storeCredential(t, store, "project:read", "project:write", "deploy:write")
	var read []string
	api := twoEnvironments(&read)
	var planned, bootstrapped string
	api.plan = func(_ context.Context, _ control.CredentialRequest, request control.PlanRequest) (control.Plan, error) {
		planned = request.Environment
		return control.Plan{ID: "plan-1", ProjectSlug: request.ProjectSlug, Environment: request.Environment, ExpectedRevision: "7", BillingReady: true}, nil
	}
	api.bootstrapSandbox = func(_ context.Context, _ control.CredentialRequest, request control.BootstrapRequest) (control.Bootstrap, error) {
		bootstrapped = request.ProjectSlug
		return control.Bootstrap{ProjectSlug: request.ProjectSlug, Writer: "config", Environment: "sandbox", SandboxRelayURL: sandboxRelayURL}, nil
	}
	relay := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != productionRelayURL {
			t.Fatalf("doctor requested an unexpected URL: %s", request.URL)
		}
		return &http.Response{Body: io.NopCloser(strings.NewReader(`{"schemaVersion":1}`)), Header: make(http.Header), StatusCode: http.StatusOK}, nil
	})}
	production := Dependencies{
		API: api, HTTP: relay, Store: store, WorkingDir: directory,
		Getenv: environment(map[string]string{"OE_ENV": "production"}),
	}

	exit, stdout, _ := run(t, production, "project", "connection")
	if exit != 0 || stdout != productionRelayURL+"\n" {
		t.Fatalf("OE_ENV=production did not select Production: exit=%d %q", exit, stdout)
	}
	exit, stdout, _ = run(t, production, "project", "connection", "--env", "sandbox")
	if exit != 0 || stdout != sandboxRelayURL+"\n" {
		t.Fatalf("--env did not override OE_ENV: exit=%d %q", exit, stdout)
	}
	exit, stdout, _ = run(t, production, "--json", "notifications", "status")
	if exit != 0 || strings.Join(read, ",") != "production" {
		t.Fatalf("notifications did not read OE_ENV: %v %s", read, stdout)
	}
	exit, stdout, _ = run(t, production, "--json", "doctor")
	if checks := decodeEvent(t, []byte(stdout)); exit != 0 || checks.Data["environment"] != "production" || checks.Data["configurationSource"] != ".env.production.local" {
		t.Fatalf("doctor did not read OE_ENV: exit=%d %s", exit, stdout)
	}

	// A variable left in a shell never changes the target of a deploy or a
	// Sandbox bootstrap.
	sandbox := production
	sandbox.Getenv = environment(map[string]string{"OE_ENV": "sandbox"})
	if exit, stdout, _ := run(t, sandbox, "--json", "plan"); exit != 0 || planned != "production" {
		t.Fatalf("OE_ENV=sandbox changed the plan environment to %q: %s", planned, stdout)
	}
	if exit, stdout, _ := run(t, production, "--json", "sandbox", "--no-wait"); exit != 0 || bootstrapped != "variable-chat" {
		t.Fatalf("OE_ENV=production stopped oe sandbox: %s", stdout)
	}

	invalid := production
	invalid.Getenv = environment(map[string]string{"OE_ENV": "stage"})
	exit, stdout, _ = run(t, invalid, "--json", "project", "connection")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitUsage || failure.Code != "USAGE_ERROR" || failure.Error != "OE_ENV must be sandbox or production" {
		t.Fatalf("OE_ENV=stage was accepted: exit=%d %s", exit, stdout)
	}
	if exit, stdout, _ := run(t, invalid, "--json", "project", "connection", "-e", "sandbox"); exit != 0 {
		t.Fatalf("--env did not override an invalid OE_ENV: %s", stdout)
	}
}

func TestEnvironmentFlagIsAUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "--environment", "sandbox", "doctor"},
		{"--json", "--environment=production", "project", "connection"},
		{"--json", "project", "connection", "--environment", "production"},
		{"--json", "plan", "--environment=sandbox"},
	} {
		exit, stdout, _ := run(t, Dependencies{WorkingDir: initializedProject(t, "flag-chat")}, args...)
		if failure := decodeEvent(t, []byte(stdout)); exit != exitUsage || failure.Code != "USAGE_ERROR" {
			t.Fatalf("%v exited %d: %s", args, exit, stdout)
		}
	}
	_, stdout, _ := run(t, Dependencies{}, "--json", "help")
	if strings.Contains(stdout, "--environment") {
		t.Fatalf("help still names --environment: %s", stdout)
	}
}

func TestSandboxEnvironmentNotFoundNamesAuthStatus(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{notifications: func(context.Context, control.CredentialRequest, string, string) (control.NotificationConfiguration, error) {
		return control.NotificationConfiguration{}, &control.APIError{Status: 404, Code: "ENVIRONMENT_NOT_FOUND", Message: "Relay environment not found."}
	}}
	dependencies := Dependencies{API: api, Store: store, WorkingDir: initializedProject(t, "missing-chat")}
	// Every project has Sandbox, so the session is the thing to check.
	exit, stdout, _ := run(t, dependencies, "--json", "notifications", "status")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitFailure || failure.Code != "ENVIRONMENT_NOT_FOUND" || failure.Next != "oe auth status" {
		t.Fatalf("Sandbox ENVIRONMENT_NOT_FOUND did not name oe auth status: exit=%d %s", exit, stdout)
	}
	exit, stdout, _ = run(t, dependencies, "--json", "notifications", "status", "--env", "production")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitFailure || failure.Code != "ENVIRONMENT_NOT_FOUND" || failure.Next == "oe auth status" {
		t.Fatalf("Production ENVIRONMENT_NOT_FOUND named oe auth status: exit=%d %s", exit, stdout)
	}
}
