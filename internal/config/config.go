package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/jsonc"
)

const (
	Filename  = "open-e2ee.jsonc"
	SchemaURL = "https://open-e2ee.dev/schemas/config/v1.json"
)

type Config struct {
	Schema       string                 `json:"$schema"`
	Project      string                 `json:"project"`
	Writer       string                 `json:"writer"`
	Relay        RelayPolicy            `json:"relay"`
	Environments map[string]Environment `json:"environments"`
}

type RelayPolicy struct {
	DeliveryRetention   string `json:"deliveryRetention"`
	AttachmentRetention string `json:"attachmentRetention"`
}

type Environment struct {
	PublishableKey string       `json:"publishableKey,omitempty"`
	Relay          *RelayPolicy `json:"relay,omitempty"`
}

func New(project string) Config {
	return Config{
		Schema:  SchemaURL,
		Project: project,
		Writer:  "config",
		Relay: RelayPolicy{
			DeliveryRetention:   "30d",
			AttachmentRetention: "30d",
		},
		Environments: map[string]Environment{
			"development": {Relay: &RelayPolicy{DeliveryRetention: "1d", AttachmentRetention: "1d"}},
			"production":  {},
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

func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var value Config
	decoder := json.NewDecoder(bytes.NewReader(jsonc.ToJSON(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (c Config) Validate() error {
	if c.Schema != SchemaURL {
		return fmt.Errorf("$schema must be %q", SchemaURL)
	}
	if !validSlug(c.Project) {
		return errors.New("project must contain lowercase letters, digits, and single hyphens")
	}
	if c.Writer != "config" && c.Writer != "console" {
		return errors.New("writer must be config or console")
	}
	for _, name := range []string{"development", "production"} {
		if _, ok := c.Environments[name]; !ok {
			return fmt.Errorf("environments.%s is required", name)
		}
	}
	for name, environment := range c.Environments {
		if name != "development" && name != "production" {
			return fmt.Errorf("unsupported environment %q", name)
		}
		if environment.Relay != nil {
			if err := validateRetention(environment.Relay.DeliveryRetention); err != nil {
				return fmt.Errorf("environments.%s.relay.deliveryRetention: %w", name, err)
			}
			if err := validateRetention(environment.Relay.AttachmentRetention); err != nil {
				return fmt.Errorf("environments.%s.relay.attachmentRetention: %w", name, err)
			}
		}
	}
	if err := validateRetention(c.Relay.DeliveryRetention); err != nil {
		return fmt.Errorf("relay.deliveryRetention: %w", err)
	}
	return validateRetention(c.Relay.AttachmentRetention)
}

func Write(path string, value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	contents, err := marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".open-e2ee-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func marshal(value Config) ([]byte, error) {
	// encoding/json writes map keys in sorted order. Keep this comment header
	// stable so generated project files remain reviewable.
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("// Public service policy. Do not put secrets in this file.\n")
	output.Write(contents)
	output.WriteByte('\n')
	return output.Bytes(), nil
}

func validSlug(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validateRetention(value string) error {
	if value == "" {
		return nil
	}
	allowed := []string{"1h", "6h", "12h", "1d", "3d", "7d", "14d", "30d"}
	for _, candidate := range allowed {
		if candidate == value {
			return nil
		}
	}
	return fmt.Errorf("unsupported retention %q", value)
}
