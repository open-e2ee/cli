package credential

import (
	"errors"
	"testing"
)

func TestResolvePrefersScopedCICredentialWithoutStoringIt(t *testing.T) {
	t.Setenv("OE_ACCESS_TOKEN", "ci-token")
	t.Setenv("OE_ACCESS_TOKEN_SCOPES", "project:read,deploy:write")
	store := NewMemory()
	value, err := Resolve(store, "https://control.example")
	if err != nil {
		t.Fatal(err)
	}
	if value.AccessToken != "ci-token" || value.Source != "environment" {
		t.Fatalf("unexpected credential: %#v", value)
	}
	if _, err := store.Get("https://control.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CI credential was stored: %v", err)
	}
	if err := RequireScope(value, "deploy:write"); err != nil {
		t.Fatal(err)
	}
	if err := RequireScope(value, "secret:write"); err == nil {
		t.Fatal("accepted missing scope")
	}
}

func TestProfileDropsPathsAndNormalizesHost(t *testing.T) {
	profile, err := Profile("https://CONTROL.EXAMPLE/v1")
	if err != nil {
		t.Fatal(err)
	}
	if profile != "https://control.example" {
		t.Fatalf("unexpected profile %q", profile)
	}
}

func TestMemoryStoreLifecycle(t *testing.T) {
	store := NewMemory()
	if err := store.Set("profile", Credential{AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get("profile")
	if err != nil || value.AccessToken != "token" {
		t.Fatalf("get: %#v, %v", value, err)
	}
	if err := store.Delete("profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete did not remove credential: %v", err)
	}
}
