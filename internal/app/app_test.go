package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-e2ee/cli/internal/config"
	"github.com/open-e2ee/cli/internal/control"
	"github.com/open-e2ee/cli/internal/credential"
)

const (
	developmentRelayURL = "https://development.relay.open-e2ee.dev/v1/connection/pk_dev_public"
	productionRelayURL  = "https://relay.open-e2ee.dev/v1/connection/pk_prod_public"
)

func TestLoginStoresBrowserCredentialWithoutPrintingToken(t *testing.T) {
	store := credential.NewMemory()
	api := &fakeAPI{
		startAuthorization: func(context.Context, control.AuthorizationRequest) (control.Authorization, error) {
			return control.Authorization{DeviceCode: "authorization", VerificationURL: "https://login.example/device", UserCode: "ABCD", IntervalSeconds: 1}, nil
		},
		pollAuthorization: func(context.Context, control.Authorization) (control.Token, error) {
			return control.Token{AccessToken: "browser-secret"}, nil
		},
	}
	var stdout bytes.Buffer
	var opened string
	exit := Run(context.Background(), []string{"--json", "login"}, Dependencies{
		API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: t.TempDir(),
		OpenURL: func(target string) error { opened = target; return nil },
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
	if exit != 0 {
		t.Fatalf("login failed: %s", stdout.String())
	}
	if opened != "https://login.example/device" {
		t.Fatalf("browser did not open: %q", opened)
	}
	if strings.Contains(stdout.String(), "browser-secret") {
		t.Fatal("login output exposed the access token")
	}
	profile, err := credential.Profile(defaultControlURL)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(profile)
	if err != nil || stored.AccessToken != "browser-secret" {
		t.Fatalf("credential was not stored: %#v, %v", stored, err)
	}
}

func TestDevBootstrapsWithoutBillingAndWaitsForAcknowledgement(t *testing.T) {
	directory := initializedProject(t, "managed-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "project:write")
	activationCalls := 0
	api := &fakeAPI{
		bootstrapDevelopment: func(_ context.Context, request control.CredentialRequest, bootstrap control.BootstrapRequest) (control.Bootstrap, error) {
			if request.OperationID == "" {
				t.Fatal("bootstrap omitted idempotency key")
			}
			if bootstrap.Writer != "config" || bootstrap.Policy.DeliveryTtlSeconds != 86_400 || bootstrap.Policy.AttachmentRetentionSeconds != 86_400 {
				t.Fatalf("unexpected writer %q", bootstrap.Writer)
			}
			return control.Bootstrap{
				ProjectSlug: "managed-chat", Writer: "config", Environment: "development",
				DevelopmentRelayURL: developmentRelayURL,
			}, nil
		},
		activation: func(context.Context, control.CredentialRequest, string) (control.Activation, error) {
			activationCalls++
			if activationCalls == 1 {
				return control.Activation{FirstDevice: true}, nil
			}
			return control.Activation{FirstDevice: true, FirstAcknowledged: true}, nil
		},
	}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--environment", "development", "--json", "dev", "--timeout", "1s"}, Dependencies{
		API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if exit != 0 {
		t.Fatalf("dev failed: %s", stdout.String())
	}
	value, err := config.Load(filepath.Join(directory, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if value.Environments["development"].RelayURL != developmentRelayURL || value.Environments["production"].RelayURL != "" {
		t.Fatalf("development Relay connection was not written in isolation: %#v", value.Environments)
	}
	environment, err := os.ReadFile(filepath.Join(directory, ".env.local"))
	if err != nil || !strings.Contains(string(environment), "OPEN_E2EE_RELAY_URL="+developmentRelayURL) {
		t.Fatalf("development environment was not installed: %q %v", environment, err)
	}
	if activationCalls != 2 || !strings.Contains(stdout.String(), "firstAcknowledgedMessage") {
		t.Fatalf("did not wait for first acknowledgement: calls=%d output=%s", activationCalls, stdout.String())
	}
}

func TestDeployRequiresBillingAndExplicitJSONConfirmation(t *testing.T) {
	directory := initializedProject(t, "production-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	deployed := false
	api := &fakeAPI{
		getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{Slug: "production-chat", Writer: "config", Production: projectEnvironment(productionRelayURL, "7")}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{ID: "plan-1", ProjectSlug: "production-chat", Environment: "production", ExpectedRevision: "7", BillingReady: true, Changes: []control.Change{{Path: "relay.deliveryRetentionSeconds"}}}, nil
		},
		deploy: func(_ context.Context, request control.CredentialRequest, deployment control.DeployRequest) (control.Deployment, error) {
			deployed = true
			if request.OperationID == "" || deployment.ExpectedRevision != "7" || deployment.ProjectSlug != "production-chat" || deployment.Policy.DeliveryTtlSeconds != 2_592_000 {
				t.Fatalf("deploy lost concurrency contract: %#v %#v", request, deployment)
			}
			return control.Deployment{ID: "deployment-1", Revision: "revision-8", Status: "complete", RelayURL: productionRelayURL}, nil
		},
	}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "deploy"}, Dependencies{API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit == 0 || deployed {
		t.Fatalf("JSON deploy did not require --confirm: deployed=%v output=%s", deployed, stdout.String())
	}
	stdout.Reset()
	exit = Run(context.Background(), []string{"--json", "deploy", "--confirm"}, Dependencies{API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit != 0 || !deployed {
		t.Fatalf("confirmed deploy failed: deployed=%v output=%s", deployed, stdout.String())
	}
	environment, err := os.ReadFile(filepath.Join(directory, ".env.production.local"))
	if err != nil || !strings.Contains(string(environment), "OPEN_E2EE_RELAY_URL="+productionRelayURL) {
		t.Fatalf("production environment was not installed: %q %v", environment, err)
	}
	if !strings.Contains(stdout.String(), `"configurationFile":".env.production.local"`) || strings.Contains(stdout.String(), productionRelayURL) {
		t.Fatalf("production handoff was incomplete or exposed the connection: %s", stdout.String())
	}
}

func TestDoctorReportsSafeConnectionOriginAndRefusesProjectDrift(t *testing.T) {
	directory := initializedProject(t, "doctor-chat")
	path := filepath.Join(directory, config.Filename)
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	development := value.Environments["development"]
	development.RelayURL = developmentRelayURL
	value.Environments["development"] = development
	if err := config.Write(path, value); err != nil {
		t.Fatal(err)
	}
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
		return control.Project{Slug: "doctor-chat", Development: projectEnvironment(developmentRelayURL, "1")}, nil
	}}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != developmentRelayURL {
			t.Fatalf("doctor requested an unexpected URL: %s", request.URL)
		}
		return &http.Response{Body: io.NopCloser(strings.NewReader(`{"schemaVersion":1}`)), Header: make(http.Header), StatusCode: http.StatusOK}, nil
	})}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--environment", "development", "--json", "doctor"}, Dependencies{API: api, HTTP: httpClient, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit != 0 || !strings.Contains(stdout.String(), `"relayOrigin":"https://development.relay.open-e2ee.dev"`) || strings.Contains(stdout.String(), "pk_dev_public") {
		t.Fatalf("doctor did not report only the safe origin: %s", stdout.String())
	}
	api.getProject = func(context.Context, control.CredentialRequest, string) (control.Project, error) {
		return control.Project{Slug: "doctor-chat", Development: projectEnvironment("https://development.relay.open-e2ee.dev/v1/connection/another-project", "1")}, nil
	}
	stdout.Reset()
	exit = Run(context.Background(), []string{"--environment", "development", "--json", "doctor"}, Dependencies{API: api, HTTP: httpClient, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit == 0 || !strings.Contains(stdout.String(), "stale or belongs to another project") || strings.Contains(stdout.String(), "pk_dev_public") {
		t.Fatalf("doctor did not refuse project drift safely: %s", stdout.String())
	}
}

func TestDeployOpensCardSetupBeforeProductionMutation(t *testing.T) {
	directory := initializedProject(t, "billing-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	var opened string
	api := &fakeAPI{
		getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{Slug: "billing-chat", Writer: "config"}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{ID: "plan", ProjectSlug: "billing-chat", Environment: "production", ExpectedRevision: "0", BillingSetupURL: "https://billing.example/setup"}, nil
		},
	}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "deploy", "--confirm"}, Dependencies{
		API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory,
		OpenURL: func(target string) error { opened = target; return nil },
	})
	if exit == 0 || opened != "https://billing.example/setup" {
		t.Fatalf("card setup gate did not stop deploy: opened=%q output=%s", opened, stdout.String())
	}
}

func TestConsoleWriterFailsBeforeRemoteMutation(t *testing.T) {
	directory := initializedProject(t, "console-chat")
	path := filepath.Join(directory, config.Filename)
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	value.Writer = "console"
	if err := config.Write(path, value); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{bootstrapDevelopment: func(context.Context, control.CredentialRequest, control.BootstrapRequest) (control.Bootstrap, error) {
		t.Fatal("console-first project reached bootstrap mutation")
		return control.Bootstrap{}, nil
	}}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--environment", "development", "--json", "dev"}, Dependencies{API: api, Store: credential.NewMemory(), Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit == 0 || !strings.Contains(stdout.String(), "console-first") {
		t.Fatalf("console writer was not rejected: %s", stdout.String())
	}
}

