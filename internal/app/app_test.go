package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-e2ee/cli/internal/config"
	"github.com/open-e2ee/cli/internal/control"
	"github.com/open-e2ee/cli/internal/credential"
)

func TestLoginStoresBrowserCredentialWithoutPrintingToken(t *testing.T) {
	store := credential.NewMemory()
	api := &fakeAPI{
		startAuthorization: func(context.Context, control.AuthorizationRequest) (control.Authorization, error) {
			return control.Authorization{ID: "authorization", VerificationURL: "https://login.example/device", UserCode: "ABCD", IntervalSeconds: 1}, nil
		},
		pollAuthorization: func(context.Context, string) (control.Token, error) {
			return control.Token{AccessToken: "browser-secret", Scopes: []string{"project:read"}}, nil
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
	stored, err := store.Get(defaultControlURL)
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
			if bootstrap.Writer != "config" {
				t.Fatalf("unexpected writer %q", bootstrap.Writer)
			}
			return control.Bootstrap{
				ProjectSlug: "managed-chat", Writer: "config", Environment: "development",
				DevelopmentPublicKey: "pk_dev_public", ProductionPublicKey: "pk_prod_public",
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
	if value.Environments["development"].PublishableKey != "pk_dev_public" || value.Environments["production"].PublishableKey != "pk_prod_public" {
		t.Fatalf("publishable configuration not written: %#v", value.Environments)
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
			return control.Project{Slug: "production-chat", Writer: "config", Revision: "revision-7"}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{ID: "plan-1", Environment: "production", ExpectedRevision: "revision-7", BillingReady: true, Changes: []control.Change{{Path: "relay.deliveryRetention"}}}, nil
		},
		deploy: func(_ context.Context, request control.CredentialRequest, deployment control.DeployRequest) (control.Deployment, error) {
			deployed = true
			if request.OperationID == "" || deployment.ExpectedRevision != "revision-7" {
				t.Fatalf("deploy lost concurrency contract: %#v %#v", request, deployment)
			}
			return control.Deployment{ID: "deployment-1", Revision: "revision-8", Status: "complete"}, nil
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
}

func TestDeployOpensCardSetupBeforeProductionMutation(t *testing.T) {
	directory := initializedProject(t, "billing-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "deploy:write")
	var opened string
	api := &fakeAPI{
		getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
			return control.Project{Slug: "billing-chat", Writer: "config", Revision: "one"}, nil
		},
		plan: func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error) {
			return control.Plan{ID: "plan", Environment: "production", ExpectedRevision: "one", BillingSetupURL: "https://billing.example/setup"}, nil
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

func TestSecretValueIsNeverPrintedOrWritten(t *testing.T) {
	directory := initializedProject(t, "secret-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "secret:write")
	var captured string
	api := &fakeAPI{setSecret: func(_ context.Context, request control.CredentialRequest, secret control.SecretRequest) error {
		if request.OperationID == "" {
			t.Fatal("secret mutation omitted idempotency key")
		}
		captured = secret.Value
		return nil
	}}
	t.Setenv("TEST_RELAY_SECRET", "highly-sensitive-value")
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "secret", "set", "AUTH_PRIVATE_KEY", "--from-env", "TEST_RELAY_SECRET"}, Dependencies{API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit != 0 || captured != "highly-sensitive-value" {
		t.Fatalf("secret set failed: captured=%q output=%s", captured, stdout.String())
	}
	if strings.Contains(stdout.String(), captured) {
		t.Fatal("secret output exposed the value")
	}
	contents, err := os.ReadFile(filepath.Join(directory, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), captured) {
		t.Fatal("config file contained the secret value")
	}
}

func TestSecretValueIsRedactedFromControlError(t *testing.T) {
	directory := initializedProject(t, "secret-error-chat")
	store := credential.NewMemory()
	storeCredential(t, store, "secret:write")
	api := &fakeAPI{setSecret: func(_ context.Context, _ control.CredentialRequest, secret control.SecretRequest) error {
		return errors.New("server rejected " + secret.Value)
	}}
	t.Setenv("TEST_RELAY_SECRET", "must-never-appear")
	var stdout bytes.Buffer
	exit := Run(context.Background(), []string{"--json", "secret", "set", "AUTH_KEY", "--from-env", "TEST_RELAY_SECRET"}, Dependencies{API: api, Store: store, Out: &stdout, Err: &bytes.Buffer{}, WorkingDir: directory})
	if exit == 0 || strings.Contains(stdout.String(), "must-never-appear") || !strings.Contains(stdout.String(), "[redacted]") {
		t.Fatalf("secret error was not redacted: %s", stdout.String())
	}
}

func TestProjectSelectionReplacesPublishableKeys(t *testing.T) {
	directory := initializedProject(t, "old-chat")
	path := filepath.Join(directory, config.Filename)
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	development := value.Environments["development"]
	development.PublishableKey = "old-development"
	value.Environments["development"] = development
	production := value.Environments["production"]
	production.PublishableKey = "old-production"
	value.Environments["production"] = production
	if err := config.Write(path, value); err != nil {
		t.Fatal(err)
	}
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	api := &fakeAPI{getProject: func(context.Context, control.CredentialRequest, string) (control.Project, error) {
		return control.Project{Slug: "new-chat", Writer: "config", DevelopmentPublishableKey: "new-development", ProductionPublishableKey: "new-production"}, nil
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
	if selected.Project != "new-chat" || selected.Environments["development"].PublishableKey != "new-development" || selected.Environments["production"].PublishableKey != "new-production" {
		t.Fatalf("project keys were not replaced: %#v", selected)
	}
}

func TestValidatePlanRejectsCrossBoundaryResponses(t *testing.T) {
	project := control.Project{ID: "project-1", Revision: "revision-1"}
	valid := control.Plan{ID: "plan-1", ProjectID: "project-1", Environment: "production", ExpectedRevision: "revision-1"}
	if err := validatePlan(valid, project, "production"); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]control.Plan{
		"missing id":        {ProjectID: "project-1", Environment: "production", ExpectedRevision: "revision-1"},
		"wrong project":     {ID: "plan-1", ProjectID: "project-2", Environment: "production", ExpectedRevision: "revision-1"},
		"wrong environment": {ID: "plan-1", ProjectID: "project-1", Environment: "development", ExpectedRevision: "revision-1"},
		"stale revision":    {ID: "plan-1", ProjectID: "project-1", Environment: "production", ExpectedRevision: "revision-0"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePlan(changed, project, "production"); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestAdvancedInputValidation(t *testing.T) {
	if err := validateProvider("device-owned", ""); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][2]string{{"unknown", ""}, {"oidc", "http://issuer.example"}, {"clerk", ""}, {"device-owned", "https://issuer.example"}} {
		if err := validateProvider(input[0], input[1]); err == nil {
			t.Fatalf("accepted provider %#v", input)
		}
	}
	for _, name := range []string{"lowercase", "_LEADING", "TRAILING_", "DOUBLE__SEPARATOR", "HAS-DASH"} {
		if validSecretName(name) {
			t.Fatalf("accepted secret name %q", name)
		}
	}
}

func TestJSONOutputIsOneDocumentAfterAutomaticLogin(t *testing.T) {
	directory := initializedProject(t, "login-dev")
	api := &fakeAPI{
		startAuthorization: func(context.Context, control.AuthorizationRequest) (control.Authorization, error) {
			return control.Authorization{ID: "auth", VerificationURL: "https://login.example", UserCode: "CODE"}, nil
		},
		pollAuthorization: func(context.Context, string) (control.Token, error) {
			return control.Token{AccessToken: "token", Scopes: []string{"project:write"}}, nil
		},
		bootstrapDevelopment: func(context.Context, control.CredentialRequest, control.BootstrapRequest) (control.Bootstrap, error) {
			return control.Bootstrap{ProjectSlug: "login-dev", Writer: "config", Environment: "development", DevelopmentPublicKey: "dev", ProductionPublicKey: "prod"}, nil
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
	if err := store.Set(defaultControlURL, credential.Credential{AccessToken: "test-token", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
}

type fakeAPI struct {
	startAuthorization   func(context.Context, control.AuthorizationRequest) (control.Authorization, error)
	pollAuthorization    func(context.Context, string) (control.Token, error)
	bootstrapDevelopment func(context.Context, control.CredentialRequest, control.BootstrapRequest) (control.Bootstrap, error)
	activation           func(context.Context, control.CredentialRequest, string) (control.Activation, error)
	plan                 func(context.Context, control.CredentialRequest, control.PlanRequest) (control.Plan, error)
	deploy               func(context.Context, control.CredentialRequest, control.DeployRequest) (control.Deployment, error)
	getProject           func(context.Context, control.CredentialRequest, string) (control.Project, error)
	setSecret            func(context.Context, control.CredentialRequest, control.SecretRequest) error
}

func (f *fakeAPI) Health(context.Context) error { return nil }
func (f *fakeAPI) StartAuthorization(ctx context.Context, request control.AuthorizationRequest) (control.Authorization, error) {
	if f.startAuthorization == nil {
		return control.Authorization{}, errors.New("unexpected StartAuthorization")
	}
	return f.startAuthorization(ctx, request)
}
func (f *fakeAPI) PollAuthorization(ctx context.Context, id string) (control.Token, error) {
	if f.pollAuthorization == nil {
		return control.Token{}, errors.New("unexpected PollAuthorization")
	}
	return f.pollAuthorization(ctx, id)
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
func (f *fakeAPI) ListProjects(context.Context, control.CredentialRequest) ([]control.Project, error) {
	return nil, nil
}
func (f *fakeAPI) GetProject(ctx context.Context, credential control.CredentialRequest, project string) (control.Project, error) {
	if f.getProject == nil {
		return control.Project{}, errors.New("unexpected GetProject")
	}
	return f.getProject(ctx, credential, project)
}
func (f *fakeAPI) ListProviders(context.Context, control.CredentialRequest, string) ([]control.Provider, error) {
	return nil, nil
}
func (f *fakeAPI) SetProvider(context.Context, control.CredentialRequest, control.ProviderRequest) (control.Provider, error) {
	return control.Provider{}, nil
}
func (f *fakeAPI) ListSecrets(context.Context, control.CredentialRequest, string) ([]control.Secret, error) {
	return nil, nil
}
func (f *fakeAPI) SetSecret(ctx context.Context, credential control.CredentialRequest, request control.SecretRequest) error {
	if f.setSecret == nil {
		return errors.New("unexpected SetSecret")
	}
	return f.setSecret(ctx, credential, request)
}
func (f *fakeAPI) DeleteSecret(context.Context, control.CredentialRequest, string, string) error {
	return nil
}
