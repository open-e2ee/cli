package acceptance_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type commandResult struct {
	Status  string `json:"status"`
	Command string `json:"command"`
}

func TestGoldenInitWorkflow(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	project := t.TempDir()
	command := exec.Command("go", "run", "./cmd/oe", "--json", "init", "--directory", project, "--name", "golden-chat")
	command.Dir = root
	command.Env = append(os.Environ(), "OE_TEST_MODE=1")

	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stdout
	if err := command.Run(); err != nil {
		t.Fatalf("golden init workflow failed: %v\n%s", err, stdout.String())
	}

	var result commandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode init result: %v\n%s", err, stdout.String())
	}
	if result != (commandResult{Status: "ok", Command: "init"}) {
		t.Fatalf("unexpected init result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(project, "open-e2ee.jsonc")); err != nil {
		t.Fatalf("initializer did not write open-e2ee.jsonc: %v", err)
	}
}

func TestCommandSurfaceGolden(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	command := exec.Command("go", "run", "./cmd/oe", "--json", "help")
	command.Dir = root

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("command surface failed: %v\n%s", err, output)
	}

	golden, err := os.ReadFile(filepath.Join("testdata", "help.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(output), bytes.TrimSpace(golden)) {
		t.Fatalf("command surface drifted\nwant: %s\n got: %s", golden, output)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
