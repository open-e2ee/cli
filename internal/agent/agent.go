// Package agent tells a coding agent from a person. It reads only the
// environment variables that coding agents set for the commands that they
// run. It never reads the process tree.
package agent

import (
	"slices"
	"strings"
)

// Mode is the value of the --agent flag.
type Mode string

const (
	// Auto reads the environment.
	Auto Mode = "auto"
	// Yes treats the caller as an agent.
	Yes Mode = "yes"
	// No treats the caller as a person or a script.
	No Mode = "no"
)

// Valid reports whether m is a value that --agent accepts.
func (m Mode) Valid() bool {
	return m == Auto || m == Yes || m == No
}

// Harness is a coding agent that the environment names. ID is stable for a
// caller to record. Name is for a person to read.
type Harness struct {
	ID   string
	Name string
}

// signal is one environment variable that names a harness.
type signal struct {
	harness  Harness
	variable string
	// value is the exact value that the variable must hold. An empty value
	// accepts any value that is not empty or false-like.
	value string
}

var (
	openCode      = Harness{"opencode", "OpenCode"}
	qwenCode      = Harness{"qwen-code", "Qwen Code"}
	pi            = Harness{"pi", "Pi"}
	cursorAgent   = Harness{"cursor-agent", "Cursor Agent"}
	claudeCode    = Harness{"claude-code", "Claude Code"}
	codex         = Harness{"codex", "OpenAI Codex"}
	amp           = Harness{"amp", "Amp"}
	geminiCLI     = Harness{"gemini-agent", "Gemini CLI"}
	auggie        = Harness{"auggie", "Auggie"}
	crush         = Harness{"crush", "Crush"}
	vsCodeCopilot = Harness{"vscode-copilot-agent", "GitHub Copilot in VS Code"}
	warp          = Harness{"warp", "Warp"}
)

// signals is a copy of the detector list of the Cloudflare cf CLI,
// packages/cli/src/lib/agent-context.ts at cloudflare/cf commit b3f2d6af.
// Each comment gives the evidence that cf cites for its detector. The order is
// cf's precedence: a direct agent comes before a host surface, so Claude Code
// that runs inside Warp is Claude Code. Update the table from cf, not from a
// guess.
var signals = []signal{
	// cf line 274. opencode sets it at startup:
	// https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/index.ts#L76
	{openCode, "OPENCODE", "1"},
	// cf line 82: https://github.com/QwenLM/qwen-code/blob/c2902ae70f7/packages/core/src/services/shellContextEnv.ts#L120
	{qwenCode, "QWEN_CODE_SESSION_ID", ""},
	// cf line 110: https://github.com/badlogic/pi-mono/blob/a328aa89ad6e6dc5c5628ff896769532ed3d29df/packages/coding-agent/docs/environment-variables.md#L15
	{pi, "PI_CODING_AGENT", "true"},
	{pi, "AI_AGENT", "pi"},
	// cf line 146: Cursor Agent child-process environment contract, verified
	// by cf on 2026-09-02.
	{cursorAgent, "CURSOR_AGENT", "1"},
	// cf line 167: Claude Code child-process environment, verified by cf on
	// 2026-09-02.
	{claudeCode, "CLAUDECODE", "1"},
	// cf line 182: https://github.com/openai/codex/blob/cb1eea3e98ebc433ab5f9c12ce043e979d1902df/codex-rs/protocol/src/shell_environment.rs#L150
	{codex, "CODEX_THREAD_ID", ""},
	// cf line 194: Amp child-process environment contract, verified by cf on
	// 2026-09-02.
	{amp, "AMP_CURRENT_THREAD_ID", ""},
	// cf line 204: https://github.com/google-gemini/gemini-cli/blob/62364cb2000795537a6895261b37ec668e4cf527/packages/core/src/services/shellExecutionService.ts#L569
	{geminiCLI, "GEMINI_CLI", "1"},
	// cf line 210: @augmentcode/auggie local tool host environment, verified
	// by cf on 2026-09-02.
	{auggie, "AUGMENT_AGENT", "1"},
	// cf line 216: charmbracelet/crush shell and hooks environment, verified
	// by cf on 2026-09-02.
	{crush, "CRUSH", "1"},
	{crush, "AI_AGENT", "crush"},
	// cf line 234: VS Code Copilot agent child-process environment, verified
	// by cf on 2026-09-02.
	{vsCodeCopilot, "AI_AGENT", "github_copilot_vscode_agent"},
	{vsCodeCopilot, "COPILOT_AGENT", "1"},
	// cf line 259: Warp/Oz child-process environment contract, verified by cf
	// on 2026-09-02.
	{warp, "OZ_RUN_ID", ""},
}

// Detect returns the first harness in the table that the environment names.
func Detect(getenv func(string) string) (Harness, bool) {
	for _, entry := range signals {
		if entry.matches(getenv(entry.variable)) {
			return entry.harness, true
		}
	}
	return Harness{}, false
}

// Resolve applies mode to the detection. It reports whether an agent runs the
// CLI, and it returns the harness when the environment names one. No returns
// no harness, even when the environment names one.
func Resolve(mode Mode, getenv func(string) string) (Harness, bool) {
	if mode == No {
		return Harness{}, false
	}
	harness, detected := Detect(getenv)
	return harness, detected || mode == Yes
}

// Variables returns the name of each variable that Detect reads, sorted.
func Variables() []string {
	names := make([]string, 0, len(signals))
	for _, entry := range signals {
		names = append(names, entry.variable)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (s signal) matches(raw string) bool {
	if s.value != "" {
		return raw == s.value
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
