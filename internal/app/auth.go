package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/credential"
	"github.com/open-e2ee/oe/internal/output"
)

const acceptTermsCommand = "oe auth login --accept-terms"

func (r *runner) auth(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("auth", "name an auth command: login, status, or logout")
	}
	switch args[0] {
	case "login":
		return r.authLogin(ctx, args[1:])
	case "status":
		return r.authStatus(ctx, args[1:])
	case "logout":
		return r.authLogout(args[1:])
	default:
		return usageError("auth", fmt.Sprintf("unknown auth command %q", args[0]))
	}
}

// envelopeCommand is the command that the output of a run names. An auth or
// config command names its verb, such as "auth login".
func envelopeCommand(command string, args []string) string {
	if command == "auth" && len(args) > 0 {
		switch args[0] {
		case "login", "status", "logout":
			return "auth " + args[0]
		}
	}
	if command == "config" && len(args) > 0 && args[0] == "pull" {
		return "config pull"
	}
	return command
}

// authLogin logs in with the device flow, then runs the terms step. With
// --accept-terms and a valid session, or with OE_ACCESS_TOKEN, it starts no
// device flow. A terms state that stays required is not a failure: the login
// succeeded, and next names the acceptance when the caller may give it.
func (r *runner) authLogin(ctx context.Context, args []string) error {
	flags := newFlags("auth login")
	timeout := flags.Duration("timeout", 5*time.Minute, "login timeout")
	acceptTerms := flags.Bool("accept-terms", false, "accept the OpenE2EE terms for the organization")
	if err := parseFlags(flags, "auth", args); err != nil {
		return err
	}
	session, loggedIn, err := r.loginSession(ctx, *timeout, *acceptTerms)
	if err != nil {
		return err
	}
	request := control.CredentialRequest{AccessToken: session.AccessToken}
	terms, err := r.api.Terms(ctx, request)
	if err != nil {
		return err
	}
	claims := sessionClaimsOf(session.AccessToken)
	data := claims.data()
	data["source"] = session.Source
	accepting := *acceptTerms && terms.State == control.TermsRequired
	if !*acceptTerms && terms.State == control.TermsRequired && terms.CanAccept && r.canPrompt() {
		accepting, err = r.askTerms(claims.organizationName(), terms.Documents)
		if err != nil {
			return err
		}
	}
	changed := false
	if accepting {
		accepted, err := r.api.AcceptTerms(ctx, request, r.termsActor(*acceptTerms))
		if err != nil {
			return err
		}
		terms, changed = accepted.Terms, accepted.Changed
	}
	if *acceptTerms || accepting {
		data["changed"] = changed
	}
	data["terms"] = terms.State
	data["canAccept"] = terms.CanAccept

	var message []string
	switch {
	case loggedIn:
		message = append(message, "Logged in to "+claims.organizationName()+".")
	case session.Source == "environment":
		message = append(message, "Using the scoped CI credential from OE_ACCESS_TOKEN. It was not stored.")
	}
	subject := claims.organizationSubject()
	next := ""
	switch {
	case terms.State == control.TermsAccepted && changed:
		message = append(message, subject+" accepted the OpenE2EE terms.")
	case terms.State == control.TermsAccepted:
		message = append(message, subject+" has accepted the OpenE2EE terms.")
	case terms.CanAccept:
		message = append(message, subject+" has not accepted the OpenE2EE terms.")
		data["documents"] = termsDocuments(terms.Documents)
		next = acceptTermsCommand
	default:
		message = append(message, subject+" has not accepted the OpenE2EE terms. An administrator of "+
			claims.organizationName()+" must accept them, in the console or with "+acceptTermsCommand+".")
		data["documents"] = termsDocuments(terms.Documents)
	}
	text := strings.Join(message, " ")
	if r.out.Mode() == output.Text && terms.State == control.TermsRequired {
		text += documentList(terms.Documents)
	}
	return r.out.SuccessNext("auth login", text, next, data)
}

// loginSession returns the session for the terms step, and reports whether
// this run logged in. OE_ACCESS_TOKEN is never replaced by a login. With
// acceptTerms, a stored session that is still valid is used as it is.
func (r *runner) loginSession(ctx context.Context, timeout time.Duration, acceptTerms bool) (credential.Credential, bool, error) {
	if acceptTerms || r.getenv("OE_ACCESS_TOKEN") != "" {
		value, err := r.access(ctx, "", false)
		if err == nil {
			return value, false, nil
		}
		if failure, ok := errors.AsType[*problem](err); !ok || failure.exit != exitAuthentication {
			return credential.Credential{}, false, err
		}
	}
	value, err := r.interactiveLogin(ctx, timeout, r.announcePending)
	return value, err == nil, err
}

