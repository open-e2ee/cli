package notifications

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpoSetupAndNSEPreserveConfiguration(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "package.json"), `{
  "dependencies": {
    "@bacons/apple-targets": "1.0.0",
    "expo": "55.0.0",
    "expo-notifications": "1.0.0"
  }
}`)
	writeFixture(t, filepath.Join(root, "app.json"), `{
  "expo": {
    "name": "Chat",
    "ios": {"bundleIdentifier": "dev.open_e2ee.chat"},
    "plugins": [["existing-plugin", {"preserve": true}]]
  }
}`)

	setup, err := SetupIOS(root)
	if err != nil || setup.Kind != ExpoCNG || setup.RemainingAction != "" {
		t.Fatalf("setup failed: %#v %v", setup, err)
	}
	added, err := AddNSE(root)
	if err != nil || added.RemainingAction != "" {
		t.Fatalf("NSE generation failed: %#v %v", added, err)
	}
	if _, err := VerifyIOS(root, true); err != nil {
		t.Fatalf("generated Expo configuration did not verify: %v", err)
	}

	contents, err := os.ReadFile(filepath.Join(root, "app.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "existing-plugin") || strings.Contains(string(contents), "usernotifications.filtering") {
		t.Fatalf("configuration was lost or filtering was added: %s", contents)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, targetDir, "NotificationService.swift")); err != nil {
		t.Fatal(err)
	}

	second, err := AddNSE(root)
	if err != nil || len(second.Changed) != 0 {
		t.Fatalf("NSE generation is not idempotent: %#v %v", second, err)
	}
	writeFixture(t, filepath.Join(root, targetDir, "NotificationService.swift"), "// application-owned change\n")
	if _, err := AddNSE(root); err == nil || !strings.Contains(err.Error(), "preserve it") {
		t.Fatalf("rerun overwrote or accepted application-owned extension changes: %v", err)
	}
}

func TestExpoGoOnlyProjectGetsActionableRefusal(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "package.json"), `{"dependencies":{"expo":"55.0.0"}}`)
	writeFixture(t, filepath.Join(root, "app.json"), `{"expo":{"ios":{"bundleIdentifier":"dev.open_e2ee.chat"}}}`)

	result, err := SetupIOS(root)
	if err != nil || !strings.Contains(result.RemainingAction, "Expo Go cannot") {
		t.Fatalf("Expo Go limitation was not actionable: %#v %v", result, err)
	}
	if _, err := VerifyIOS(root, false); err == nil || !strings.Contains(err.Error(), "Expo Go is not valid verification") {
		t.Fatalf("Expo Go verification was not refused: %v", err)
	}
}

func TestBareReactNativeGeneratesSafeHandoff(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "package.json"), `{"dependencies":{"react-native":"0.84.0"}}`)
	if err := os.MkdirAll(filepath.Join(root, "ios", "Chat.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "ios", "Chat.xcodeproj", "project.pbxproj"), "// project\n")

	result, err := AddNSE(root)
	if err != nil || result.Kind != BareReactNative || !strings.Contains(result.RemainingAction, "Xcode") {
		t.Fatalf("bare handoff failed: %#v %v", result, err)
	}
	if _, err := VerifyIOS(root, true); err == nil || !strings.Contains(err.Error(), TargetName) {
		t.Fatalf("missing bare target was not refused: %v", err)
	}
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
