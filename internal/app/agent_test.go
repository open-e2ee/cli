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

	"github.com/open-e2ee/cli/internal/agent"
	"github.com/open-e2ee/cli/internal/control"
	"github.com/open-e2ee/cli/internal/credential"
	"github.com/open-e2ee/cli/internal/projectlock"
)

// TestMain removes the variables that coding agents set, so a test that runs
// under an agent sees the same defaults as CI. A test that needs a variable
// passes Getenv.
func TestMain(m *testing.M) {
	for _, name := range agent.Variables() {
		os.Unsetenv(name)
	}
	m.Run()
}

type event struct {
	Status  string `json:"status"`
	Command string `json:"command"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Code    string `json:"code"`
	Next    string `json:"next"`
	Action  struct {
		URL string `json:"url"`
	} `json:"action"`
	Data map[string]any `json:"data"`
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
		"invalid agent mode":        {"--json", "--agent=maybe", "doctor"},
		"missing agent mode":        {"doctor", "--json", "--agent"},
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

// productionPlan answers a Production plan for project. The plan needs a
// card when setupURL is not empty.
func productionPlan(project, setupURL string) *fakeAPI {
	return &fakeAPI{
		getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{Slug: project, Writer: "config"}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{
				ID: "plan-1", ProjectSlug: project, Environment: "production", ExpectedRevision: "0",
				BillingReady: setupURL == "", BillingSetupURL: setupURL,
			}, nil
		},
	}
}

// deviceLogin answers a browser authorization that a person approves at once.
func deviceLogin() *fakeAPI {
	return &fakeAPI{
		startAuthorization: func(context.Context, control.AuthorizationRequest) (control.Authorization, error) {
			return control.Authorization{DeviceCode: "device", VerificationURL: "https://login.example/device", UserCode: "ABCD", IntervalSeconds: 1}, nil
		},
		pollAuthorization: func(context.Context, control.Authorization) (control.Token, error) {
			return control.Token{AccessToken: "token"}, nil
		},
	}
}

// unreadable is standard input that fails the test when a command reads it.
type unreadable struct{ t *testing.T }

func (u unreadable) Read([]byte) (int, error) {
	u.t.Error("the command read standard input")
	return 0, io.EOF
}

// opener is a fake browser that records each URL that it opens.
type opener struct{ opened []string }

func (o *opener) open(target string) error {
	o.opened = append(o.opened, target)
	return nil
}

func terminal() bool { return true }

func TestAgentFlagOverridesDetection(t *testing.T) {
	claudeCode := environment(map[string]string{"CLAUDECODE": "1"})
	exit, stdout, _ := run(t, Dependencies{Getenv: claudeCode}, "--agent", "no", "version")
	if exit != 0 || stdout != Version+"\n" {
		t.Fatalf("--agent no did not restore text: exit=%d %q", exit, stdout)
	}
	exit, stdout, _ = run(t, Dependencies{}, "version", "--agent=yes")
	if exit != 0 || decodeEvent(t, []byte(stdout)).Data["version"] != Version {
		t.Fatalf("--agent yes did not select JSON: exit=%d %q", exit, stdout)
	}
	for _, test := range []struct {
		args   []string
		getenv func(string) string
		opened int
	}{
		{[]string{"--agent", "no", "login"}, claudeCode, 1},
		{[]string{"--agent", "yes", "login"}, environment(nil), 0},
		{[]string{"--agent", "auto", "login"}, claudeCode, 0},
	} {
		browser := &opener{}
		exit, stdout, _ := run(t, Dependencies{
			API: deviceLogin(), Getenv: test.getenv, Interactive: terminal, OpenURL: browser.open,
		}, test.args...)
		if exit != 0 || len(browser.opened) != test.opened {
			t.Fatalf("%v opened %d browsers, want %d: exit=%d %s", test.args, len(browser.opened), test.opened, exit, stdout)
		}
	}
}

func TestAgentNeverOpensABrowser(t *testing.T) {
	codex := environment(map[string]string{"CODEX_THREAD_ID": "codex-thread"})
	browser := &opener{}
	exit, stdout, stderr := run(t, Dependencies{
		API: deviceLogin(), Getenv: codex, Interactive: terminal, OpenURL: browser.open,
	}, "login")
	if exit != 0 || !strings.Contains(stderr, "Open https://login.example/device and enter code ABCD.") {
		t.Fatalf("an agent login hid the URL from the person who approves it: exit=%d stdout=%s stderr=%q", exit, stdout, stderr)
	}

	directory := initializedProject(t, "agent-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	exit, stdout, _ = run(t, Dependencies{
		API: productionPlan("agent-chat", "https://billing.example/setup"), Store: store, WorkingDir: directory,
		Getenv: codex, Interactive: terminal, OpenURL: browser.open,
	}, "deploy", "--confirm")
	failure := decodeEvent(t, []byte(stdout))
	if exit != exitFailure || failure.Code != "BILLING_SETUP_REQUIRED" || failure.Action.URL != "https://billing.example/setup" {
		t.Fatalf("an agent did not get the setup page in action.url: exit=%d %s", exit, stdout)
	}
	if len(browser.opened) != 0 {
		t.Fatalf("an agent opened a browser: %v", browser.opened)
	}
}

func TestNoTTYAndNoAgentNeverPrompts(t *testing.T) {
	directory := initializedProject(t, "headless-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	browser := &opener{}
	script := Dependencies{
		Store: store, WorkingDir: directory, In: unreadable{t},
		Interactive: func() bool { return false }, OpenURL: browser.open,
	}

	script.API = productionPlan("headless-chat", "")
	exit, stdout, stderr := run(t, script, "deploy")
	if exit != exitUsage || stdout != "" || strings.Contains(stderr, "[y/N]") || !strings.HasSuffix(stderr, "next: oe deploy --confirm\n") {
		t.Fatalf("a script got a prompt in place of a text refusal: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}

	script.API = productionPlan("headless-chat", "https://billing.example/setup")
	exit, _, stderr = run(t, script, "deploy", "--confirm")
	if exit != exitFailure || !strings.Contains(stderr, "https://billing.example/setup") {
		t.Fatalf("a script did not get the setup page: exit=%d stderr=%q", exit, stderr)
	}

	script.API = deviceLogin()
	exit, stdout, _ = run(t, script, "login")
	if exit != 0 || !strings.Contains(stdout, "https://login.example/device") {
		t.Fatalf("a script login hid the URL: exit=%d %q", exit, stdout)
	}
	if len(browser.opened) != 0 {
		t.Fatalf("a script opened a browser: %v", browser.opened)
	}
}

func TestAgentAtATerminalNeverPrompts(t *testing.T) {
	directory := initializedProject(t, "terminal-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	exit, stdout, _ := run(t, Dependencies{
		API: productionPlan("terminal-chat", ""), Store: store, WorkingDir: directory, In: unreadable{t},
		Getenv: environment(map[string]string{"OPENCODE": "1"}), Interactive: terminal,
	}, "deploy")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitUsage || failure.Code != "CONFIRMATION_REQUIRED" || failure.Next != "oe deploy --confirm" {
		t.Fatalf("an agent at a terminal was not refused without a prompt: exit=%d %s", exit, stdout)
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

// environment returns a Getenv that reads only values.
func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAgentDefaultsToJSON(t *testing.T) {
	claudeCode := environment(map[string]string{"CLAUDECODE": "1"})
	exit, stdout, _ := run(t, Dependencies{Getenv: claudeCode}, "version")
	if exit != 0 || decodeEvent(t, []byte(stdout)).Data["version"] != Version {
		t.Fatalf("an agent did not get JSON by default: exit=%d %q", exit, stdout)
	}
	exit, stdout, stderr := run(t, Dependencies{Getenv: claudeCode}, "plan")
	if failure := decodeEvent(t, []byte(stdout)); exit != exitFailure || stderr != "" || failure.Code != "CONFIG_NOT_FOUND" {
		t.Fatalf("an agent did not get the failure as JSON on stdout: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	exit, stdout, _ = run(t, Dependencies{API: deviceLogin(), Getenv: claudeCode}, "--json-stream", "login")
	first, _, _ := strings.Cut(stdout, "\n")
	if exit != 0 || decodeEvent(t, []byte(first)).Status != "progress" {
		t.Fatalf("--json-stream under an agent wrote no progress event: exit=%d %q", exit, stdout)
	}
}
