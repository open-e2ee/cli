package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-e2ee/cli/internal/control"
	"github.com/open-e2ee/cli/internal/credential"
	"github.com/open-e2ee/cli/internal/projectlock"
)

type event struct {
	Status  string         `json:"status"`
	Command string         `json:"command"`
	Message string         `json:"message"`
	Error   string         `json:"error"`
	Code    string         `json:"code"`
	Next    string         `json:"next"`
	Data    map[string]any `json:"data"`
}

// decodeEvent decodes the one JSON document that --json writes and fails when
// stdout holds another.
func decodeEvent(t *testing.T, stdout []byte) event {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	var value event
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, stdout)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("JSON mode wrote more than one document: %s", stdout)
	}
	return value
}

func run(t *testing.T, dependencies Dependencies, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	dependencies.Out, dependencies.Err = &stdout, &stderr
	if dependencies.WorkingDir == "" {
		dependencies.WorkingDir = t.TempDir()
	}
	if dependencies.API == nil {
		dependencies.API = &fakeAPI{}
	}
	if dependencies.Store == nil {
		dependencies.Store = credential.NewMemory()
	}
	exit := Run(t.Context(), args, dependencies)
	return exit, stdout.String(), stderr.String()
}

func TestUsageErrorsExitTwoInTheRequestedMode(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown command":           {"--json", "bogus"},
		"unknown global flag":       {"--bogus", "--json"},
		"unknown command flag":      {"deploy", "--bogus", "--json"},
		"unexpected argument":       {"--json", "doctor", "extra"},
		"missing global flag value": {"--json", "doctor", "--environment"},
		"invalid environment":       {"--json", "--environment=stage", "doctor"},
		"missing subcommand":        {"--json", "project"},
		"unknown help topic":        {"--json", "help", "bogus"},
		"sandbox with production":   {"--json", "--environment", "production", "sandbox"},
	} {
		t.Run(name, func(t *testing.T) {
			exit, stdout, stderr := run(t, Dependencies{}, args...)
			if exit != exitUsage {
				t.Fatalf("exit %d, want %d: %s %s", exit, exitUsage, stdout, stderr)
			}
			failure := decodeEvent(t, []byte(stdout))
			if failure.Status != "error" || failure.Code != "USAGE_ERROR" || !strings.HasPrefix(failure.Next, "oe ") {
				t.Fatalf("usage error has no code or next command: %s", stdout)
			}
			if strings.Contains(failure.Error, "flag provided but not defined") && !strings.Contains(failure.Error, "oe deploy") {
				t.Fatalf("flag error does not name the command: %s", failure.Error)
			}
		})
	}
}

func TestTextFailureNamesTheNextCommandOnStandardError(t *testing.T) {
	exit, stdout, stderr := run(t, Dependencies{}, "plan")
	if exit != exitFailure || stdout != "" {
		t.Fatalf("text failure wrote to stdout or exited %d: %q", exit, stdout)
	}
	if !strings.Contains(stderr, "error: open-e2ee.jsonc was not found") || !strings.HasSuffix(stderr, "next: oe init\n") {
		t.Fatalf("text failure did not name oe init: %q", stderr)
	}
}

func TestGlobalFlagsAreAcceptedAfterTheCommand(t *testing.T) {
	for _, args := range [][]string{
		{"version", "--json"},
		{"--json", "version"},
		{"version", "--", "--json"},
	} {
		exit, stdout, _ := run(t, Dependencies{}, args...)
		if slices.Contains(args, "--") {
			if exit != exitUsage {
				t.Fatalf("arguments after -- reached the global parser: %v exit=%d %s", args, exit, stdout)
			}
			continue
		}
		if exit != 0 || decodeEvent(t, []byte(stdout)).Data["version"] != Version {
			t.Fatalf("global flag position changed the result: %v %s", args, stdout)
		}
	}
}

func TestHelpDescribesEachCommand(t *testing.T) {
	exit, stdout, _ := run(t, Dependencies{}, "-h")
	if exit != 0 || !strings.Contains(stdout, "Exit codes:") || !strings.Contains(stdout, "oe help <command>") {
		t.Fatalf("-h did not show the command surface: %s", stdout)
	}
	for _, args := range [][]string{{"--json", "help", "project"}, {"--json", "project", "--help"}} {
		exit, stdout, _ := run(t, Dependencies{}, args...)
		spec := decodeEvent(t, []byte(stdout))
		usage, _ := spec.Data["usage"].([]any)
		if exit != 0 || spec.Command != "project" || !slices.Contains(usage, any("oe project connection [PROJECT] [--environment sandbox|production]")) {
			t.Fatalf("%v did not describe project: %s", args, stdout)
		}
	}
}

func TestMissingSessionExitsFourAndNamesLogin(t *testing.T) {
	exit, stdout, _ := run(t, Dependencies{}, "--json", "project", "show", "any-chat")
	failure := decodeEvent(t, []byte(stdout))
	if exit != exitAuthentication || failure.Code != "AUTHENTICATION_REQUIRED" || failure.Next != "oe login" {
		t.Fatalf("missing session was not an authentication failure: exit=%d %s", exit, stdout)
	}
}

