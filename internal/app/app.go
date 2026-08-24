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
	"golang.org/x/term"
)

const defaultControlURL = "https://control.relay.open-e2ee.dev"

var commands = []string{"init", "login", "dev", "deploy", "plan", "doctor", "project", "provider", "secret"}

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
	if dependencies.WorkingDir == "" {
		dependencies.WorkingDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(dependencies.Err, "error: determine working directory: %v\n", err)
			return 1
		}
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
		api: api, store: dependencies.Store, in: dependencies.In,
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
	case "provider":
		return r.provider(ctx, args)
	case "secret":
		return r.secret(ctx, args)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func (r *runner) commandHelp(command string) error {
	usage := map[string]string{
		"init":     "oe init [--directory PATH] [--name PROJECT] [--force]",
		"login":    "oe login [--timeout DURATION]",
		"dev":      "oe dev [--timeout DURATION] [--no-wait]",
		"plan":     "oe plan",
		"deploy":   "oe deploy [--confirm]",
		"doctor":   "oe doctor",
		"project":  "oe project <list|show|select PROJECT>",
		"provider": "oe provider <list|set --kind KIND [--issuer URL]>",
		"secret":   "oe secret <list|set NAME [--from-env VARIABLE]|delete NAME>",
		"version":  "oe version",
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
	authorization, err := r.api.StartAuthorization(ctx, control.AuthorizationRequest{Scopes: []string{
		"project:read", "project:write", "provider:write", "secret:write", "deploy:write",
	}})
	if err != nil {
		return credential.Credential{}, err
	}
	if authorization.ID == "" || authorization.VerificationURL == "" || authorization.UserCode == "" {
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
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		token, err := r.api.PollAuthorization(loginCtx, authorization.ID)
		if err != nil {
			return credential.Credential{}, err
		}
		if !token.Pending {
			if token.AccessToken == "" {
				return credential.Credential{}, errors.New("browser authorization completed without a credential")
			}
			value := credential.Credential{AccessToken: token.AccessToken, Scopes: token.Scopes, ExpiresAt: token.ExpiresAt}
			if err := r.store.Set(profile, value); err != nil {
				return credential.Credential{}, err
			}
			value.Source = "keychain"
			return value, nil
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
	bootstrap, err := r.api.BootstrapDevelopment(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.BootstrapRequest{
		ProjectSlug: value.Project, Writer: value.Writer,
	})
	if err != nil {
		return err
	}
	if bootstrap.Writer != "config" || bootstrap.ProjectSlug != value.Project || bootstrap.Environment != "development" {
		return errors.New("control API returned a bootstrap for a different project, writer, or environment")
	}
	if bootstrap.DevelopmentPublicKey == "" || bootstrap.ProductionPublicKey == "" {
		return errors.New("control API returned incomplete publishable configuration")
	}
	development := value.Environments["development"]
	development.PublishableKey = bootstrap.DevelopmentPublicKey
	value.Environments["development"] = development
	production := value.Environments["production"]
	production.PublishableKey = bootstrap.ProductionPublicKey
	value.Environments["production"] = production
	if err := config.Write(path, value); err != nil {
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
	plan, err := r.api.Plan(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, control.PlanRequest{
		ProjectSlug: value.Project, Environment: r.environment, Writer: value.Writer,
		ExpectedRevision: project.Revision, Config: value,
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
	lock, err := projectlock.Acquire(ctx, filepath.Dir(mustConfigPath(r.directory)))
	if err != nil {
		return err
	}
	defer lock.Release()
	plan, err := r.api.Plan(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, control.PlanRequest{
		ProjectSlug: value.Project, Environment: "production", Writer: value.Writer,
		ExpectedRevision: project.Revision, Config: value,
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
		PlanID: plan.ID, Writer: value.Writer, ExpectedRevision: plan.ExpectedRevision,
	})
	if err != nil {
		return err
	}
	return r.out.Success("deploy", "Production configuration deployed.", map[string]any{
		"deploymentId": deployment.ID, "revision": deployment.Revision, "status": deployment.Status,
	})
}

func (r *runner) doctor(ctx context.Context, args []string) error {
	flags := newFlags("doctor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	checks := map[string]any{"config": "ok", "control": "ok", "credential": "ok", "telemetry": "disabled"}
	if _, _, err := r.loadConfig(); err != nil {
		checks["config"] = err.Error()
	}
	if err := r.api.Health(ctx); err != nil {
		checks["control"] = err.Error()
	}
	if _, err := r.access(ctx, "project:read", false); err != nil {
		checks["credential"] = err.Error()
	}
	for _, value := range checks {
		if text, ok := value.(string); ok && text != "ok" && text != "disabled" {
			return fmt.Errorf("doctor found a problem: %s", text)
		}
	}
	return r.out.Success("doctor", "All checks passed. CLI telemetry is disabled.", checks)
}

func (r *runner) project(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oe project <list|show|select>")
	}
	access, err := r.access(ctx, "project:read", false)
	if err != nil {
		return err
	}
	request := control.CredentialRequest{AccessToken: access.AccessToken}
	switch args[0] {
	case "list":
		projects, err := r.api.ListProjects(ctx, request)
		if err != nil {
			return err
		}
		return r.out.Success("project", fmt.Sprintf("Found %d project(s).", len(projects)), map[string]any{"projects": projects})
	case "show":
		_, value, err := r.loadConfig()
		if err != nil {
			return err
		}
		project, err := r.api.GetProject(ctx, request, value.Project)
		if err != nil {
			return err
		}
		return r.out.Success("project", "Project loaded.", map[string]any{"project": project})
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
		development.PublishableKey = project.DevelopmentPublishableKey
		value.Environments["development"] = development
		production := value.Environments["production"]
		production.PublishableKey = project.ProductionPublishableKey
		value.Environments["production"] = production
		if err := config.Write(path, value); err != nil {
			return err
		}
		return r.out.Success("project", "Selected project "+project.Slug+".", map[string]any{"project": project.Slug})
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func (r *runner) provider(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oe provider <list|set>")
	}
	_, value, err := r.loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		access, err := r.access(ctx, "project:read", false)
		if err != nil {
			return err
		}
		providers, err := r.api.ListProviders(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
		if err != nil {
			return err
		}
		return r.out.Success("provider", fmt.Sprintf("Found %d provider(s).", len(providers)), map[string]any{"providers": providers})
	case "set":
		flags := newFlags("provider set")
		kind := flags.String("kind", "", "device-owned, clerk, firebase, oidc, or custom-token")
		issuer := flags.String("issuer", "", "provider issuer")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *kind == "" {
			return errors.New("--kind is required")
		}
		if err := validateProvider(*kind, *issuer); err != nil {
			return err
		}
		access, err := r.access(ctx, "provider:write", false)
		if err != nil {
			return err
		}
		operation, err := operationID()
		if err != nil {
			return err
		}
		provider, err := r.api.SetProvider(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.ProviderRequest{
			ProjectSlug: value.Project, Environment: r.environment, Kind: *kind, Issuer: *issuer,
		})
		if err != nil {
			return err
		}
		return r.out.Success("provider", "Identity provider updated.", map[string]any{"provider": provider})
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
}

func (r *runner) secret(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: oe secret <list|set|delete>")
	}
	_, value, err := r.loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		access, err := r.access(ctx, "project:read", false)
		if err != nil {
			return err
		}
		secrets, err := r.api.ListSecrets(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
		if err != nil {
			return err
		}
		return r.out.Success("secret", fmt.Sprintf("Found %d secret name(s). Values are never returned.", len(secrets)), map[string]any{"secrets": secrets})
	case "set":
		name, fromEnv, err := parseSecretSet(args[1:])
		if err != nil {
			return err
		}
		if name == "" {
			return errors.New("usage: oe secret set <name> [--from-env VARIABLE]")
		}
		if !validSecretName(name) {
			return errors.New("secret name must use uppercase letters, digits, and single underscores")
		}
		secretValue, err := r.readSecret(fromEnv)
		if err != nil {
			return err
		}
		access, err := r.access(ctx, "secret:write", false)
		if err != nil {
			return err
		}
		operation, err := operationID()
		if err != nil {
			return err
		}
		err = r.api.SetSecret(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, control.SecretRequest{
			ProjectSlug: value.Project, Environment: r.environment, Name: name, Value: secretValue,
		})
		if err != nil {
			return redactError(err, secretValue)
		}
		return r.out.Success("secret", "Secret stored. Its value was not written or printed.", map[string]any{"name": name, "environment": r.environment})
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: oe secret delete <name>")
		}
		if !validSecretName(args[1]) {
			return errors.New("secret name must use uppercase letters, digits, and single underscores")
		}
		access, err := r.access(ctx, "secret:write", false)
		if err != nil {
			return err
		}
		operation, err := operationID()
		if err != nil {
			return err
		}
		if err := r.api.DeleteSecret(ctx, control.CredentialRequest{AccessToken: access.AccessToken, OperationID: operation}, value.Project, args[1]); err != nil {
			return err
		}
		return r.out.Success("secret", "Secret deleted.", map[string]any{"name": args[1], "environment": r.environment})
	default:
		return fmt.Errorf("unknown secret command %q", args[0])
	}
}

func (r *runner) readSecret(fromEnvironment string) (string, error) {
	if fromEnvironment != "" {
		value := r.getenv(fromEnvironment)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is empty", fromEnvironment)
		}
		return value, nil
	}
	if file, ok := r.in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(r.errOut, "Secret value: ")
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(r.errOut)
		if err != nil {
			return "", err
		}
		if len(value) == 0 {
			return "", errors.New("secret value is empty")
		}
		return string(value), nil
	}
	value, err := io.ReadAll(io.LimitReader(r.in, 1<<20))
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimRight(string(value), "\r\n")
	if trimmed == "" {
		return "", errors.New("secret value is empty")
	}
	return trimmed, nil
}

func parseSecretSet(args []string) (string, string, error) {
	var name string
	var fromEnvironment string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--from-env":
			if index+1 >= len(args) {
				return "", "", errors.New("--from-env needs a value")
			}
			fromEnvironment = args[index+1]
			index++
		default:
			if strings.HasPrefix(args[index], "-") {
				return "", "", fmt.Errorf("unknown secret set flag %q", args[index])
			}
			if name != "" {
				return "", "", errors.New("secret set accepts one name")
			}
			name = args[index]
		}
	}
	return name, fromEnvironment, nil
}

func validateProvider(kind, issuer string) error {
	switch kind {
	case "device-owned":
		if issuer != "" {
			return errors.New("device-owned identity does not use --issuer")
		}
		return nil
	case "clerk", "firebase", "oidc", "custom-token":
		if issuer == "" {
			return fmt.Errorf("--issuer is required for %s", kind)
		}
		parsed, err := url.Parse(issuer)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("provider issuer must be an absolute HTTPS URL")
		}
		return nil
	default:
		return fmt.Errorf("unsupported provider kind %q", kind)
	}
}

func validSecretName(name string) bool {
	if name == "" || strings.HasPrefix(name, "_") || strings.HasSuffix(name, "_") || strings.Contains(name, "__") {
		return false
	}
	for _, character := range name {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
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
	if plan.ExpectedRevision != project.Revision {
		return errors.New("control API returned a plan for a stale project revision")
	}
	if plan.ProjectID != "" && project.ID != "" && plan.ProjectID != project.ID {
		return errors.New("control API returned a plan for a different project")
	}
	return nil
}

func redactError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
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
	if err := credential.RequireScope(value, scope); err != nil {
		return credential.Credential{}, err
	}
	return value, nil
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
	mode        output.Mode
	controlURL  string
	environment string
}

func parseGlobal(args []string) (globalOptions, string, []string, error) {
	options := globalOptions{mode: output.Text, controlURL: defaultControlURL, environment: "production"}
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
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "-") && args[0] != "--help" && args[0] != "--version" {
				return options, "", nil, fmt.Errorf("unknown global flag %q", args[0])
			}
			if options.environment != "development" && options.environment != "production" {
				return options, "", nil, errors.New("--environment must be development or production")
			}
			return options, args[0], args[1:], nil
		}
	}
	return options, "", nil, nil
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
