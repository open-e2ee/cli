package agent

import (
	"slices"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAgentDetectionReadsTheAgentVariables(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment map[string]string
		harness     string
	}{
		{"OpenCode", map[string]string{"OPENCODE": "1"}, "opencode"},
		{"Qwen Code", map[string]string{"QWEN_CODE_SESSION_ID": "qwen-session"}, "qwen-code"},
		{"Pi", map[string]string{"PI_CODING_AGENT": "true"}, "pi"},
		{"Pi through AI_AGENT", map[string]string{"AI_AGENT": "pi"}, "pi"},
		{"Cursor Agent", map[string]string{"CURSOR_AGENT": "1"}, "cursor-agent"},
		{"Claude Code", map[string]string{"CLAUDECODE": "1"}, "claude-code"},
		{"Codex", map[string]string{"CODEX_THREAD_ID": "codex-thread"}, "codex"},
		{"Amp", map[string]string{"AMP_CURRENT_THREAD_ID": "amp-thread"}, "amp"},
		{"Gemini CLI", map[string]string{"GEMINI_CLI": "1"}, "gemini-agent"},
		{"Auggie", map[string]string{"AUGMENT_AGENT": "1"}, "auggie"},
		{"Crush", map[string]string{"CRUSH": "1"}, "crush"},
		{"Crush through AI_AGENT", map[string]string{"AI_AGENT": "crush"}, "crush"},
		{"VS Code Copilot through AI_AGENT", map[string]string{"AI_AGENT": "github_copilot_vscode_agent"}, "vscode-copilot-agent"},
		{"VS Code Copilot", map[string]string{"COPILOT_AGENT": "1"}, "vscode-copilot-agent"},
		{"Warp", map[string]string{"OZ_RUN_ID": "warp-run"}, "warp"},
		{"a direct agent before its host", map[string]string{"OZ_RUN_ID": "warp-run", "CLAUDECODE": "1"}, "claude-code"},
		{"no variable", map[string]string{}, ""},
		{"an exact marker with another value", map[string]string{"CLAUDECODE": "true", "OPENCODE": "yes", "PI_CODING_AGENT": "1"}, ""},
		{"an AI_AGENT value that no row names", map[string]string{"AI_AGENT": "some-other-agent"}, ""},
		{"a false-like session ID", map[string]string{"CODEX_THREAD_ID": " False ", "QWEN_CODE_SESSION_ID": "0", "AMP_CURRENT_THREAD_ID": "off", "OZ_RUN_ID": " "}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, detected := Detect(environment(test.environment))
			if detected != (test.harness != "") || harness.ID != test.harness {
				t.Fatalf("Detect = %#v, %v; want %q", harness, detected, test.harness)
			}
			if detected && harness.Name == "" {
				t.Fatalf("harness %q has no name", harness.ID)
			}
		})
	}
}

func TestAgentModeOverridesDetection(t *testing.T) {
	claudeCode := environment(map[string]string{"CLAUDECODE": "1"})
	empty := environment(nil)
	for _, test := range []struct {
		mode        Mode
		getenv      func(string) string
		underAgent  bool
		harnessID   string
		description string
	}{
		{Auto, claudeCode, true, "claude-code", "auto follows the environment"},
		{Auto, empty, false, "", "auto with no variable"},
		{Yes, empty, true, "", "yes with no variable"},
		{Yes, claudeCode, true, "claude-code", "yes keeps the detected name"},
		{No, claudeCode, false, "", "no ignores the environment"},
	} {
		harness, underAgent := Resolve(test.mode, test.getenv)
		if underAgent != test.underAgent || harness.ID != test.harnessID {
			t.Fatalf("%s: Resolve = %#v, %v", test.description, harness, underAgent)
		}
	}
	for _, mode := range []Mode{"", "true", "YES"} {
		if mode.Valid() {
			t.Fatalf("--agent accepted %q", mode)
		}
	}
}

func TestVariablesNameEveryRowOnce(t *testing.T) {
	names := Variables()
	if !slices.IsSorted(names) || len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Fatalf("Variables is not sorted and unique: %v", names)
	}
	for _, entry := range signals {
		if !slices.Contains(names, entry.variable) {
			t.Fatalf("Variables omits %s", entry.variable)
		}
	}
}
