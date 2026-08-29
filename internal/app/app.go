package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/open-e2ee/cli/internal/config"
	"github.com/open-e2ee/cli/internal/control"
	"github.com/open-e2ee/cli/internal/credential"
	"github.com/open-e2ee/cli/internal/output"
	"github.com/open-e2ee/cli/internal/projectlock"
)

const defaultControlURL = "https://console.open-e2ee.dev/api/cli"

var commands = []string{"init", "login", "dev", "deploy", "plan", "doctor", "project"}

type Dependencies struct {
	API        control.API
	Store      credential.Store
	HTTP       *http.Client
	In         io.Reader
	Out        io.Writer
	Err        io.Writer
	OpenURL    func(string) error
	Sleep      func(context.Context, time.Duration) error
	Now        func() time.Time
	Getenv     func(string) string
	WorkingDir string
}

type runner struct {
	api         control.API
	http        *http.Client
	store       credential.Store
	in          io.Reader
	out         *output.Writer
	errOut      io.Writer
	openURL     func(string) error
	sleep       func(context.Context, time.Duration) error
	now         func() time.Time
	getenv      func(string) string
	directory   string
	controlURL  string
	environment string
	mode        output.Mode
}

func Run(ctx context.Context, args []string, dependencies Dependencies) int {
	global, command, commandArgs, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintf(defaultWriter(dependencies.Err, os.Stderr), "error: %v\n", err)
		return 2
	}
	if command == "" {
		command = "help"
	}
	if dependencies.Out == nil {
		dependencies.Out = os.Stdout
	}
	if dependencies.Err == nil {
		dependencies.Err = os.Stderr
	}
	if dependencies.In == nil {
		dependencies.In = os.Stdin
	}
	if dependencies.Store == nil {
		dependencies.Store = credential.Keychain{}
	}
	if dependencies.OpenURL == nil {
		dependencies.OpenURL = openBrowser
	}
	if dependencies.Sleep == nil {
		dependencies.Sleep = sleepContext
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Getenv == nil {
		dependencies.Getenv = os.Getenv
	}
	if dependencies.HTTP == nil {
		dependencies.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if dependencies.WorkingDir == "" {
		dependencies.WorkingDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(dependencies.Err, "error: determine working directory: %v\n", err)
			return 1
		}
	}
	if !global.environmentExplicit {
		global.environment = defaultEnvironment(command, dependencies.WorkingDir)
	}
	if err := validateControlURL(global.controlURL); err != nil {
		fmt.Fprintf(dependencies.Err, "error: %v\n", err)
		return 2
	}
	api := dependencies.API
	if api == nil {
		api, err = control.New(global.controlURL, dependencies.HTTP)
		if err != nil {
			fmt.Fprintf(dependencies.Err, "error: %v\n", err)
			return 2
		}
	}
	r := &runner{
		api: api, http: dependencies.HTTP, store: dependencies.Store, in: dependencies.In,
		out: output.New(global.mode, dependencies.Out), errOut: dependencies.Err,
		openURL: dependencies.OpenURL, sleep: dependencies.Sleep, now: dependencies.Now,
		getenv: dependencies.Getenv, directory: dependencies.WorkingDir,
		controlURL: global.controlURL, environment: global.environment, mode: global.mode,
	}
	if err := r.execute(ctx, command, commandArgs); err != nil {
		_ = r.out.Failure(command, err)
		return 1
	}
	return 0
}

