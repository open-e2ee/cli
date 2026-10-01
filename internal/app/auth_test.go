package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-e2ee/oe/internal/config"
	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/credential"
)

// relayTerms is the terms document list of the console route tests.
var relayTerms = []map[string]any{{
	"name": "Relay service terms", "url": "https://open-e2ee.dev/legal/relay-terms/2026-08-26",
	"version": "relay-2026-08-26",
}}

// console is a loopback control API with the WorkOS device flow and the terms
// routes. The CLI reaches it through the real control client, so each body is
// the JSON that the console route answers. routes serves any other
// authorized route that a test adds.
type console struct {
	t         *testing.T
	server    *httptest.Server
	routes    *http.ServeMux
	mu        sync.Mutex
	state     string
	canAccept bool
	requests  []string
	accepts   []map[string]any
}

func newConsole(t *testing.T, state string, canAccept bool) *console {
	t.Helper()
	c := &console{t: t, routes: http.NewServeMux(), state: state, canAccept: canAccept}
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.server.Close)
	return c
}

// controlURL is the value of --control-url for this console.
func (c *console) controlURL() string { return c.server.URL + "/api/cli" }

// requested reports each "METHOD path" that the console answered.
func (c *console) requested() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

// accepted reports each acceptance body that the console recorded.
func (c *console) accepted() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.accepts)
}

func (c *console) serve(response http.ResponseWriter, request *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request.Method+" "+request.URL.Path)
	switch request.URL.Path {
	case "/api/cli/v1/auth/configuration":
		c.answer(response, http.StatusOK, map[string]any{
			"schemaVersion": 1, "clientId": "client_cli_test",
			"deviceAuthorizationEndpoint": c.server.URL + "/user_management/authorize/device",
			"tokenEndpoint":               c.server.URL + "/user_management/authenticate",
		})
		return
	case "/user_management/authorize/device":
		c.answer(response, http.StatusOK, map[string]any{
			"device_code": "device", "user_code": "ABCD-EFGH", "expires_in": 900, "interval": 1,
			"verification_uri": "https://login.example/device",
		})
		return
	case "/user_management/authenticate":
		c.answer(response, http.StatusOK, map[string]any{
			"access_token": sessionToken(), "refresh_token": "refresh",
		})
		return
	}
	if request.Header.Get("Authorization") == "" {
		c.answer(response, http.StatusUnauthorized, map[string]any{
			"code": "AUTHENTICATION_REQUIRED", "message": "Run oe auth login first.",
		})
		return
	}
	switch request.Method + " " + request.URL.Path {
	case "GET /api/cli/v1/terms":
		c.answer(response, http.StatusOK, c.terms())
	case "POST /api/cli/v1/terms/acceptance":
		if !c.canAccept {
			c.answer(response, http.StatusForbidden, map[string]any{
				"code":    "TERMS_PERMISSION_REQUIRED",
				"message": "Your Organization role does not permit accepting the Relay service terms. An administrator of your Organization must accept them.",
			})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			c.t.Errorf("decode the acceptance body: %v", err)
		}
		c.accepts = append(c.accepts, body)
		changed := c.state == "required"
		c.state = "accepted"
		answer := c.terms()
		answer["changed"] = changed
		c.answer(response, http.StatusOK, answer)
	default:
		if handler, pattern := c.routes.Handler(request); pattern != "" {
			handler.ServeHTTP(response, request)
			return
		}
		c.t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		response.WriteHeader(http.StatusNotFound)
	}
}

func (c *console) terms() map[string]any {
	var acceptedAt any
	if c.state == "accepted" {
		acceptedAt = "2026-09-01T09:30:00.000Z"
	}
	return map[string]any{
		"acceptedAt": acceptedAt, "canAccept": c.canAccept, "documents": relayTerms, "state": c.state,
	}
}

func (c *console) answer(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "private, no-store")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(body); err != nil {
		c.t.Errorf("encode the console answer: %v", err)
	}
}

