// Package config owns open-e2ee.config.ts, the public service policy of a
// project. The file is TypeScript, so node evaluates it: Load runs the loader
// script that the binary embeds and validates the JSON that it prints against
// schema/config-v2.json. Edit changes the source text with the splicer script,
// which parses the file and never evaluates it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

const Filename = "open-e2ee.config.ts"

type Config struct {
	Product      string       `json:"product"`
	Project      string       `json:"project"`
	Relay        RelayPolicy  `json:"relay"`
	Environments Environments `json:"environments"`
}

// RelayPolicy is the shared Relay policy. Each environment takes it unless
// its own relay section overrides a field.
type RelayPolicy struct {
	DeliveryRetention   string `json:"deliveryRetention"`
	AttachmentRetention string `json:"attachmentRetention"`
}

// Environments holds the Sandbox section and, once the project deploys to
// production, the Production section.
type Environments struct {
	Sandbox    Environment  `json:"sandbox"`
	Production *Environment `json:"production,omitempty"`
}

type Environment struct {
	Relay *RelayOverride `json:"relay,omitempty"`
}

// RelayOverride holds the fields of the shared policy that one environment
// changes.
type RelayOverride struct {
	DeliveryRetention   string `json:"deliveryRetention,omitempty"`
	AttachmentRetention string `json:"attachmentRetention,omitempty"`
}

// Error is a config failure with a stable code. Data holds the field errors
// of a value that is not valid, or the edit that a person must make.
type Error struct {
	Code    string
	Message string
	Data    map[string]any
}

func (e *Error) Error() string { return e.Message }

// FieldError is one schema or policy error, at the dotted path of the
// property.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ManualEdit is a change that Edit cannot make, because the file computes
// the value.
type ManualEdit struct {
	File              string `json:"file"`
	Path              string `json:"path"`
	CurrentExpression string `json:"currentExpression"`
	NewValue          any    `json:"newValue"`
}

// Change sets the value at a property path. A map value merges into the
// section at the path, and a missing section is added.
type Change struct {
	Path  []string `json:"path"`
	Value any      `json:"value"`
}

// RelayPolicyFor merges the environment's override into the shared policy,
// field by field.
func (c Config) RelayPolicyFor(environment string) (RelayPolicy, error) {
	var selected *Environment
	switch environment {
	case "sandbox":
		selected = &c.Environments.Sandbox
	case "production":
		selected = c.Environments.Production
	default:
		return RelayPolicy{}, fmt.Errorf("unsupported environment %q", environment)
	}
	if selected == nil {
		return RelayPolicy{}, fmt.Errorf("%s has no environments.production section", Filename)
	}
	policy := c.Relay
	if selected.Relay != nil {
		if selected.Relay.DeliveryRetention != "" {
			policy.DeliveryRetention = selected.Relay.DeliveryRetention
		}
		if selected.Relay.AttachmentRetention != "" {
			policy.AttachmentRetention = selected.Relay.AttachmentRetention
		}
	}
	return policy, nil
}

var retentionSeconds = map[string]int{
	"1h": 3_600, "6h": 21_600, "12h": 43_200, "1d": 86_400,
	"3d": 259_200, "7d": 604_800, "14d": 1_209_600,
	"30d": 2_592_000,
}

func RetentionSeconds(value string) (int, error) {
	seconds := retentionSeconds[value]
	if seconds == 0 {
		return 0, errors.New("must be one of 1h, 6h, 12h, 1d, 3d, 7d, 14d, or 30d")
	}
	return seconds, nil
}

// New returns the config that oe init writes.
func New(project string) Config {
	return Config{
		Product: "signal-relay",
		Project: project,
		Relay: RelayPolicy{
			DeliveryRetention:   "30d",
			AttachmentRetention: "30d",
		},
		Environments: Environments{
			Sandbox:    Environment{Relay: &RelayOverride{DeliveryRetention: "1d", AttachmentRetention: "1d"}},
			Production: &Environment{},
		},
	}
}

func Find(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, Filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("%s was not found", Filename)
		}
		abs = parent
	}
}

// Load evaluates the config file with node and validates its default export.
func Load(path string) (Config, error) {
	value, err := evaluate(path)
	if err != nil {
		return Config{}, err
	}
	return validate(value)
}