func (r *runner) execute(ctx context.Context, command string, args []string) error {
	if command != "help" && containsHelp(args) {
		return r.commandHelp(command)
	}
	switch command {
	case "help", "--help", "-h":
		return r.help()
	case "version", "--version":
		return r.out.Success("version", Version, map[string]any{"version": Version})
	case "init":
		return r.init(args)
	case "login":
		return r.login(ctx, args)
	case "dev":
		return r.dev(ctx, args)
	case "plan":
		return r.plan(ctx, args)
	case "deploy":
		return r.deploy(ctx, args)
	case "doctor":
		return r.doctor(ctx, args)
	case "project":
		return r.project(ctx, args)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func (r *runner) commandHelp(command string) error {
	usage := map[string]string{
		"init":    "oe init [--directory PATH] [--name PROJECT] [--force]",
		"login":   "oe login [--timeout DURATION]",
		"dev":     "oe dev [--timeout DURATION] [--no-wait]",
		"plan":    "oe plan",
		"deploy":  "oe deploy [--confirm]",
		"doctor":  "oe doctor",
		"project": "oe project <show|select PROJECT>",
		"version": "oe version",
	}
	text, ok := usage[command]
	if !ok {
		return fmt.Errorf("unknown command %q", command)
	}
	return r.out.Success(command, text, map[string]any{"usage": text})
}

func (r *runner) help() error {
	return r.out.Success("help", "Use oe <command> --help for command details.", map[string]any{"commands": commands})
}

func (r *runner) init(args []string) error {
	flags := newFlags("init")
	directory := flags.String("directory", r.directory, "project directory")
	name := flags.String("name", "", "project slug")
	force := flags.Bool("force", false, "replace an existing config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	project := *name
	if project == "" {
		project = slug(filepath.Base(*directory))
	}
	path := filepath.Join(*directory, config.Filename)
	if err := os.MkdirAll(*directory, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists; use --force to replace it", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := projectlock.Acquire(context.Background(), *directory)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := config.Write(path, config.New(project)); err != nil {
		return err
	}
	quickstart := filepath.Join(*directory, "open-e2ee-local.mjs")
	if _, err := os.Stat(quickstart); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(quickstart, []byte(localRoundtrip), 0o644); err != nil {
			return err
		}
	}
	return r.out.Success("init", "Initialized local OpenE2EE files. No login or credential was used.", map[string]any{
		"config": path, "project": project, "quickstart": quickstart,
	})
}

func (r *runner) login(ctx context.Context, args []string) error {
	flags := newFlags("login")
	timeout := flags.Duration("timeout", 5*time.Minute, "login timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if r.getenv("OE_ACCESS_TOKEN") != "" {
		return r.out.Success("login", "Using the scoped CI credential from OE_ACCESS_TOKEN. It was not stored.", map[string]any{"source": "environment"})
	}
	value, err := r.interactiveLogin(ctx, *timeout)
	if err != nil {
		return err
	}
	return r.out.Success("login", "Login complete. The credential is in the OS keychain.", map[string]any{"source": value.Source})
}

func (r *runner) interactiveLogin(ctx context.Context, timeout time.Duration) (credential.Credential, error) {
	profile, err := credential.Profile(r.controlURL)
	if err != nil {
		return credential.Credential{}, err
	}
	authorization, err := r.api.StartAuthorization(ctx, control.AuthorizationRequest{})
	if err != nil {
		return credential.Credential{}, err
	}
	if authorization.DeviceCode == "" || authorization.VerificationURL == "" || authorization.UserCode == "" {
		return credential.Credential{}, errors.New("control API returned an incomplete browser authorization")
	}
	_ = r.out.Progress("login", fmt.Sprintf("Open %s and enter code %s.", authorization.VerificationURL, authorization.UserCode), map[string]any{
		"verificationUrl": authorization.VerificationURL, "userCode": authorization.UserCode,
	})
	if err := r.openURL(authorization.VerificationURL); err != nil {
		fmt.Fprintf(r.errOut, "Open %s and enter code %s.\n", authorization.VerificationURL, authorization.UserCode)
	}
	interval := time.Duration(authorization.IntervalSeconds) * time.Second
	if interval < time.Second {
		interval = 2 * time.Second
	}
	authorizationLifetime := time.Duration(authorization.ExpiresInSeconds) * time.Second
	if authorizationLifetime > 0 && authorizationLifetime < timeout {
		timeout = authorizationLifetime
	}
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		token, err := r.api.PollAuthorization(loginCtx, authorization)
		if err != nil {
			return credential.Credential{}, err
		}
		if !token.Pending {
			if token.AccessToken == "" {
				return credential.Credential{}, errors.New("browser authorization completed without a credential")
			}
			value := credential.Credential{
				AccessToken: token.AccessToken, ExpiresAt: token.ExpiresAt,
				RefreshToken: token.RefreshToken,
			}
			if err := r.store.Set(profile, value); err != nil {
				return credential.Credential{}, err
			}
			value.Source = "keychain"
			return value, nil
		}
		if token.RetryAfterSeconds > 0 {
			interval = time.Duration(token.RetryAfterSeconds) * time.Second
		}
		if err := r.sleep(loginCtx, interval); err != nil {
			return credential.Credential{}, fmt.Errorf("login did not complete: %w", err)
		}
	}
}

func (r *runner) dev(ctx context.Context, args []string) error {
	flags := newFlags("dev")
	timeout := flags.Duration("timeout", 30*time.Minute, "first acknowledgement timeout")
	noWait := flags.Bool("no-wait", false, "do not wait for the first acknowledgement")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path, value, err := r.loadConfig()
	if err != nil {
		return err
	}
	if value.Writer != "config" {
		return errors.New("this project is console-first; change writer mode before oe dev can write policy")
	}
	lock, err := projectlock.Acquire(ctx, filepath.Dir(path))
	if err != nil {
		return err
	}
	defer lock.Release()
	access, err := r.access(ctx, "project:write", true)
	if err != nil {
		return err
	}
	operation, err := operationID()
	if err != nil {
		return err
	}
	policy, err := controlPolicy(value, "development")
	if err != nil {
		return err
	}
	bootstrap, err := r.api.BootstrapDevelopment(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.BootstrapRequest{
		Policy: policy, ProjectSlug: value.Project, Writer: value.Writer,
	})
	if err != nil {
		return err
	}
	if bootstrap.Writer != "config" || bootstrap.ProjectSlug != value.Project || bootstrap.Environment != "development" {
		return errors.New("control API returned a bootstrap for a different project, writer, or environment")
	}
	if bootstrap.DevelopmentRelayURL == "" {
		return errors.New("control API returned an incomplete development Relay connection")
	}
	development := value.Environments["development"]
	development.RelayURL = bootstrap.DevelopmentRelayURL
	value.Environments["development"] = development
	value.SelectedEnvironment = "development"
	if err := config.Write(path, value); err != nil {
		return err
	}
	if err := writeRelayEnvironment(filepath.Dir(path), ".env.local", bootstrap.DevelopmentRelayURL); err != nil {
		return err
	}
	_ = r.out.Progress("dev", "Managed development is ready. Connect the first device.", map[string]any{"environment": "development"})
	if *noWait {
		return r.out.Success("dev", "Managed development is ready. First-acknowledgement waiting was skipped.", map[string]any{"environment": "development", "waiting": false})
	}
	waitCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	for {
		state, err := r.api.Activation(waitCtx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
		if err != nil {
			return err
		}
		if state.FirstDevice && state.FirstAcknowledged {
			return r.out.Success("dev", "The first managed message was acknowledged.", map[string]any{
				"environment": "development", "firstDevice": true, "firstAcknowledgedMessage": true,
			})
		}
		if err := r.sleep(waitCtx, 2*time.Second); err != nil {
			return fmt.Errorf("wait for first acknowledged managed message: %w", err)
		}
	}
}

func (r *runner) plan(ctx context.Context, args []string) error {
	flags := newFlags("plan")
	if err := flags.Parse(args); err != nil {
		return err
	}
	value, project, access, err := r.deployContext(ctx, "project:read")
	if err != nil {
		return err
	}
	policy, err := controlPolicy(value, r.environment)
	if err != nil {
		return err
	}
	operation, err := operationID()
	if err != nil {
		return err
	}
	plan, err := r.api.Plan(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.PlanRequest{
		Environment: r.environment, Policy: policy, ProjectSlug: value.Project, Writer: value.Writer,
	})
	if err != nil {
		return err
	}
	if err := validatePlan(plan, project, r.environment); err != nil {
		return err
	}
	return r.out.Success("plan", fmt.Sprintf("Plan %s has %d change(s).", plan.ID, len(plan.Changes)), map[string]any{
		"planId": plan.ID, "environment": plan.Environment, "changes": plan.Changes,
		"billingReady": plan.BillingReady,
	})
}

func (r *runner) deploy(ctx context.Context, args []string) error {
	flags := newFlags("deploy")
	confirm := flags.Bool("confirm", false, "confirm the production deploy")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if r.environment != "production" {
		return errors.New("oe deploy targets production; use oe dev for development")
	}
	value, project, access, err := r.deployContext(ctx, "deploy:write")
	if err != nil {
		return err
	}
	configPath := mustConfigPath(r.directory)
	lock, err := projectlock.Acquire(ctx, filepath.Dir(configPath))
	if err != nil {
		return err
	}
	defer lock.Release()
	policy, err := controlPolicy(value, "production")
	if err != nil {
		return err
	}
	planOperation, err := operationID()
	if err != nil {
		return err
	}
	plan, err := r.api.Plan(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: planOperation}, control.PlanRequest{
		Environment: "production", Policy: policy, ProjectSlug: value.Project, Writer: value.Writer,
	})
	if err != nil {
		return err
	}
	if err := validatePlan(plan, project, "production"); err != nil {
		return err
	}
	if !plan.BillingReady {
		if access.Source == "environment" {
			return errors.New("production billing setup is incomplete; finish it in the console before a CI deploy")
		}
		if plan.BillingSetupURL == "" {
			return errors.New("production billing setup is incomplete and no setup URL was returned")
		}
		if err := r.openURL(plan.BillingSetupURL); err != nil {
			return fmt.Errorf("open production card setup: %w", err)
		}
		return errors.New("finish production card setup, then run oe deploy again")
	}
	if !*confirm {
		if access.Source == "environment" || r.mode != output.Text {
			return errors.New("production deploy requires --confirm in CI and JSON modes")
		}
		approved, err := askConfirmation(r.in, r.errOut, fmt.Sprintf("Deploy %d production change(s)?", len(plan.Changes)))
		if err != nil {
			return err
		}
		if !approved {
			return errors.New("production deploy cancelled")
		}
	}
	operation, err := operationID()
	if err != nil {
		return err
	}
	deployment, err := r.api.Deploy(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.DeployRequest{
		ExpectedRevision: plan.ExpectedRevision, PlanID: plan.ID, Policy: policy,
		ProjectSlug: value.Project, Writer: value.Writer,
	})
	if err != nil {
		return err
	}
	if deployment.RelayURL == "" {
		return errors.New("control API returned an incomplete production Relay connection")
	}
	production := value.Environments["production"]
	production.RelayURL = deployment.RelayURL
	value.Environments["production"] = production
	value.SelectedEnvironment = "production"
	if err := config.Write(configPath, value); err != nil {
		return err
	}
	if err := writeRelayEnvironment(filepath.Dir(configPath), ".env.production.local", deployment.RelayURL); err != nil {
		return err
	}
	return r.out.Success("deploy", "Production Relay is active. Install OPEN_E2EE_RELAY_URL from .env.production.local in the hosting environment.", map[string]any{
		"configurationFile": ".env.production.local", "deploymentId": deployment.ID,
		"revision": deployment.Revision, "status": deployment.Status, "variable": "OPEN_E2EE_RELAY_URL",
	})
}

func (r *runner) doctor(ctx context.Context, args []string) error {
	flags := newFlags("doctor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	checks := map[string]any{"config": "ok", "control": "ok", "credential": "ok", "relay": "ok", "telemetry": "disabled"}
	_, value, err := r.loadConfig()
	if err != nil {
		return fmt.Errorf("doctor found a problem: %w", err)
	}
	local := value.Environments[r.environment].RelayURL
	if local == "" {
		command := "oe dev"
		if r.environment == "production" {
			command = "oe deploy"
		}
		return fmt.Errorf("doctor found a problem: %s Relay connection is not configured; run %s", r.environment, command)
	}
	if err := r.api.Health(ctx); err != nil {
		return fmt.Errorf("doctor found a problem: control API: %w", err)
	}
	access, err := r.access(ctx, "project:read", false)
	if err != nil {
		return fmt.Errorf("doctor found a problem: credential: %w", err)
	}
	project, err := r.api.GetProject(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
	if err != nil {
		return fmt.Errorf("doctor found a problem: project authority: %w", err)
	}
	expected := ""
	if project.Development != nil {
		expected = project.Development.RelayURL
	}
	if r.environment == "production" {
		expected = ""
		if project.Production != nil {
			expected = project.Production.RelayURL
		}
	}
	if expected == "" {
		return fmt.Errorf("doctor found a problem: %s is not active", r.environment)
	}
	if expected != local {
		return fmt.Errorf("doctor found a problem: local %s Relay connection is stale or belongs to another project; select the project again", r.environment)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, local, nil)
	if err != nil {
		return fmt.Errorf("doctor found a problem: Relay connection is invalid")
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.http.Do(request)
	if err != nil {
		return fmt.Errorf("doctor found a problem: Relay connection is unreachable")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("doctor found a problem: Relay connection returned status %d", response.StatusCode)
	}
	origin, _ := url.Parse(local)
	checks["project"] = value.Project
	checks["environment"] = r.environment
	checks["configurationSource"] = config.Filename
	checks["relayOrigin"] = origin.Scheme + "://" + origin.Host
	return r.out.Success("doctor", "All checks passed. CLI telemetry is disabled.", checks)
}

func (r *runner) project(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oe project <show|select>")
	}
	access, err := r.access(ctx, "project:read", false)
	if err != nil {
		return err
	}
	request := control.CredentialRequest{AccessToken: access.AccessToken}
	switch args[0] {
	case "show":
		_, value, err := r.loadConfig()
		if err != nil {
			return err
		}
		project, err := r.api.GetProject(ctx, request, value.Project)
		if err != nil {
			return err
		}
		return r.out.Success("project", "Project loaded.", map[string]any{"project": projectSummary(project)})
	case "select":
		if len(args) != 2 {
			return errors.New("usage: oe project select <project>")
		}
		project, err := r.api.GetProject(ctx, request, args[1])
		if err != nil {
			return err
		}
		path, value, err := r.loadConfig()
		if err != nil {
			return err
		}
		lock, err := projectlock.Acquire(ctx, filepath.Dir(path))
		if err != nil {
			return err
		}
		defer lock.Release()
		value.Project = project.Slug
		value.Writer = project.Writer
		development := value.Environments["development"]
		if project.Development != nil {
			development.RelayURL = project.Development.RelayURL
		} else {
			development.RelayURL = ""
		}
		value.Environments["development"] = development
		production := value.Environments["production"]
		if project.Production != nil {
			production.RelayURL = project.Production.RelayURL
		} else {
			production.RelayURL = ""
		}
		value.Environments["production"] = production
		if err := config.Write(path, value); err != nil {
			return err
		}
		if err := writeRelayEnvironment(filepath.Dir(path), ".env.local", development.RelayURL); err != nil {
			return err
		}
		if err := writeRelayEnvironment(filepath.Dir(path), ".env.production.local", production.RelayURL); err != nil {
			return err
		}
		return r.out.Success("project", "Selected project "+project.Slug+".", map[string]any{"project": project.Slug})
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func projectSummary(project control.Project) map[string]string {
	return map[string]string{
		"slug":   project.Slug,
		"writer": project.Writer,
	}
}

func (r *runner) deployContext(ctx context.Context, scope string) (config.Config, control.Project, credential.Credential, error) {
	_, value, err := r.loadConfig()
	if err != nil {
		return config.Config{}, control.Project{}, credential.Credential{}, err
	}
	if value.Writer != "config" {
		return config.Config{}, control.Project{}, credential.Credential{}, errors.New("this project is console-first; repository deploys are disabled")
	}
	access, err := r.access(ctx, scope, false)
	if err != nil {
		return config.Config{}, control.Project{}, credential.Credential{}, err
	}
	project, err := r.api.GetProject(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
	if err != nil {
		return config.Config{}, control.Project{}, credential.Credential{}, err
	}
	if project.Writer != value.Writer {
		return config.Config{}, control.Project{}, credential.Credential{}, errors.New("writer mode drift: server and repository disagree")
	}
	return value, project, access, nil
}

func validatePlan(plan control.Plan, project control.Project, environment string) error {
	if plan.ID == "" {
		return errors.New("control API returned a plan without an ID")
	}
	if plan.Environment != environment {
		return errors.New("control API returned a plan for a different environment")
	}
	expectedRevision := "0"
	if environment == "development" && project.Development != nil {
		expectedRevision = project.Development.Revision
	}
	if environment == "production" && project.Production != nil {
		expectedRevision = project.Production.Revision
	}
	if plan.ExpectedRevision != expectedRevision {
		return errors.New("control API returned a plan for a stale project revision")
	}
	if plan.ProjectSlug != project.Slug {
		return errors.New("control API returned a plan for a different project")
	}
	return nil
}

func controlPolicy(value config.Config, environment string) (control.RelayPolicyRequest, error) {
	policy, err := value.RelayPolicyFor(environment)
	if err != nil {
		return control.RelayPolicyRequest{}, err
	}
	attachment, err := config.RetentionSeconds(policy.AttachmentRetention)
	if err != nil {
		return control.RelayPolicyRequest{}, err
	}
	delivery, err := config.RetentionSeconds(policy.DeliveryRetention)
	if err != nil {
		return control.RelayPolicyRequest{}, err
	}
	return control.RelayPolicyRequest{
		AttachmentRetentionSeconds: attachment,
		DeliveryTtlSeconds:         delivery,
	}, nil
}

func (r *runner) access(ctx context.Context, scope string, loginWhenMissing bool) (credential.Credential, error) {
	profile, err := credential.Profile(r.controlURL)
	if err != nil {
		return credential.Credential{}, err
	}
	value, err := credential.Resolve(r.store, profile)
	if errors.Is(err, credential.ErrNotFound) && loginWhenMissing {
		value, err = r.interactiveLogin(ctx, 5*time.Minute)
	}
	if err != nil {
		return credential.Credential{}, fmt.Errorf("login required: run oe login: %w", err)
	}
	if value.Source != "environment" && value.RefreshToken != "" && credentialNeedsRefresh(value, r.now()) {
		refreshed, refreshErr := r.api.RefreshAuthorization(ctx, value.RefreshToken)
		if refreshErr != nil {
			if errors.Is(refreshErr, control.ErrSessionExpired) {
				_ = r.store.Delete(profile)
				return credential.Credential{}, errors.New("the WorkOS session expired; run oe login again")
			}
			if !credentialIsCurrentlyValid(value, r.now()) {
				return credential.Credential{}, fmt.Errorf("refresh the WorkOS session: %w", refreshErr)
			}
		} else {
			value = credential.Credential{
				AccessToken: refreshed.AccessToken, ExpiresAt: refreshed.ExpiresAt,
				RefreshToken: refreshed.RefreshToken, Source: "keychain",
			}
			if err := r.store.Set(profile, value); err != nil {
				return credential.Credential{}, err
			}
		}
	}
	if err := credential.RequireScope(value, scope); err != nil {
		return credential.Credential{}, err
	}
	return value, nil
}

func credentialNeedsRefresh(value credential.Credential, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, value.ExpiresAt)
	return err != nil || !expiresAt.After(now.Add(time.Minute))
}

func credentialIsCurrentlyValid(value credential.Credential, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, value.ExpiresAt)
	return err == nil && expiresAt.After(now)
}

func (r *runner) loadConfig() (string, config.Config, error) {
	path, err := config.Find(r.directory)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("run oe init first: %w", err)
	}
	value, err := config.Load(path)
	return path, value, err
}

type globalOptions struct {
	mode                output.Mode
	controlURL          string
	environment         string
	environmentExplicit bool
}

func parseGlobal(args []string) (globalOptions, string, []string, error) {
	options := globalOptions{mode: output.Text, controlURL: defaultControlURL}
	for len(args) > 0 {
		switch args[0] {
		case "--json":
			options.mode = output.JSON
			args = args[1:]
		case "--json-stream":
			options.mode = output.JSONStream
			args = args[1:]
		case "--control-url":
			if len(args) < 2 {
				return options, "", nil, errors.New("--control-url needs a value")
			}
			options.controlURL = args[1]
			args = args[2:]
		case "--environment":
			if len(args) < 2 {
				return options, "", nil, errors.New("--environment needs a value")
			}
			options.environment = args[1]
			options.environmentExplicit = true
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") && args[0] != "--help" && args[0] != "--version" {
				return options, "", nil, fmt.Errorf("unknown global flag %q", args[0])
			}
			if options.environmentExplicit && options.environment != "development" && options.environment != "production" {
				return options, "", nil, errors.New("--environment must be development or production")
			}
			return options, args[0], args[1:], nil
		}
	}
	return options, "", nil, nil
}

func defaultEnvironment(command, directory string) string {
	switch command {
	case "plan", "deploy":
		return "production"
	case "dev":
		return "development"
	}
	path, err := config.Find(directory)
	if err == nil {
		value, loadErr := config.Load(path)
		if loadErr == nil {
			return value.SelectedEnvironment
		}
	}
	return "development"
}

func containsHelp(args []string) bool {
	for _, argument := range args {
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return false
}

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func validateControlURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid control URL %q", raw)
	}
	hostname := parsed.Hostname()
	if parsed.Scheme != "https" && hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		return errors.New("the control URL must use HTTPS except on loopback")
	}
	return nil
}