func TestControlRefusalKeepsTheConsoleCode(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	for _, test := range []struct {
		refusal *control.APIError
		exit    int
		next    string
	}{
		{&control.APIError{Status: 401, Code: "INVALID_SESSION", Message: "Run oe login again."}, exitAuthentication, "oe login"},
		{&control.APIError{Status: 404, Code: "PROJECT_NOT_FOUND", Message: "Relay project not found."}, exitFailure, ""},
		{&control.APIError{Status: 500, Message: "Internal Server Error"}, exitFailure, ""},
	} {
		api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{}, test.refusal
		}}
		exit, stdout, _ := run(t, Dependencies{API: api, Store: store}, "--json", "project", "show", "any-chat")
		failure := decodeEvent(t, []byte(stdout))
		wantCode := test.refusal.Code
		if wantCode == "" {
			wantCode = "CONTROL_ERROR"
		}
		if exit != test.exit || failure.Code != wantCode || failure.Next != test.next || failure.Error != test.refusal.Message {
			t.Fatalf("refusal %#v became exit=%d %s", test.refusal, exit, stdout)
		}
	}
}

func TestProjectConnectionPrintsTheServerRelayURL(t *testing.T) {
	directory := initializedProject(t, "connection-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	var requested []string
	api := &fakeAPI{getProject: func(_ context.Context, _ control.CredentialRequest, slug string) (control.Project, error) {
		requested = append(requested, slug)
		return control.Project{Slug: slug, Writer: "config", Sandbox: projectEnvironment(sandboxRelayURL, "3")}, nil
	}}
	dependencies := Dependencies{API: api, Store: store, WorkingDir: directory}

	exit, stdout, _ := run(t, dependencies, "project", "connection")
	if exit != 0 || stdout != sandboxRelayURL+"\n" {
		t.Fatalf("text connection is not the bare URL: exit=%d %q", exit, stdout)
	}
	exit, stdout, _ = run(t, dependencies, "project", "connection", "other-chat", "--json", "--environment=sandbox")
	connection := decodeEvent(t, []byte(stdout))
	if exit != 0 || connection.Data["relayUrl"] != sandboxRelayURL || connection.Data["project"] != "other-chat" || connection.Data["variable"] != "OPEN_E2EE_RELAY_URL" {
		t.Fatalf("JSON connection is incomplete: %s", stdout)
	}
	exit, stdout, _ = run(t, dependencies, "--json", "project", "connection", "--environment", "production")
	inactive := decodeEvent(t, []byte(stdout))
	if exit != exitFailure || inactive.Code != "ENVIRONMENT_NOT_ACTIVE" || inactive.Next != "oe deploy" {
		t.Fatalf("inactive Production did not name oe deploy: %s", stdout)
	}
	if !slices.Equal(requested, []string{"connection-chat", "other-chat", "connection-chat"}) {
		t.Fatalf("connection read the wrong projects: %v", requested)
	}
}

func TestProjectShowNeedsNoConfigAndOmitsTheConnection(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{getProject: func(_ context.Context, _ control.CredentialRequest, slug string) (control.Project, error) {
		return control.Project{Slug: slug, Writer: "config", Sandbox: projectEnvironment(sandboxRelayURL, "3")}, nil
	}}
	exit, stdout, _ := run(t, Dependencies{API: api, Store: store}, "--json", "project", "show", "show-chat")
	shown := decodeEvent(t, []byte(stdout))
	project, _ := shown.Data["project"].(map[string]any)
	environments, _ := project["environments"].(map[string]any)
	sandbox, _ := environments["sandbox"].(map[string]any)
	production, _ := environments["production"].(map[string]any)
	if exit != 0 || sandbox["active"] != true || sandbox["revision"] != "3" || production["active"] != false {
		t.Fatalf("project show lost the environments: %s", stdout)
	}
	if strings.Contains(stdout, "pk_sandbox_public") {
		t.Fatalf("project show printed a Relay connection URL: %s", stdout)
	}
}

func TestTextDeployWithoutATerminalRequiresConfirm(t *testing.T) {
	directory := initializedProject(t, "headless-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	api := &fakeAPI{
		getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{Slug: "headless-chat", Writer: "config"}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{ID: "plan-1", ProjectSlug: "headless-chat", Environment: "production", ExpectedRevision: "0", BillingReady: true}, nil
		},
	}
	exit, stdout, stderr := run(t, Dependencies{
		API: api, Store: store, WorkingDir: directory,
		In: strings.NewReader("y\n"), Interactive: func() bool { return false },
	}, "deploy")
	if exit != exitUsage || stdout != "" || !strings.HasSuffix(stderr, "next: oe deploy --confirm\n") {
		t.Fatalf("headless deploy did not ask for --confirm: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

func TestLogoutRemovesTheStoredSession(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	exit, stdout, _ := run(t, Dependencies{Store: store, Getenv: func(string) string { return "" }}, "--json", "logout")
	if exit != 0 || decodeEvent(t, []byte(stdout)).Data["environmentCredential"] != false {
		t.Fatalf("logout failed: %s", stdout)
	}
	profile, err := credential.Profile(defaultControlURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(profile); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("logout left the session stored: %v", err)
	}
	if exit, _, _ := run(t, Dependencies{Store: store}, "logout"); exit != 0 {
		t.Fatal("a second logout failed")
	}
}

func TestInitIgnoresTheLockFile(t *testing.T) {
	directory := t.TempDir()
	for range 2 {
		exit, stdout, _ := run(t, Dependencies{WorkingDir: directory}, "--json", "init", "--force")
		if exit != 0 {
			t.Fatalf("init failed: %s", stdout)
		}
	}
	ignored, err := os.ReadFile(filepath.Join(directory, ".gitignore"))
	if err != nil || strings.Count(string(ignored), projectlock.Filename+"\n") != 1 {
		t.Fatalf("init did not ignore the lock file once: %q %v", ignored, err)
	}
	exit, stdout, _ := run(t, Dependencies{WorkingDir: directory}, "--json", "init")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitFailure || failure.Code != "CONFIG_EXISTS" || failure.Next != "oe init --force" {
		t.Fatalf("existing config was not a named conflict: %s", stdout)
	}
}