// sessionToken is an unsigned access token with the claims of a WorkOS
// session in the console route tests.
func sessionToken() string {
	claims, _ := json.Marshal(map[string]any{
		"client_id": "client_cli_test", "sub": "user_example", "org_id": "org_example",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

// storeSession stores a valid session for the console.
func (c *console) storeSession(t *testing.T, store credential.Store) {
	t.Helper()
	profile, err := credential.Profile(c.controlURL())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(profile, credential.Credential{
		AccessToken: sessionToken(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshToken: "refresh",
	}); err != nil {
		t.Fatal(err)
	}
}

// dependencies reaches the console through the real control client.
func (c *console) dependencies(store credential.Store, getenv func(string) string) Dependencies {
	api, err := control.New(c.controlURL(), c.server.Client())
	if err != nil {
		c.t.Fatal(err)
	}
	return Dependencies{
		API: api, Store: store, Getenv: getenv,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

// decodeEvents decodes each JSON document on stdout.
func decodeEvents(t *testing.T, stdout string) []event {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var events []event
	for {
		var value event
		if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
			return events
		} else if err != nil {
			t.Fatalf("decode JSON output: %v\n%s", err, stdout)
		}
		events = append(events, value)
	}
}

var underClaudeCode = environment(map[string]string{"CLAUDECODE": "1"})

func TestAuthLoginUnderAnAgentWritesPendingThenResult(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	server := newConsole(t, "accepted", true)
	store := credential.NewMemory()
	browser := &opener{}
	dependencies := server.dependencies(store, underClaudeCode)
	dependencies.Interactive, dependencies.OpenURL, dependencies.In = terminal, browser.open, unreadable{t}
	exit, stdout, stderr := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	events := decodeEvents(t, stdout)
	if exit != 0 || len(events) != 2 || stderr != "" {
		t.Fatalf("an agent login did not write two JSON documents: exit=%d stdout=%s stderr=%q", exit, stdout, stderr)
	}
	pending, result := events[0], events[1]
	if pending.Status != "pending" || pending.Command != "auth login" || pending.Message != "A person must approve this device." ||
		pending.Action.Kind != "browser" || pending.Action.URL != "https://login.example/device" || pending.Action.Reason != "login" ||
		pending.Data["userCode"] != "ABCD-EFGH" || pending.Data["expiresInSeconds"] != float64(900) {
		t.Fatalf("the pending event does not hand the device to a person: %s", stdout)
	}
	organization, _ := result.Data["organization"].(map[string]any)
	if result.Status != "ok" || result.Command != "auth login" || result.Next != "" || organization["id"] != "org_example" ||
		result.Data["user"] != "user_example" || result.Data["terms"] != "accepted" || result.Data["source"] != "keychain" {
		t.Fatalf("the login result is incomplete: %s", stdout)
	}
	if len(browser.opened) != 0 || strings.Contains(stdout, sessionToken()[:20]) {
		t.Fatalf("an agent login opened a browser or printed the token: %v %s", browser.opened, stdout)
	}
	profile, _ := credential.Profile(server.controlURL())
	if _, err := store.Get(profile); err != nil {
		t.Fatalf("the session was not stored: %v", err)
	}
}

func TestAuthLoginTermsRequiredUnderAnAgentExitsZeroWithNext(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	server := newConsole(t, "required", true)
	dependencies := server.dependencies(credential.NewMemory(), underClaudeCode)
	dependencies.Interactive, dependencies.In = terminal, unreadable{t}
	exit, stdout, _ := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	events := decodeEvents(t, stdout)
	if exit != 0 || len(events) != 2 {
		t.Fatalf("required terms under an agent failed the login: exit=%d %s", exit, stdout)
	}
	result := events[1]
	documents, _ := result.Data["documents"].([]any)
	if result.Status != "ok" || result.Data["terms"] != "required" || result.Data["canAccept"] != true ||
		len(documents) != 1 || result.Next != "oe auth login --accept-terms" {
		t.Fatalf("required terms did not name the acceptance: %s", stdout)
	}
	if slices.Contains(server.requested(), "POST /api/cli/v1/terms/acceptance") {
		t.Fatal("a login without --accept-terms accepted the terms")
	}
}

func TestAuthLoginAcceptTermsSendsTheDetectedAgent(t *testing.T) {
	for _, test := range []struct {
		name   string
		getenv func(string) string
		want   map[string]any
	}{
		{"agent", underClaudeCode, map[string]any{"actor": "agent", "agentName": "claude-code"}},
		{"person", environment(nil), map[string]any{"actor": "person"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OE_ACCESS_TOKEN", "")
			server := newConsole(t, "required", true)
			store := credential.NewMemory()
			server.storeSession(t, store)
			dependencies := server.dependencies(store, test.getenv)
			exit, stdout, _ := run(t, dependencies, "--json", "--control-url", server.controlURL(), "auth", "login", "--accept-terms")
			result := decodeEvent(t, []byte(stdout))
			if exit != 0 || result.Data["terms"] != "accepted" || result.Data["changed"] != true || result.Next != "" {
				t.Fatalf("--accept-terms did not accept: exit=%d %s", exit, stdout)
			}
			if accepts := server.accepted(); len(accepts) != 1 || !maps.Equal(accepts[0], test.want) {
				t.Fatalf("acceptance body %v, want %v", accepts, test.want)
			}
			if slices.Contains(server.requested(), "POST /user_management/authorize/device") {
				t.Fatal("--accept-terms with a valid session started a device flow")
			}

			// The organization has accepted, so a second run records nothing.
			exit, stdout, _ = run(t, dependencies, "--json", "--control-url", server.controlURL(), "auth", "login", "--accept-terms")
			if again := decodeEvent(t, []byte(stdout)); exit != 0 || again.Data["changed"] != false || len(server.accepted()) != 1 {
				t.Fatalf("a second --accept-terms was not a no-op: exit=%d %s", exit, stdout)
			}
		})
	}
}

func TestAuthLoginAsksAPersonAtATerminal(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	for _, test := range []struct {
		answer string
		terms  string
	}{{"y\n", "accepted"}, {"n\n", "required"}} {
		server := newConsole(t, "required", true)
		store := credential.NewMemory()
		dependencies := server.dependencies(store, environment(nil))
		dependencies.Interactive, dependencies.OpenURL = terminal, (&opener{}).open
		dependencies.In = strings.NewReader(test.answer)
		exit, stdout, stderr := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
		if exit != 0 || !strings.Contains(stderr, "Relay service terms: https://open-e2ee.dev/legal/relay-terms/2026-08-26") ||
			!strings.Contains(stderr, "Accept these terms for org_example? [y/N]") {
			t.Fatalf("a person was not asked once with the documents: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
		}
		server.mu.Lock()
		state := server.state
		server.mu.Unlock()
		if state != test.terms {
			t.Fatalf("answer %q left the terms %s", test.answer, state)
		}
		profile, _ := credential.Profile(server.controlURL())
		if _, err := store.Get(profile); err != nil {
			t.Fatalf("answer %q removed the session: %v", test.answer, err)
		}
	}
}

func TestAcceptTermsWithoutPermissionExitsFiveAndKeepsTheSession(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	server := newConsole(t, "required", false)
	store := credential.NewMemory()
	server.storeSession(t, store)
	dependencies := server.dependencies(store, underClaudeCode)
	exit, stdout, _ := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	events := decodeEvents(t, stdout)
	if result := events[len(events)-1]; exit != 0 || result.Data["canAccept"] != false || result.Next != "" ||
		!strings.Contains(result.Message, "An administrator of org_example must accept them") {
		t.Fatalf("a member login did not name the administrator: exit=%d %s", exit, stdout)
	}

	exit, stdout, _ = run(t, dependencies, "--control-url", server.controlURL(), "auth", "login", "--accept-terms")
	failure := decodeEvent(t, []byte(stdout))
	if exit != exitPersonAction || failure.Command != "auth login" || failure.Code != "TERMS_PERMISSION_REQUIRED" || failure.Next != "" ||
		!strings.HasPrefix(failure.Error, "Your Organization role does not permit") {
		t.Fatalf("acceptance without permission: exit=%d %s", exit, stdout)
	}
	profile, _ := credential.Profile(server.controlURL())
	if _, err := store.Get(profile); err != nil {
		t.Fatalf("a refused acceptance removed the session: %v", err)
	}
}

func TestAuthStatusExitsFourWithoutASession(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	exit, stdout, _ := run(t, Dependencies{}, "--json", "auth", "status")
	failure := decodeEvent(t, []byte(stdout))
	if exit != exitAuthentication || failure.Command != "auth status" || failure.Code != "AUTHENTICATION_REQUIRED" || failure.Next != "oe auth login" {
		t.Fatalf("auth status without a session: exit=%d %s", exit, stdout)
	}
}

func TestAuthStatusReportsTermsAndTokenSource(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	server := newConsole(t, "required", false)
	store := credential.NewMemory()
	server.storeSession(t, store)
	exit, stdout, _ := run(t, server.dependencies(store, nil), "--json", "--control-url", server.controlURL(), "auth", "status")
	status := decodeEvent(t, []byte(stdout))
	organization, _ := status.Data["organization"].(map[string]any)
	// The memory store stands in for the OS keychain and names itself.
	if exit != 0 || status.Command != "auth status" || status.Data["user"] != "user_example" || organization["id"] != "org_example" ||
		status.Data["terms"] != "required" || status.Data["canAccept"] != false || status.Data["source"] != "memory" {
		t.Fatalf("auth status is incomplete: exit=%d %s", exit, stdout)
	}

	t.Setenv("OE_ACCESS_TOKEN", sessionToken())
	exit, stdout, _ = run(t, server.dependencies(credential.NewMemory(), nil), "--control-url", server.controlURL(), "auth", "status")
	if exit != 0 || !strings.Contains(stdout, "The credential is OE_ACCESS_TOKEN.") || !strings.Contains(stdout, "has not accepted the OpenE2EE terms.") {
		t.Fatalf("auth status did not name the environment credential: exit=%d %q", exit, stdout)
	}
}

func TestTopLevelLoginIsAUsageError(t *testing.T) {
	for _, verb := range []string{"login", "logout"} {
		exit, stdout, _ := run(t, Dependencies{}, "--json", verb)
		failure := decodeEvent(t, []byte(stdout))
		if exit != exitUsage || failure.Code != "USAGE_ERROR" || failure.Next != "oe help" {
			t.Fatalf("oe %s is still a command: exit=%d %s", verb, exit, stdout)
		}
	}
	exit, stdout, _ := run(t, Dependencies{}, "--json", "help")
	var help struct {
		Data struct {
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &help); exit != 0 || err != nil {
		t.Fatalf("help failed: exit=%d %v", exit, err)
	}
	var names []string
	for _, command := range help.Data.Commands {
		names = append(names, command.Name)
	}
	if !slices.Contains(names, "auth") || slices.Contains(names, "login") || slices.Contains(names, "logout") {
		t.Fatalf("help lists %v", names)
	}
	var stderr bytes.Buffer
	if exit := Run(t.Context(), []string{"auth", "bogus"}, Dependencies{Out: io.Discard, Err: &stderr, WorkingDir: t.TempDir()}); exit != exitUsage ||
		!strings.HasSuffix(stderr.String(), "next: oe help auth\n") {
		t.Fatalf("an unknown auth command: exit=%d %q", exit, stderr.String())
	}
}

func TestTermsRefusalNamesTheCommandToRetry(t *testing.T) {
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
		return control.Project{}, &control.APIError{
			Status: 409, Code: "TERMS_REQUIRED", Message: "Accept the OpenE2EE terms first.", CanAccept: true,
			Documents: []control.TermsDocument{{Name: "Relay service terms", URL: "https://open-e2ee.dev/legal/relay-terms/2026-08-26", Version: "relay-2026-08-26"}},
		}
	}}
	exit, stdout, _ := run(t, Dependencies{API: api, Store: store}, "--json", "project", "show", "any-chat")
	failure := decodeEvent(t, []byte(stdout))
	documents, _ := failure.Data["documents"].([]any)
	if exit != exitPersonAction || failure.Next != "oe auth login --accept-terms" || failure.Data["retry"] != "oe --json project show any-chat" ||
		failure.Data["canAccept"] != true || len(documents) != 1 {
		t.Fatalf("a terms refusal did not name the acceptance and the retry: exit=%d %s", exit, stdout)
	}
}

// setupPrompt is the first line of the post-login prompt.
const setupPrompt = "What do you want to do in this directory?"

// appDirectory is a new directory named name that holds the package.json of
// an app and no config.
func appDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := emptyDirectory(t, name)
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"`+name+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}

// personLogin runs the CLI for a person at a terminal, with no agent, in
// directory. answers are the lines that the person types.
func personLogin(server *console, store credential.Store, directory, answers string) Dependencies {
	dependencies := server.dependencies(store, environment(nil))
	dependencies.Interactive, dependencies.OpenURL = terminal, (&opener{}).open
	dependencies.WorkingDir, dependencies.In = directory, strings.NewReader(answers)
	return dependencies
}

// projectRoutes adds the project create, list, and read routes to the
// console. The account can read one project, chat-demo.
func projectRoutes(t *testing.T, server *console) {
	t.Helper()
	answer := func(response http.ResponseWriter, body string) {
		response.Header().Set("Content-Type", "application/json")
		io.WriteString(response, body)
	}
	server.routes.HandleFunc("POST /api/cli/v1/projects/bootstrap", func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode the bootstrap body: %v", err)
		}
		_, created := created(1, body)
		answer(response, created)
	})
	server.routes.HandleFunc("GET /api/cli/v1/projects", func(response http.ResponseWriter, _ *http.Request) {
		answer(response, `[{"name":"Chat demo","product":"signal-relay","production":{"state":"active"},"slug":"chat-demo"}]`)
	})
	server.routes.HandleFunc("GET /api/cli/v1/projects/chat-demo", func(response http.ResponseWriter, _ *http.Request) {
		answer(response, fmt.Sprintf(linkedProject, "chat-demo"))
	})
}

func TestLoginOffersCreateLinkSkipInAnAppDirectory(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	for _, test := range []struct {
		name    string
		terms   string
		answers string
		// project is the project of the config after the run, or "" for no
		// config.
		project string
		output  string
	}{
		{"create", "accepted", "1\n", "prompt-chat", "Created project prompt-chat with its Sandbox environment"},
		{"link", "accepted", "2\n1\n", "chat-demo", "Linked chat-demo."},
		{"skip", "accepted", "3\n", "", "next: oe new"},
		{"no answer", "accepted", "", "", "next: oe new"},
		// The terms prompt and the setup prompt read one line each.
		{"after the terms", "required", "y\n2\n1\n", "chat-demo", "Linked chat-demo."},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newConsole(t, test.terms, true)
			projectRoutes(t, server)
			store := credential.NewMemory()
			directory := appDirectory(t, "prompt-chat")
			exit, stdout, stderr := run(t, personLogin(server, store, directory, test.answers), "--control-url", server.controlURL(), "auth", "login")
			if exit != 0 || !strings.Contains(stdout, "Logged in to org_example.") || !strings.Contains(stdout, test.output) {
				t.Fatalf("answer %q: exit=%d stdout=%q stderr=%q", test.answers, exit, stdout, stderr)
			}
			for _, line := range []string{setupPrompt, "1. Create a new signal-relay project (oe new)", "2. Link an existing project (oe link)", "3. Skip"} {
				if !strings.Contains(stderr, line) {
					t.Fatalf("the prompt has no %q: %q", line, stderr)
				}
			}
			// The prompt names only the setup commands.
			if prompt := stderr[strings.Index(stderr, setupPrompt):]; strings.Count(prompt, "oe ") != 2 {
				t.Fatalf("the prompt names a command other than oe new and oe link: %q", prompt)
			}
			path := filepath.Join(directory, config.Filename)
			if test.project == "" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("a skip wrote %s: %v", config.Filename, err)
				}
				if slices.ContainsFunc(server.requested(), func(request string) bool { return strings.Contains(request, "/v1/projects") }) {
					t.Fatalf("a skip called a project route: %q", server.requested())
				}
			} else if linked, err := config.Load(path); err != nil || linked.Project != test.project {
				t.Fatalf("the config holds %#v (%v), want project %s", linked, err, test.project)
			}
			profile, _ := credential.Profile(server.controlURL())
			if _, err := store.Get(profile); err != nil {
				t.Fatalf("the session was not kept: %v", err)
			}
		})
	}
}

