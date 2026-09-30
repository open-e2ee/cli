package app

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/open-e2ee/oe/internal/config"
	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/output"
)

// problem is a command failure with a stable code, the exit status that the
// code implies, and the command that moves the caller forward.
type problem struct {
	code    string
	message string
	next    string
	exit    int
	data    map[string]any
	cause   error

	// actionURL is a page that a person must open to move forward.
	actionURL string
}

func (p *problem) Error() string { return p.message }
func (p *problem) Unwrap() error { return p.cause }

func usageError(command, message string) error {
	next := "oe help"
	if _, ok := lookupCommand(command); ok {
		next += " " + command
	}
	return &problem{code: "USAGE_ERROR", message: message, next: next, exit: exitUsage}
}

func unknownCommand(name string) error {
	return &problem{code: "USAGE_ERROR", message: fmt.Sprintf("unknown command %q", name), next: "oe help", exit: exitUsage}
}

func loginRequired(code, message string, cause error) error {
	return &problem{code: code, message: message, next: "oe auth login", exit: exitAuthentication, cause: cause}
}

// classify gives every error that reaches Run a code and an exit status. A
// control API refusal keeps the code that the console sent. A temporary failure
// exits 6, and its next is the command line of the run. A terms refusal exits
// 5: a person must accept, or an administrator must.
func classify(err error, commandLine string) *problem {
	if known, ok := errors.AsType[*problem](err); ok {
		return known
	}
	if failure, ok := errors.AsType[*config.Error](err); ok {
		result := &problem{code: failure.Code, message: err.Error(), next: commandLine, exit: exitFailure, data: failure.Data, cause: err}
		switch failure.Code {
		case "NODE_REQUIRED":
			result.exit, result.actionURL = exitPersonAction, "https://nodejs.org/en/download"
		case "CONFIG_EDIT_REQUIRED":
			result.exit = exitPersonAction
		}
		return result
	}
	if refusal, ok := errors.AsType[*control.APIError](err); ok {
		result := &problem{code: cmp.Or(refusal.Code, "CONTROL_ERROR"), message: err.Error(), exit: exitFailure, cause: err}
		switch {
		case refusal.Status == http.StatusUnauthorized || refusal.Code == "AUTHENTICATION_REQUIRED" || refusal.Code == "INVALID_SESSION":
			result.code = cmp.Or(refusal.Code, "AUTHENTICATION_REQUIRED")
			result.next = "oe auth login"
			result.exit = exitAuthentication
		case refusal.Code == "ORGANIZATION_REQUIRED":
			result.next = "oe auth login"
		case refusal.Code == "TERMS_REQUIRED":
			result.exit = exitPersonAction
			result.data = map[string]any{
				"canAccept": refusal.CanAccept, "documents": termsDocuments(refusal.Documents),
				"retry": commandLine,
			}
			if refusal.CanAccept {
				result.next = "oe auth login --accept-terms"
			}
		case refusal.Code == "TERMS_PERMISSION_REQUIRED":
			result.exit = exitPersonAction
		case refusal.Code == "AUTHORITY_UNAVAILABLE", refusal.Status == http.StatusTooManyRequests,
			refusal.Status == http.StatusBadGateway, refusal.Status == http.StatusServiceUnavailable,
			refusal.Status == http.StatusGatewayTimeout:
			result.next = commandLine
			result.exit = exitTemporary
		}
		return result
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return &problem{code: "CONTROL_UNAVAILABLE", message: err.Error(), next: commandLine, exit: exitTemporary, cause: err}
	}
	return &problem{code: "COMMAND_FAILED", message: err.Error(), exit: exitFailure, cause: err}
}

// commandLine writes args as one oe command line for a POSIX shell. It quotes
// each argument that holds a character outside a safe set.
func commandLine(args []string) string {
	words := []string{"oe"}
	for _, argument := range args {
		if argument != "" && strings.Trim(argument, shellSafe) == "" {
			words = append(words, argument)
			continue
		}
		words = append(words, "'"+strings.ReplaceAll(argument, "'", `'\''`)+"'")
	}
	return strings.Join(words, " ")
}

const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_-"

func (p *problem) output() output.Problem {
	return output.Problem{
		Message: p.message, Code: p.code, Next: p.next,
		Action: output.Action{URL: p.actionURL}, Data: p.data,
	}
}
