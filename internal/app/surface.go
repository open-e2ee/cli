package app

import (
	"fmt"
	"strings"

	"github.com/open-e2ee/oe/internal/output"
)

// commandSpec is one entry of the public command surface. Help text, the JSON
// help document, and usage errors all read this table, so they cannot drift.
type commandSpec struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Usage   []string `json:"usage"`
}

type globalFlagSpec struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type exitCodeSpec struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

type variableSpec struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

const (
	exitFailure        = 1
	exitUsage          = 2
	exitAuthentication = 4
	exitPersonAction   = 5
	exitTemporary      = 6
)

var commandSurface = []commandSpec{
	{"init", "Write open-e2ee.jsonc and an in-memory example script. Needs no login.", []string{
		"oe init [--directory PATH] [--name PROJECT] [--force]",
	}},
	{"auth", "Log in with a browser and store the session in the OS keychain, then accept the OpenE2EE terms for the organization. Show or remove the session.", []string{
		"oe auth login [--accept-terms] [--timeout DURATION]",
		"oe auth status",
		"oe auth logout",
	}},
	{"sandbox", "Create or update the Sandbox environment, write its Relay connection to .env.local, and wait for the first acknowledged message.", []string{
		"oe sandbox [--timeout DURATION] [--no-wait]",
	}},
	{"plan", "Show the Relay policy changes that a deploy applies, without a change.", []string{
		"oe plan [--environment sandbox|production]",
	}},
	{"deploy", "Deploy Relay policy to Production and write its Relay connection to .env.production.local.", []string{
		"oe deploy [--confirm]",
	}},
	{"doctor", "Check the config, session, project, and Relay connection of the selected environment.", []string{
		"oe doctor [--environment sandbox|production]",
	}},
	{"project", "Read the Relay project, print a Relay connection URL, or select another project.", []string{
		"oe project show [PROJECT]",
		"oe project connection [PROJECT] [--environment sandbox|production]",
		"oe project select PROJECT",
	}},
	{"notifications", "Stage and verify best-effort iOS notification profiles.", []string{
		"oe notifications status",
		"oe notifications setup ios [--profile background-only|visible-alert]",
		"oe notifications add-nse",
		"oe notifications verify ios [--app-bundle PATH]",
		"oe notifications apple-filtering-request",
	}},
	{"version", "Print the CLI version.", []string{
		"oe version",
	}},
	{"help", "Show all commands, or the usage of one command.", []string{
		"oe help [COMMAND]",
	}},
}

var globalFlagSurface = []globalFlagSpec{
	{"--json", "Write one JSON document to stdout for the result, for success and for failure. oe auth login writes one pending event before it, while a person approves the device."},
	{"--json-stream", "Write newline-delimited JSON progress events, then one final event, to stdout."},
	{"--agent yes|no|auto", "Say whether a coding agent runs oe. The default, auto, reads the environment variables that coding agents set. Under an agent, the default output is JSON, and oe never prompts and never opens a browser. --agent no restores text output."},
	{"--environment sandbox|production", "Select the environment. The default is the command's own environment, then selectedEnvironment in open-e2ee.jsonc, then sandbox."},
	{"--control-url URL", "Use another control API. It must use HTTPS except on loopback."},
	{"-h, --help", "Show help."},
}

var exitCodeSurface = []exitCodeSpec{
	{0, "The command succeeded."},
	{exitFailure, "The command failed. The error and its code tell why."},
	{exitUsage, "The command line is invalid or a required input is missing."},
	{exitAuthentication, "Authentication is required. Run oe auth login."},
	{exitPersonAction, "A person must act before the command can continue. The error tells what to do. When action.url is present, it is the page that the person opens."},
	{exitTemporary, "The failure is temporary. It is safe to run the same command again later. next is that command."},
}

var variableSurface = []variableSpec{
	{"OE_ACCESS_TOKEN", "A scoped CI credential. The CLI keeps it in memory and never stores it."},
	{"OE_ACCESS_TOKEN_SCOPES", "The scopes of OE_ACCESS_TOKEN, separated by commas or spaces."},
	{"OE_OPERATION_ID", "The idempotency key for each remote mutation of one run. Set it only to retry one mutation."},
}

func lookupCommand(name string) (commandSpec, bool) {
	for _, spec := range commandSurface {
		if spec.Name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func (r *runner) help() error {
	var text strings.Builder
	text.WriteString("oe manages OpenE2EE Signal Protocol Relay projects from a repository.\n\n")
	text.WriteString("Usage: oe <command> [arguments] [global flags]\n\nCommands:\n")
	for _, spec := range commandSurface {
		fmt.Fprintf(&text, "  %-14s %s\n", spec.Name, spec.Summary)
	}
	text.WriteString("\nGlobal flags (before or after the command):\n")
	for _, flag := range globalFlagSurface {
		fmt.Fprintf(&text, "  %s\n      %s\n", flag.Name, flag.Summary)
	}
	text.WriteString("\nExit codes:\n")
	for _, exit := range exitCodeSurface {
		fmt.Fprintf(&text, "  %d  %s\n", exit.Code, exit.Meaning)
	}
	text.WriteString("\nEnvironment variables:\n")
	for _, variable := range variableSurface {
		fmt.Fprintf(&text, "  %s\n      %s\n", variable.Name, variable.Summary)
	}
	text.WriteString("\nRun oe help <command> for one command. Run oe help --json for this surface as JSON.")
	message := text.String()
	if r.out.Mode() != output.Text {
		message = "The data field describes every command. Run oe help <command> for one command."
	}
	return r.out.Success("help", message, map[string]any{
		"commands":             commandSurface,
		"globalFlags":          globalFlagSurface,
		"exitCodes":            exitCodeSurface,
		"environmentVariables": variableSurface,
	})
}

func (r *runner) commandHelp(name string) error {
	spec, ok := lookupCommand(name)
	if !ok {
		return unknownCommand(name)
	}
	message := spec.Summary
	if r.out.Mode() == output.Text {
		message += "\n\nUsage:\n  " + strings.Join(spec.Usage, "\n  ")
	}
	return r.out.Success(spec.Name, message, map[string]any{
		"name": spec.Name, "summary": spec.Summary, "usage": spec.Usage,
	})
}