func TestLoginOutsideAnAppDirectoryOffersNothing(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	// A package.json in a parent does not make an app directory.
	child := filepath.Join(appDirectory(t, "parent-chat"), "notes")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{emptyDirectory(t, "plain"), child} {
		server := newConsole(t, "accepted", true)
		dependencies := personLogin(server, credential.NewMemory(), directory, "")
		dependencies.In = unreadable{t}
		exit, stdout, stderr := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
		if exit != 0 || !strings.Contains(stdout, "Logged in to org_example.") || strings.Contains(stdout, "next:") || strings.Contains(stderr, setupPrompt) {
			t.Fatalf("a login in %s offered a setup or a next command: exit=%d stdout=%q stderr=%q", directory, exit, stdout, stderr)
		}
		if names := entries(t, directory); len(names) != 0 {
			t.Fatalf("a login in %s wrote %v", directory, names)
		}
	}
}

func TestLoginInASetUpDirectoryOffersNothing(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	root := initializedProject(t, "set-chat")
	web := filepath.Join(root, "web")
	if err := os.Mkdir(web, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, web} {
		if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"set-chat"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source := mustRead(t, filepath.Join(root, config.Filename))
	for _, directory := range []string{root, web} {
		server := newConsole(t, "accepted", true)
		dependencies := personLogin(server, credential.NewMemory(), directory, "")
		dependencies.In = unreadable{t}
		exit, stdout, stderr := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
		if exit != 0 || !strings.HasSuffix(stdout, "\nnext: oe link\n") || strings.Contains(stderr, setupPrompt) {
			t.Fatalf("a login in the set-up %s: exit=%d stdout=%q stderr=%q", directory, exit, stdout, stderr)
		}
	}
	if after := mustRead(t, filepath.Join(root, config.Filename)); !bytes.Equal(after, source) {
		t.Fatalf("a login changed the config:\n%s", after)
	}
}

func TestLoginUnderAnAgentNamesNewInNext(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "")
	directory := appDirectory(t, "agent-chat")
	server := newConsole(t, "accepted", true)
	dependencies := server.dependencies(credential.NewMemory(), underClaudeCode)
	dependencies.Interactive, dependencies.In, dependencies.WorkingDir = terminal, unreadable{t}, directory
	exit, stdout, stderr := run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	if events := decodeEvents(t, stdout); exit != 0 || len(events) != 2 || stderr != "" || events[1].Next != "oe new" {
		t.Fatalf("an agent login in an app directory did not name oe new: exit=%d stdout=%s stderr=%q", exit, stdout, stderr)
	}

	// A person without a terminal gets the same next and no prompt.
	dependencies = personLogin(server, credential.NewMemory(), directory, "")
	dependencies.Interactive, dependencies.In = func() bool { return false }, unreadable{t}
	exit, stdout, stderr = run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	if exit != 0 || !strings.HasSuffix(stdout, "\nnext: oe new\n") || strings.Contains(stderr, setupPrompt) {
		t.Fatalf("a login without a terminal: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}

	// Terms that stay required keep the terms next. After --accept-terms,
	// next is oe new.
	server = newConsole(t, "required", true)
	store := credential.NewMemory()
	server.storeSession(t, store)
	dependencies = server.dependencies(store, underClaudeCode)
	dependencies.Interactive, dependencies.In, dependencies.WorkingDir = terminal, unreadable{t}, directory
	exit, stdout, _ = run(t, dependencies, "--control-url", server.controlURL(), "auth", "login")
	if events := decodeEvents(t, stdout); exit != 0 || events[len(events)-1].Next != "oe auth login --accept-terms" {
		t.Fatalf("required terms did not keep the terms next: exit=%d %s", exit, stdout)
	}
	exit, stdout, _ = run(t, dependencies, "--control-url", server.controlURL(), "auth", "login", "--accept-terms")
	if result := decodeEvent(t, []byte(stdout)); exit != 0 || result.Data["terms"] != "accepted" || result.Next != "oe new" {
		t.Fatalf("an accepted login in an app directory did not name oe new: exit=%d %s", exit, stdout)
	}
	if _, err := os.Stat(filepath.Join(directory, config.Filename)); !os.IsNotExist(err) {
		t.Fatalf("a login under an agent wrote %s: %v", config.Filename, err)
	}
}
