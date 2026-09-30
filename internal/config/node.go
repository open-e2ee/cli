package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	oe "github.com/open-e2ee/oe/packages/oe"
)

// scriptTimeout bounds one node run. The loader runs the config file, which
// can start work that never ends.
const scriptTimeout = 30 * time.Second

// evaluate runs the loader on the config file at path and returns the JSON
// value of its default export.
func evaluate(path string) (any, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	output, err := runScript(filepath.Dir(absolute), "config-load.mjs", nil, absolute)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		return nil, fmt.Errorf("read the value of %s: %w", Filename, err)
	}
	return value, nil
}

// runScript extracts the embedded scripts to a new temporary directory and
// runs one of them with the node on PATH, in directory. A script reports a
// refusal as {code, message} on the last line of stderr.
func runScript(directory, script string, stdin []byte, args ...string) ([]byte, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, &Error{
			Code:    "NODE_REQUIRED",
			Message: fmt.Sprintf("oe needs Node.js 22.18 or later to read %s; node is not on PATH", Filename),
		}
	}
	scripts, err := os.MkdirTemp("", "oe-config-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scripts)
	if err := extract(scripts); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, node, append([]string{filepath.Join(scripts, "lib", script)}, args...)...)
	command.Dir = directory
	command.Stdin = bytes.NewReader(stdin)
	command.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if ctx.Err() != nil {
		return nil, &Error{
			Code:    "CONFIG_INVALID",
			Message: fmt.Sprintf("%s did not finish within %s", Filename, scriptTimeout),
		}
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	var refusal struct{ Code, Message string }
	if json.Unmarshal([]byte(lines[len(lines)-1]), &refusal) == nil && refusal.Code != "" {
		return nil, &Error{Code: refusal.Code, Message: refusal.Message}
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		return nil, err
	}
	tail := lines[max(0, len(lines)-10):]
	return nil, &Error{
		Code:    "CONFIG_INVALID",
		Message: fmt.Sprintf("node failed to run %s: %s", script, strings.TrimSpace(strings.Join(tail, "\n"))),
	}
}

func extract(directory string) error {
	return fs.WalkDir(oe.Scripts, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(directory, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := oe.Scripts.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}
