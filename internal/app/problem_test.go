package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/credential"
	"github.com/open-e2ee/oe/internal/output"
)

// The exit statuses in these tests are literals: a script switches on the
// number, so a test that reads the constant cannot catch a change to it.

func TestTemporaryFailuresExitSixAndNameTheSameCommand(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	unreachable := fmt.Errorf("control request failed: %w", &url.Error{
		Op: "Get", URL: "https://console.open-e2ee.dev/api/cli/v1/projects/any-chat", Err: errors.New("connection refused"),
	})
	for _, test := range []struct {
		name  string
		cause error
		code  string
	}{
		// 500 is not temporary by itself, so only the code can give exit 6.
		{"authority unavailable", &control.APIError{Status: 500, Code: "AUTHORITY_UNAVAILABLE", Message: "Current WorkOS authority could not be verified."}, "AUTHORITY_UNAVAILABLE"},
		{"429", &control.APIError{Status: 429, Message: "Too Many Requests"}, "CONTROL_ERROR"},
		{"502", &control.APIError{Status: 502, Message: "Bad Gateway"}, "CONTROL_ERROR"},
		{"503", &control.APIError{Status: 503, Message: "Service Unavailable"}, "CONTROL_ERROR"},
		{"504", &control.APIError{Status: 504, Message: "Gateway Timeout"}, "CONTROL_ERROR"},
		{"control unavailable", unreachable, "CONTROL_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
				return control.Project{}, test.cause
			}}
			exit, stdout, _ := run(t, Dependencies{API: api, Store: store}, "--json", "project", "show", "any-chat")
			failure := decodeEvent(t, []byte(stdout))
			if exit != 6 || failure.Code != test.code || failure.Next != "oe --json project show any-chat" {
				t.Fatalf("temporary failure became exit=%d %s", exit, stdout)
			}
		})
	}
}

func TestHelpListsExitCodesFiveAndSix(t *testing.T) {
	exit, stdout, _ := run(t, Dependencies{}, "--json", "help")
	help := decodeEvent(t, []byte(stdout))
	exitCodes, _ := help.Data["exitCodes"].([]any)
	var codes []float64
	for _, entry := range exitCodes {
		value, _ := entry.(map[string]any)
		code, _ := value["code"].(float64)
		if meaning, _ := value["meaning"].(string); meaning == "" {
			t.Fatalf("exit code %v has no meaning: %s", code, stdout)
		}
		codes = append(codes, code)
	}
	if exit != 0 || !slices.Contains(codes, 5) || !slices.Contains(codes, 6) {
		t.Fatalf("help does not list exit codes 5 and 6: %s", stdout)
	}
	exit, stdout, _ = run(t, Dependencies{}, "help")
	if exit != 0 || !strings.Contains(stdout, "\n  5  ") || !strings.Contains(stdout, "\n  6  ") {
		t.Fatalf("text help does not list exit codes 5 and 6: %s", stdout)
	}
}

func TestActionURLIsWrittenInTheErrorEnvelope(t *testing.T) {
	refusal := &problem{
		code: "CARD_REQUIRED", message: "Production needs a card", exit: 5,
		next: "oe config push --yes", actionURL: "https://console.open-e2ee.dev/billing?project=any-chat",
	}
	var stdout, stderr strings.Builder
	exit := fail(output.New(output.JSON, &stdout, &stderr), "config", []string{"--json", "config", "push", "--yes"}, "", refusal)
	var envelope struct {
		Code   string `json:"code"`
		Next   string `json:"next"`
		Action struct {
			URL string `json:"url"`
		} `json:"action"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatalf("decode failure: %v\n%s", err, stdout.String())
	}
	if exit != 5 || envelope.Code != "CARD_REQUIRED" || envelope.Next != "oe config push --yes" || envelope.Action.URL != refusal.actionURL {
		t.Fatalf("action URL is not in the envelope: exit=%d %s", exit, stdout.String())
	}

	stdout.Reset()
	fail(output.New(output.Text, &stdout, &stderr), "config", nil, "", refusal)
	want := "error: Production needs a card\naction: open https://console.open-e2ee.dev/billing?project=any-chat\nnext: oe config push --yes\n"
	if stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("text failure did not name the action: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	fail(output.New(output.JSON, &stdout, &stderr), "project", nil, "", &problem{code: "PROJECT_NOT_FOUND", message: "not found", exit: exitFailure})
	if strings.Contains(stdout.String(), `"action"`) {
		t.Fatalf("a failure with no action wrote one: %s", stdout.String())
	}
}

func TestCommandLineQuotesEachArgumentForAShell(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--json", "project", "show", "any-chat"}, "oe --json project show any-chat"},
		{[]string{"--control-url=https://127.0.0.1:8443/api/cli", "doctor"}, "oe --control-url=https://127.0.0.1:8443/api/cli doctor"},
		{[]string{"init", "--directory", "/tmp/my app"}, "oe init --directory '/tmp/my app'"},
		{[]string{"notifications", "verify", "ios", "--app-bundle", "it's"}, `oe notifications verify ios --app-bundle 'it'\''s'`},
		{[]string{"doctor", "$(touch pwned)"}, "oe doctor '$(touch pwned)'"},
		{[]string{"project", "show", ""}, "oe project show ''"},
	} {
		if got := commandLine(test.args); got != test.want {
			t.Errorf("commandLine(%q) = %q, want %q", test.args, got, test.want)
		}
	}
}
