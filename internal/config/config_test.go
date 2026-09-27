package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLoadAndFind(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "project", "nested")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(directory), Filename)
	value := New("secure-chat")
	if err := Write(path, value); err != nil {
		t.Fatal(err)
	}
	found, err := Find(directory)
	if err != nil {
		t.Fatal(err)
	}
	if found != path {
		t.Fatalf("want %s, got %s", path, found)
	}
	loaded, err := Load(found)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Project != "secure-chat" || loaded.Writer != "config" {
		t.Fatalf("unexpected config: %#v", loaded)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "// Public service policy") {
		t.Fatal("missing public-policy warning")
	}
}

func TestLoadAcceptsJSONC(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	contents := `{
  "$schema": "https://open-e2ee.dev/schemas/config/v1.json",
  "project": "jsonc-chat", // project comment
  "writer": "config",
  "selectedEnvironment": "sandbox",
  "relay": { "deliveryRetention": "30d", "attachmentRetention": "30d", },
  "environments": {
    "sandbox": { "relay": { "deliveryRetention": "1d", "attachmentRetention": "1d" } },
    "production": {}
  },
}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("valid JSONC rejected: %v", err)
	}
}

func TestEnvironmentUsesOneRelayConnectionURL(t *testing.T) {
	for name, urls := range map[string][2]string{
		"customer": {
			"https://sandbox.relay.open-e2ee.dev/signal/v1/connection/public-locator",
			"https://relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
		"staging": {
			"https://stage-sandbox.relay.open-e2ee.dev/signal/v1/connection/public-locator",
			"https://stage.relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), Filename)
			contents := fmt.Sprintf(`{
  "$schema": "https://open-e2ee.dev/schemas/config/v1.json",
  "project": "url-only-chat",
  "writer": "config",
  "selectedEnvironment": "production",
  "relay": { "deliveryRetention": "30d", "attachmentRetention": "30d" },
  "environments": {
    "sandbox": {
      "relayUrl": %q,
      "relay": { "deliveryRetention": "1d", "attachmentRetention": "1d" }
    },
    "production": { "relayUrl": %q }
  }
			}`, urls[0], urls[1])
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			value, err := Load(path)
			if err != nil {
				t.Fatalf("one Relay connection URL was rejected: %v", err)
			}
			if value.Environments["sandbox"].RelayURL == "" || value.Environments["production"].RelayURL == "" {
				t.Fatal("Relay connection URL was not retained")
			}
		})
	}
}

func TestEnvironmentRejectsCrossedRelayConnectionURL(t *testing.T) {
	for name, crossed := range map[string]struct {
		environment string
		url         string
	}{
		"customer sandbox to production": {
			environment: "sandbox",
			url:         "https://relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
		"customer production to sandbox": {
			environment: "production",
			url:         "https://sandbox.relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
		"staging sandbox to production": {
			environment: "sandbox",
			url:         "https://stage.relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
		"staging production to sandbox": {
			environment: "production",
			url:         "https://stage-sandbox.relay.open-e2ee.dev/signal/v1/connection/public-locator",
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := New("crossed-host")
			value.Environments[crossed.environment] = Environment{RelayURL: crossed.url}
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "belongs to another environment") {
				t.Fatalf("want crossed environment error, got %v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownEnvironments(t *testing.T) {
	value := New("safe-chat")
	value.Environments["staging"] = Environment{}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported environment") {
		t.Fatalf("want unsupported environment error, got %v", err)
	}
}

func TestLoadRejectsSecretFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	contents := `{
  "$schema": "https://open-e2ee.dev/schemas/config/v1.json",
  "project": "safe-chat",
  "writer": "config",
  "selectedEnvironment": "sandbox",
  "relay": { "deliveryRetention": "30d", "attachmentRetention": "30d" },
  "environments": { "sandbox": {}, "production": {} },
  "apiSecret": "must-not-be-here"
}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("secret-like unknown field was not rejected: %v", err)
	}
}

func TestValidateProjectSlug(t *testing.T) {
	for _, invalid := range []string{"", "Upper", "double--hyphen", "-leading", "trailing-", "has space"} {
		value := New(invalid)
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted invalid project %q", invalid)
		}
	}
}

func TestSandboxRetentionCannotExceedSevenDays(t *testing.T) {
	value := New("bounded-sandbox")
	value.Environments["sandbox"] = Environment{Relay: &RelayPolicy{
		DeliveryRetention: "14d", AttachmentRetention: "7d",
	}}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "managed maximum") {
		t.Fatalf("sandbox retention above seven days was accepted: %v", err)
	}
}