func operationID() (string, error) {
	if supplied := strings.TrimSpace(os.Getenv("OE_OPERATION_ID")); supplied != "" {
		return supplied, nil
	}
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "oe_" + hex.EncodeToString(value), nil
}

func askConfirmation(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func slug(value string) string {
	value = strings.ToLower(value)
	var result strings.Builder
	lastHyphen := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			result.WriteRune(character)
			lastHyphen = false
		} else if !lastHyphen && result.Len() > 0 {
			result.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func writeRelayEnvironment(directory, filename, relayURL string) error {
	const variable = "OPEN_E2EE_RELAY_URL"
	const configuredComment = "# Public Managed Relay connection. This is not a credential."
	const unconfiguredComment = "# Managed Relay connection is not configured for this environment."
	path := filepath.Join(directory, filename)
	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(contents), "\r\n"), "\n")
	output := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		if line == "" && len(output) == 0 {
			continue
		}
		if strings.HasPrefix(line, variable+"=") || line == configuredComment || line == unconfiguredComment {
			continue
		}
		output = append(output, line)
	}
	if relayURL == "" {
		output = append(output, unconfiguredComment)
	} else {
		output = append(output, configuredComment, variable+"="+relayURL)
	}
	if err := writePublicFile(path, []byte(strings.Join(output, "\n")+"\n")); err != nil {
		return err
	}
	ignorePath := filepath.Join(directory, ".gitignore")
	ignored, err := os.ReadFile(ignorePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(ignored), "\n") {
		if strings.TrimSpace(line) == filename {
			return nil
		}
	}
	prefix := string(ignored)
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return writePublicFile(ignorePath, []byte(prefix+filename+"\n"))
}