// announcePending shows the device flow of oe auth login as a pending event.
// Under an agent it is the first of two JSON documents, so the agent can hand
// the URL and the code to the person while the CLI polls.
func (r *runner) announcePending(authorization control.Authorization) {
	message := "A person must approve this device."
	if r.out.Mode() == output.Text {
		message = loginPrompt(authorization)
	}
	_ = r.out.Pending("auth login", message, output.Action{
		Kind: "browser", URL: authorization.VerificationURL, Reason: "login",
	}, map[string]any{
		"userCode": authorization.UserCode, "expiresInSeconds": authorization.ExpiresInSeconds,
	})
}

// askTerms is the one terms prompt for a person at a terminal. It lists each
// document with its URL.
func (r *runner) askTerms(organization string, documents []control.TermsDocument) (bool, error) {
	fmt.Fprint(r.errOut, "The OpenE2EE terms:"+documentList(documents)+"\n")
	return askConfirmation(r.in, r.errOut, "Accept these terms for "+organization+"?")
}

// termsActor is the acceptance request. --accept-terms under an agent records
// the agent and the name that AD5 detection gives; a prompt answer is always
// a person.
func (r *runner) termsActor(flag bool) control.TermsAcceptanceRequest {
	if flag && r.underAgent {
		return control.TermsAcceptanceRequest{Actor: "agent", AgentName: r.harness.ID}
	}
	return control.TermsAcceptanceRequest{Actor: "person"}
}

// authStatus reads the session and the terms state of its organization. The
// terms read also checks the session with the control API.
func (r *runner) authStatus(ctx context.Context, args []string) error {
	if err := parseFlags(newFlags("auth status"), "auth", args); err != nil {
		return err
	}
	session, err := r.access(ctx, "", false)
	if err != nil {
		return err
	}
	terms, err := r.api.Terms(ctx, control.CredentialRequest{AccessToken: session.AccessToken})
	if err != nil {
		return err
	}
	claims := sessionClaimsOf(session.AccessToken)
	data := claims.data()
	data["terms"] = terms.State
	data["canAccept"] = terms.CanAccept
	data["source"] = session.Source

	message := "Logged in"
	if claims.User != "" {
		message += " as " + claims.User
	}
	message += " to " + claims.organizationName()
	if claims.Role != "" {
		message += " with the role " + claims.Role
	}
	message += "."
	if terms.State == control.TermsAccepted {
		message += " " + claims.organizationSubject() + " has accepted the OpenE2EE terms."
	} else {
		message += " " + claims.organizationSubject() + " has not accepted the OpenE2EE terms."
	}
	switch session.Source {
	case "environment":
		message += " The credential is OE_ACCESS_TOKEN."
	case "keychain":
		message += " The session is in the OS keychain."
	}
	return r.out.Success("auth status", message, data)
}

func (r *runner) authLogout(args []string) error {
	if err := parseFlags(newFlags("auth logout"), "auth", args); err != nil {
		return err
	}
	profile, err := credential.Profile(r.controlURL)
	if err != nil {
		return err
	}
	if err := r.store.Delete(profile); err != nil {
		return err
	}
	message := "Logged out. The OS keychain holds no session for " + profile + "."
	environment := r.getenv("OE_ACCESS_TOKEN") != ""
	if environment {
		message += " OE_ACCESS_TOKEN is still set, so later commands still use it."
	}
	return r.out.Success("auth logout", message, map[string]any{"profile": profile, "environmentCredential": environment})
}

// sessionClaims are the claims of a WorkOS access token that name a session.
// The control API verifies the token; the CLI decodes the claims only to show
// them. A token that is not a JWT has none.
type sessionClaims struct {
	User         string `json:"sub"`
	Organization string `json:"org_id"`
	Role         string `json:"role"`
}

func sessionClaimsOf(token string) sessionClaims {
	var claims sessionClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return sessionClaims{}
	}
	return claims
}

// data is the session part of a result. Only a claim that the token holds
// appears.
func (c sessionClaims) data() map[string]any {
	data := map[string]any{}
	if c.Organization != "" {
		data["organization"] = map[string]any{"id": c.Organization}
	}
	if c.User != "" {
		data["user"] = c.User
	}
	if c.Role != "" {
		data["role"] = c.Role
	}
	return data
}

// organizationName names the organization inside a sentence.
func (c sessionClaims) organizationName() string {
	if c.Organization == "" {
		return "your organization"
	}
	return c.Organization
}

// organizationSubject names the organization at the start of a sentence.
func (c sessionClaims) organizationSubject() string {
	if c.Organization == "" {
		return "Your organization"
	}
	return c.Organization
}

// termsDocuments is never nil, so JSON shows an empty list, not null.
func termsDocuments(documents []control.TermsDocument) []control.TermsDocument {
	if documents == nil {
		return []control.TermsDocument{}
	}
	return documents
}

// documentList gives each document on its own indented line.
func documentList(documents []control.TermsDocument) string {
	var text strings.Builder
	for _, document := range documents {
		fmt.Fprintf(&text, "\n  %s: %s", document.Name, document.URL)
	}
	return text.String()
}