func TestProjectSelectionReplacesRelayConnections(t *testing.T) {
	directory := initializedProject(t, "old-chat")
	path := filepath.Join(directory, config.Filename)
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	development := value.Environments["development"]
	development.RelayURL = "https://development.relay.open-e2ee.dev/v1/connection/old-development"
	value.Environments["development"] = development
	production := value.Environments["production"]
	production.RelayURL = "https://relay.open-e2ee.dev/v1/connection/old-production"
	value.Environments["production"] = production
	if err := config.Write(path, value); err != nil {
		t.Fatal(err)
	}
	if err := writeRelayEnvironment(directory, ".env.local", development.RelayURL); err != nil {
		t.Fatal(err)
	}
	if err := writeRelayEnvironment(directory, ".env.production.local", production.RelayURL); err != nil {
		t.Fatal(err)
	}
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
		return control.Project{Slug: "new-chat", Writer: "config", Development: projectEnvironment(developmentRelayURL, "1"), Production: projectEnvironment(productionRelayURL, "1")}, nil
	}}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "project", "select", "new-chat"}, Dependencies{API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit != 0 {
		t.Fatalf("project select failed: %s", stdout.String())
	}
	selected, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Project != "new-chat" || selected.Environments["development"].RelayURL != developmentRelayURL || selected.Environments["production"].RelayURL != productionRelayURL {
		t.Fatalf("project Relay connections were not replaced: %#v", selected)
	}
	for filename, expected := range map[string]string{
		".env.local":            developmentRelayURL,
		".env.production.local": productionRelayURL,
	} {
		contents, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil || !strings.Contains(string(contents), "OPEN_E2EE_RELAY_URL="+expected) || strings.Contains(string(contents), "old-") {
			t.Fatalf("%s did not converge to the selected project: %q %v", filename, contents, err)
		}
	}
}