func writePublicFile(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".open-e2ee-environment-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func mustConfigPath(start string) string {
	path, err := config.Find(start)
	if err != nil {
		return filepath.Join(start, config.Filename)
	}
	return path
}

func defaultWriter(candidate, fallback io.Writer) io.Writer {
	if candidate != nil {
		return candidate
	}
	return fallback
}

var Version = "0.0.0-development"

const localRoundtrip = `// Real protocol and cryptography; simulated in-memory infrastructure.
import { createSignalProtocolClient } from "@open-e2ee/signal-protocol-sdk";
import { inMemoryStore } from "@open-e2ee/signal-protocol-sdk/local/store/memory";
import { inMemoryRelay } from "@open-e2ee/signal-protocol-sdk/remote/relay/memory";

const relay = inMemoryRelay();
await relay.registerDevice("alice", { encryptedDeviceName: new ArrayBuffer(0) });
await relay.registerDevice("bob", { encryptedDeviceName: new ArrayBuffer(0) });
const alice = await createSignalProtocolClient({ identity: { userId: "alice" }, adapters: { storage: inMemoryStore(), relay } });
const bob = await createSignalProtocolClient({ identity: { userId: "bob" }, adapters: { storage: inMemoryStore(), relay } });
const delivered = new Promise((resolve) => {
  bob.registerHook("onMessageDecrypted", async (message) => {
    console.log(message.senderId + ": " + message.content);
    bob.stopRelaySubscription();
    resolve();
  });
});
await alice.send("bob", "hello");
bob.startRelaySubscription();
await delivered;
`