// Create writes value as the config file at path, and replaces any file
// there. It needs no node.
func Create(path string, value Config) error {
	contents, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(contents, &generic); err != nil {
		return err
	}
	if _, err := validate(generic); err != nil {
		return err
	}
	var source strings.Builder
	source.WriteString("// Public service policy. Do not put secrets in this file.\n")
	source.WriteString("import { defineConfig } from \"@open-e2ee/oe/config\";\n\n")
	source.WriteString("export default defineConfig(")
	writeSource(&source, generic, "")
	source.WriteString(");\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return replace(path, []byte(source.String()), 0o644)
}

// writeSource writes value as a TypeScript literal, with each property on its
// own line. The order is the order of the Config fields.
func writeSource(source *strings.Builder, value any, indent string) {
	object, ok := value.(map[string]any)
	if !ok {
		encoded, _ := json.Marshal(value)
		source.Write(encoded)
		return
	}
	if len(object) == 0 {
		source.WriteString("{}")
		return
	}
	source.WriteString("{\n")
	for _, key := range sourceOrder(object) {
		source.WriteString(indent + "  " + key + ": ")
		writeSource(source, object[key], indent+"  ")
		source.WriteString(",\n")
	}
	source.WriteString(indent + "}")
}

func sourceOrder(object map[string]any) []string {
	order := []string{
		"product", "project", "relay", "environments", "sandbox", "production",
		"deliveryRetention", "attachmentRetention",
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return slices.Index(order, a) - slices.Index(order, b)
	})
	return keys
}

// Edit applies changes to the config file. It replaces only the literals
// that change and adds each missing section, and it keeps every other byte.
// When a change reaches a value that the file computes, Edit writes nothing
// and returns CONFIG_EDIT_REQUIRED with the edit. The new file must load to
// the current value with the changes applied, or Edit writes nothing.
func Edit(path string, changes ...Change) error {
	current, err := evaluate(path)
	if err != nil {
		return err
	}
	var pending []Change
	for _, change := range changes {
		change.Value = normalize(change.Value)
		if !holds(current, change.Path, change.Value) {
			pending = append(pending, change)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	request, err := json.Marshal(map[string]any{"source": string(source), "changes": pending})
	if err != nil {
		return err
	}
	output, err := runScript(filepath.Dir(path), "config-splice.mjs", request)
	if err != nil {
		return err
	}
	var result struct {
		Source *string     `json:"source"`
		Edit   *ManualEdit `json:"edit"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("read the edit of %s: %w", Filename, err)
	}
	if result.Edit != nil {
		edit := *result.Edit
		edit.File = path
		newValue, _ := json.Marshal(edit.NewValue)
		return &Error{
			Code: "CONFIG_EDIT_REQUIRED",
			Message: fmt.Sprintf("%s computes %s with %s, so oe cannot change it; set it to %s",
				path, edit.Path, edit.CurrentExpression, newValue),
			Data: map[string]any{"edit": edit},
		}
	}
	if result.Source == nil {
		return fmt.Errorf("the edit of %s returned no source", Filename)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".open-e2ee.config-*.ts")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := writeFile(temporary, []byte(*result.Source), info.Mode().Perm()); err != nil {
		return err
	}
	edited, err := evaluate(temporaryName)
	if err != nil {
		return err
	}
	if _, err := validate(edited); err != nil {
		return err
	}
	want := current
	for _, change := range pending {
		want = apply(want, change.Path, change.Value)
	}
	if !reflect.DeepEqual(edited, normalize(want)) {
		return fmt.Errorf("the edit of %s changed more than the named values; the file is unchanged", Filename)
	}
	return os.Rename(temporaryName, path)
}

// holds reports whether current already has value at path. A map value holds
// when each of its fields holds.
func holds(current any, path []string, value any) bool {
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		if current, ok = object[key]; !ok {
			return false
		}
	}
	if fields, ok := value.(map[string]any); ok {
		for key, field := range fields {
			if !holds(current, []string{key}, field) {
				return false
			}
		}
		_, isObject := current.(map[string]any)
		return isObject
	}
	return reflect.DeepEqual(current, value)
}

// apply returns current with value set at path. A map value merges into the
// map that is there.
func apply(current any, path []string, value any) any {
	object, _ := current.(map[string]any)
	if len(path) == 0 {
		fields, ok := value.(map[string]any)
		if !ok || object == nil {
			return value
		}
		for key, field := range fields {
			object = apply(object, []string{key}, field).(map[string]any)
		}
		return object
	}
	result := make(map[string]any, len(object)+1)
	for key, field := range object {
		result[key] = field
	}
	result[path[0]] = apply(result[path[0]], path[1:], value)
	return result
}

// normalize returns value as encoding/json decodes it, so values compare with
// reflect.DeepEqual.
func normalize(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var result any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return value
	}
	return result
}

func replace(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".open-e2ee-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := writeFile(temporary, contents, mode); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

// writeFile writes contents to a new temporary file, syncs it, and closes it.
func writeFile(file *os.File, contents []byte, mode os.FileMode) error {
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