func TestRelayEnvironmentRemovalCannotLeaveAnotherProjectConnection(t *testing.T) {
	directory := t.TempDir()
	filename := ".env.production.local"
	if err := writeRelayEnvironment(directory, filename, productionRelayURL); err != nil {
		t.Fatal(err)
	}
	if err := writeRelayEnvironment(directory, filename, ""); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, filename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "OPEN_E2EE_RELAY_URL=") || strings.Contains(string(contents), productionRelayURL) {
		t.Fatalf("removed environment retained a Relay connection: %q", contents)
	}
	if !strings.Contains(string(contents), "not configured for this environment") {
		t.Fatalf("removed environment has no exact handoff: %q", contents)
	}
}

func TestValidatePlanRejectsCrossBoundaryResponses(t *testing.T) {
	project := control.Project{Slug: "project-one", Production: projectEnvironment(productionRelayURL, "1")}
	valid := control.Plan{ID: "plan-1", ProjectSlug: "project-one", Environment: "production", ExpectedRevision: "1"}
	if err := validatePlan(valid, project, "production"); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]control.Plan{
		"missing id":        {ProjectSlug: "project-one", Environment: "production", ExpectedRevision: "1"},
		"wrong project":     {ID: "plan-1", ProjectSlug: "project-two", Environment: "production", ExpectedRevision: "1"},
		"wrong environment": {ID: "plan-1", ProjectSlug: "project-one", Environment: "development", ExpectedRevision: "1"},
		"stale revision":    {ID: "plan-1", ProjectSlug: "project-one", Environment: "production", ExpectedRevision: "0"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePlan(changed, project, "production"); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestJSONOutputIsOneDocumentAfterAutomaticLogin(t *testing.T) {
	directory := initializedProject(t, "login-dev")
	api := &fakeAPI{
		startAuthorization: func(context.Context, control.AuthorizationRequest) (control.Authorization, error) {
			return control.Authorization{DeviceCode: "auth", VerificationURL: "https://login.example", UserCode: "CODE"}, nil
		},
		pollAuthorization: func(context.Context, control.Authorization) (control.Token, error) {
			return control.Token{AccessToken: "token"}, nil
		},
		bootstrapDevelopment: func(context.Context, control.CredentialRequest, control.BootstrapRequest) (control.Bootstrap, error) {
			return control.Bootstrap{ProjectSlug: "login-dev", Writer: "config", Environment: "development", DevelopmentRelayURL: developmentRelayURL}, nil
		},
	}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--environment", "development", "--json", "dev", "--no-wait"}, Dependencies{
		API: api, Store: credential.NewMemory(), Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory,
		OpenURL: func(string) error { return nil }, Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if exit != 0 {
		t.Fatalf("automatic login failed: %s", stdout.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var first map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	var second map[string]any
	if err := decoder.Decode(&second); !errors.Is(err, io.EOF) {
		t.Fatalf("JSON mode emitted multiple documents: %s", stdout.String())
	}
}

func TestTransientRefreshKeepsAStillValidSession(t *testing.T) {
	directory := initializedProject(t, "refresh-chat")
	store := credential.NewMemory()
	profile, err := credential.Profile(defaultControlURL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.Set(profile, credential.Credential{
		AccessToken: "still-valid-token", ExpiresAt: now.Add(30 * time.Second).Format(time.RFC3339),
		RefreshToken: "refresh-token",
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{
		refreshAuthorization: func(context.Context, string) (control.Token, error) {
			return control.Token{}, errors.New("temporary WorkOS failure")
		},
		getProject: func(_ context.Context, request control.CredentialRequest, _ string) (control.Project, error) {
			if request.AccessToken != "still-valid-token" {
				t.Fatalf("request did not retain the current session: %#v", request)
			}
			return control.Project{Slug: "refresh-chat", Writer: "config"}, nil
		},
	}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "project", "show"}, Dependencies{
		API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{},
		WorkingDir: directory, Now: func() time.Time { return now },
	})
	if exit != 0 {
		t.Fatalf("transient refresh rejected a valid session: %s", stdout.String())
	}
}

func TestTerminalRefreshRemovesTheExpiredSession(t *testing.T) {
	directory := initializedProject(t, "expired-chat")
	store := credential.NewMemory()
	profile, err := credential.Profile(defaultControlURL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.Set(profile, credential.Credential{
		AccessToken: "expired-token", ExpiresAt: now.Add(30 * time.Second).Format(time.RFC3339),
		RefreshToken: "expired-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{refreshAuthorization: func(context.Context, string) (control.Token, error) {
		return control.Token{}, control.ErrSessionExpired
	}}
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "project", "show"}, Dependencies{
		API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{},
		WorkingDir: directory, Now: func() time.Time { return now },
	})
	if exit == 0 || !strings.Contains(stdout.String(), "run oe login again") {
		t.Fatalf("terminal refresh did not require login: %s", stdout.String())
	}
	if _, err := store.Get(profile); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("terminal session remained stored: %v", err)
	}
}

func projectEnvironment(relayURL, revision string) *control.ProjectEnvironment {
	return &control.ProjectEnvironment{
		AttachmentRetentionSeconds: 86_400,
		DeliveryTtlSeconds:         86_400,
		RelayURL:                   relayURL,
		Revision:                   revision,
	}
}

func initializedProject(t *testing.T, project string) string {
	t.Helper()
	directory := t.TempDir()
	if err := config.Write(filepath.Join(directory, config.Filename), config.New(project)); err != nil {
		t.Fatal(err)
	}
	return directory
}

func storeCredential(t *testing.T, store credential.Store, scopes ...string) {
	t.Helper()
	profile, err := credential.Profile(defaultControlURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(profile, credential.Credential{AccessToken: "test-token", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
}

type fakeAPI struct {
	startAuthorization   func(context.Context, control.AuthorizationRequest) (control.Authorization, error)
	pollAuthorization    func(context.Context, control.Authorization) (control.Token, error)
	refreshAuthorization func(context.Context, string) (control.Token, error)
	bootstrapDevelopment func(context.Context, control.CredentialRequest, control.BootstrapRequest) (control.Bootstrap, error)
	activation           func(context.Context, control.CredentialRequest, string) (control.Activation, error)
	plan                 func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error)
	deploy               func(context.Context, control.CredentialRequest, control.DeployRequest) (control.Deployment, error)
	getProject           func(context.Context, control.CredentialRequest, string) (control.Project, error)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (f *fakeAPI) Health(context.Context) error { return nil }
func (f *fakeAPI) StartAuthorization(ctx context.Context, request control.AuthorizationRequest) (control.Authorization, error) {
	if f.startAuthorization == nil {
		return control.Authorization{}, errors.New("unexpected StartAuthorization")
	}
	return f.startAuthorization(ctx, request)
}
func (f *fakeAPI) PollAuthorization(ctx context.Context, authorization control.Authorization) (control.Token, error) {
	if f.pollAuthorization == nil {
		return control.Token{}, errors.New("unexpected PollAuthorization")
	}
	return f.pollAuthorization(ctx, authorization)
}
func (f *fakeAPI) RefreshAuthorization(ctx context.Context, refreshToken string) (control.Token, error) {
	if f.refreshAuthorization == nil {
		return control.Token{}, errors.New("unexpected RefreshAuthorization")
	}
	return f.refreshAuthorization(ctx, refreshToken)
}
func (f *fakeAPI) BootstrapDevelopment(ctx context.Context, credential control.CredentialRequest, request control.BootstrapRequest) (control.Bootstrap, error) {
	if f.bootstrapDevelopment == nil {
		return control.Bootstrap{}, errors.New("unexpected BootstrapDevelopment")
	}
	return f.bootstrapDevelopment(ctx, credential, request)
}
func (f *fakeAPI) Activation(ctx context.Context, credential control.CredentialRequest, project string) (control.Activation, error) {
	if f.activation == nil {
		return control.Activation{}, errors.New("unexpected Activation")
	}
	return f.activation(ctx, credential, project)
}
func (f *fakeAPI) Plan(ctx context.Context, credential control.CredentialRequest, request control.PlanRequest) (control.Plan, error) {
	if f.plan == nil {
		return control.Plan{}, errors.New("unexpected Plan")
	}
	return f.plan(ctx, credential, request)
}
func (f *fakeAPI) Deploy(ctx context.Context, credential control.CredentialRequest, request control.DeployRequest) (control.Deployment, error) {
	if f.deploy == nil {
		return control.Deployment{}, errors.New("unexpected Deploy")
	}
	return f.deploy(ctx, credential, request)
}
func (f *fakeAPI) GetProject(ctx context.Context, credential control.CredentialRequest, project string) (control.Project, error) {
	if f.getProject == nil {
		return control.Project{}, errors.New("unexpected GetProject")
	}
	return f.getProject(ctx, credential, project)
}
