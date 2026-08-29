package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tidwall/jsonc"
)

const (
	Filename  = "open-e2ee.jsonc"
	SchemaURL = "https://open-e2ee.dev/schemas/config/v1.json"
)

type Config struct {
	Schema              string                 `json:"$schema"`
	Project             string                 `json:"project"`
	Writer              string                 `json:"writer"`
	SelectedEnvironment string                 `json:"selectedEnvironment"`
	Relay               RelayPolicy            `json:"relay"`
	Environments        map[string]Environment `json:"environments"`
}

type RelayPolicy struct {
	DeliveryRetention   string `json:"deliveryRetention"`
	AttachmentRetention string `json:"attachmentRetention"`
}

type Environment struct {
	RelayURL string       `json:"relayUrl,omitempty"`
	Relay    *RelayPolicy `json:"relay,omitempty"`
}

func (c Config) RelayPolicyFor(environment string) (RelayPolicy, error) {
	if environment != "development" && environment != "production" {
		return RelayPolicy{}, fmt.Errorf("unsupported environment %q", environment)
	}
	selected, ok := c.Environments[environment]
	if !ok {
		return RelayPolicy{}, fmt.Errorf("environments.%s is required", environment)
	}
	if selected.Relay != nil {
		return *selected.Relay, nil
	}
	return c.Relay, nil
}

func RetentionSeconds(value string) (int, error) {
	seconds := map[string]int{
		"1h": 3_600, "6h": 21_600, "12h": 43_200, "1d": 86_400,
		"3d": 259_200, "7d": 604_800, "14d": 1_209_600,
		"30d": 2_592_000,
	}[value]
	if seconds == 0 {
		return 0, errors.New("must be one of 1h, 6h, 12h, 1d, 3d, 7d, 14d, or 30d")
	}
	return seconds, nil
}

func New(project string) Config {
	return Config{
		Schema:              SchemaURL,
		Project:             project,
		Writer:              "config",
		SelectedEnvironment: "development",
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
	if c.SelectedEnvironment != "development" && c.SelectedEnvironment != "production" {
		return errors.New("selectedEnvironment must be development or production")
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
		if environment.RelayURL != "" {
			if err := validateRelayURL(name, environment.RelayURL); err != nil {
				return fmt.Errorf("environments.%s.relayUrl: %w", name, err)
			}
		}
	}
	if err := validateRetention(c.Relay.DeliveryRetention); err != nil {
		return fmt.Errorf("relay.deliveryRetention: %w", err)
	}
	if err := validateRetention(c.Relay.AttachmentRetention); err != nil {
		return fmt.Errorf("relay.attachmentRetention: %w", err)
	}
	for _, name := range []string{"development", "production"} {
		policy, err := c.RelayPolicyFor(name)
		if err != nil {
			return err
		}
		maximum := 30 * 24 * 60 * 60
		if name == "development" {
			maximum = 7 * 24 * 60 * 60
		}
		for field, value := range map[string]string{
			"attachmentRetention": policy.AttachmentRetention,
			"deliveryRetention":   policy.DeliveryRetention,
		} {
			seconds, err := RetentionSeconds(value)
			if err != nil || seconds > maximum {
				return fmt.Errorf("environments.%s.relay.%s exceeds the managed maximum", name, field)
			}
		}
	}
	return nil
}

var relayConnectionPath = regexp.MustCompile(`^/v1/connection/[A-Za-z0-9_-]{1,255}$`)

func validateRelayURL(environment, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != value || !relayConnectionPath.MatchString(parsed.EscapedPath()) {
		return errors.New("must be an environment-scoped Managed Relay connection URL")
	}
	expectedHost := "relay.open-e2ee.dev"
	if environment == "development" {
		expectedHost = "development.relay.open-e2ee.dev"
	}
	if parsed.Host != expectedHost {
		return fmt.Errorf("belongs to another environment; expected %s", expectedHost)
	}
	return nil
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
