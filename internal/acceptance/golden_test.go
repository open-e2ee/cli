package acceptance_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if _, err := os.Stat(filepath.Join(project, "open-e2ee.config.ts")); err != nil {
		t.Fatalf("initializer did not write open-e2ee.config.ts: %v", err)
	}
}

// TestBuiltBinaryLoadsTheConfig runs the built binary outside the repository,
// so the config loader can come only from the copy that the binary embeds.
// doctor reaches RELAY_CONNECTION_MISSING only after the config loads.
func TestBuiltBinaryLoadsTheConfig(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "oe")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/oe")
	build.Dir = repositoryRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build oe: %v\n%s", err, output)
	}
	project := t.TempDir()
	if output, err := exec.Command(binary, "--json", "init", "--directory", project, "--name", "loaded-chat").CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, output)
	}
	doctor := exec.Command(binary, "--json", "doctor")
	doctor.Dir = project
	output, _ := doctor.Output()
	var result struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode doctor result: %v\n%s", err, output)
	}
	if result.Code != "RELAY_CONNECTION_MISSING" {
		t.Fatalf("doctor did not load the config: %#v", result)
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
	var gotValue any
	var wantValue any
	if err := json.Unmarshal(output, &gotValue); err != nil {
		t.Fatalf("decode command output: %v", err)
	}
	if err := json.Unmarshal(golden, &wantValue); err != nil {
		t.Fatalf("decode golden output: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
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
