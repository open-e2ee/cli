package notifications

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	TargetName = "OpenE2EENotificationService"
	targetDir  = "targets/open-e2ee-notification-service"
)

type ProjectKind string

const (
	ExpoCNG         ProjectKind = "expo-cng"
	BareReactNative ProjectKind = "bare-react-native"
)

type Result struct {
	Changed         []string
	Kind            ProjectKind
	RemainingAction string
}

type packageManifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func Inspect(root string) (ProjectKind, packageManifest, error) {
	contents, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", packageManifest{}, errors.New("package.json was not found; run this command from the application root")
	}
	var manifest packageManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return "", packageManifest{}, fmt.Errorf("parse package.json: %w", err)
	}
	if dependency(manifest, "expo") {
		return ExpoCNG, manifest, nil
	}
	if dependency(manifest, "react-native") && directoryExists(filepath.Join(root, "ios")) {
		return BareReactNative, manifest, nil
	}
	return "", packageManifest{}, errors.New("no Expo CNG or bare React Native iOS application was found")
}

func SetupIOS(root string) (Result, error) {
	kind, manifest, err := Inspect(root)
	if err != nil {
		return Result{}, err
	}
	result := Result{Kind: kind}
	if kind == ExpoCNG {
		changed, err := updateExpoConfig(root, false)
		if err != nil {
			return Result{}, err
		}
		result.Changed = changed
		if !dependency(manifest, "expo-notifications") {
			result.RemainingAction = "run `npx expo install expo-notifications`, then create a development or native build; Expo Go cannot receive this remote-push configuration"
		}
		return result, nil
	}
	result.RemainingAction = "enable Push Notifications and the remote-notification background mode on the application target in Xcode; the CLI will verify the signed result"
	return result, nil
}

func AddNSE(root string) (Result, error) {
	kind, manifest, err := Inspect(root)
	if err != nil {
		return Result{}, err
	}
	result := Result{Kind: kind}
	if kind == ExpoCNG {
		changed, err := updateExpoConfig(root, true)
		if err != nil {
			return Result{}, err
		}
		result.Changed = append(result.Changed, changed...)
	}
	generated, err := writeTargetFiles(root)
	if err != nil {
		return Result{}, err
	}
	result.Changed = append(result.Changed, generated...)
	sort.Strings(result.Changed)
	if kind == ExpoCNG && !dependency(manifest, "@bacons/apple-targets") {
		result.RemainingAction = "run `npx expo install @bacons/apple-targets`, then run `npx expo prebuild --clean`; Expo Go cannot contain a Notification Service Extension"
	}
	if kind == BareReactNative {
		result.RemainingAction = "add the generated Swift file as a Notification Service Extension target named OpenE2EENotificationService in Xcode; do not add the filtering entitlement"
	}
	return result, nil
}

func VerifyIOS(root string, requireNSE bool) (Result, error) {
	kind, manifest, err := Inspect(root)
	if err != nil {
		return Result{}, err
	}
	result := Result{Kind: kind}
	if kind == ExpoCNG {
		if !dependency(manifest, "expo-notifications") {
			return Result{}, errors.New("expo-notifications is not installed; Expo Go is not valid verification, so run `npx expo install expo-notifications` and build a development or native app")
		}
		if err := verifyExpoConfig(root, requireNSE); err != nil {
			return Result{}, err
		}
		if requireNSE && !dependency(manifest, "@bacons/apple-targets") {
			return Result{}, errors.New("@bacons/apple-targets is not installed, so Expo cannot generate the Notification Service Extension target")
		}
		result.RemainingAction = "run a development or native build and pass its .app path to `oe notifications verify ios --app-bundle PATH`; Expo Go is not verification"
		return result, nil
	}
	project, err := findPBXProject(root)
	if err != nil {
		return Result{}, err
	}
	contents, err := os.ReadFile(project)
	if err != nil {
		return Result{}, err
	}
	if requireNSE && (!bytes.Contains(contents, []byte(TargetName)) || !bytes.Contains(contents, []byte("NotificationService.swift"))) {
		return Result{}, errors.New("the bare React Native Xcode project does not contain the OpenE2EENotificationService target and generated Swift source")
	}
	result.RemainingAction = "build the app and pass its .app path to `oe notifications verify ios --app-bundle PATH`"
	return result, nil
}

func VerifySignedApp(appBundle string, requireNSE bool) error {
	info, err := os.Stat(appBundle)
	if err != nil || !info.IsDir() || filepath.Ext(appBundle) != ".app" {
		return errors.New("--app-bundle must name an existing .app bundle")
	}
	if err := exec.Command("codesign", "--verify", "--deep", "--strict", appBundle).Run(); err != nil {
		return errors.New("the application bundle does not pass strict code-signature verification")
	}
	appEntitlements, err := signedEntitlements(appBundle)
	if err != nil || !bytes.Contains(appEntitlements, []byte("aps-environment")) {
		return errors.New("the signed application does not contain the APNs environment entitlement")
	}
	if !requireNSE {
		return nil
	}
	extensions, err := filepath.Glob(filepath.Join(appBundle, "PlugIns", "*.appex"))
	if err != nil || len(extensions) != 1 {
		return errors.New("the signed application must contain exactly one Notification Service Extension for this verification")
	}
	if err := exec.Command("codesign", "--verify", "--strict", extensions[0]).Run(); err != nil {
		return errors.New("the Notification Service Extension does not pass code-signature verification")
	}
	extensionEntitlements, err := signedEntitlements(extensions[0])
	if err != nil {
		return errors.New("the Notification Service Extension entitlements cannot be inspected")
	}
	if bytes.Contains(extensionEntitlements, []byte("com.apple.developer.usernotifications.filtering")) {
		return errors.New("notification filtering is present but cannot be activated before Apple approval and physical-device evidence")
	}
	return nil
}

func signedEntitlements(path string) ([]byte, error) {
	command := exec.Command("codesign", "-d", "--entitlements", ":-", path)
	return command.CombinedOutput()
}

func updateExpoConfig(root string, includeNSE bool) ([]string, error) {
	path := filepath.Join(root, "app.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("dynamic Expo configuration is not rewritten automatically; add expo-notifications to expo.plugins and remote-notification to expo.ios.infoPlist.UIBackgroundModes, or add an app.json before retrying")
		}
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, fmt.Errorf("parse app.json: %w", err)
	}
	expo := object(document, "expo")
	plugins := anyList(expo["plugins"])
	plugins = appendPlugin(plugins, "expo-notifications")
	if includeNSE {
		plugins = appendPlugin(plugins, "@bacons/apple-targets")
	}
	expo["plugins"] = plugins
	ios := object(expo, "ios")
	infoPlist := object(ios, "infoPlist")
	backgroundModes := stringList(infoPlist["UIBackgroundModes"])
	infoPlist["UIBackgroundModes"] = stringsToAny(appendUnique(backgroundModes, "remote-notification"))
	if includeNSE {
		bundleIdentifier, ok := ios["bundleIdentifier"].(string)
		if !ok || strings.TrimSpace(bundleIdentifier) == "" {
			return nil, errors.New("expo.ios.bundleIdentifier is required before adding a Notification Service Extension")
		}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if bytes.Equal(contents, encoded) {
		return nil, nil
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return nil, err
	}
	return []string{"app.json"}, nil
}

func writeTargetFiles(root string) ([]string, error) {
	directory := filepath.Join(root, targetDir)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	files := map[string]string{
		"expo-target.config.js": "module.exports = {\n  type: \"notification-service\",\n  name: \"OpenE2EENotificationService\",\n  bundleIdentifier: \".openE2EENotificationService\",\n  entitlements: {},\n};\n",
		"Info.plist": `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>NSExtension</key><dict><key>NSExtensionPointIdentifier</key><string>com.apple.usernotifications.service</string><key>NSExtensionPrincipalClass</key><string>$(PRODUCT_MODULE_NAME).NotificationService</string></dict></dict></plist>
`,
		"NotificationService.swift": `import UserNotifications

final class NotificationService: UNNotificationServiceExtension {
  private var contentHandler: ((UNNotificationContent) -> Void)?
  private var fallbackContent: UNMutableNotificationContent?

  override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
    self.contentHandler = contentHandler
    let content = (request.content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
    self.fallbackContent = content
    contentHandler(content)
    self.contentHandler = nil
    self.fallbackContent = nil
  }

  override func serviceExtensionTimeWillExpire() {
    if let contentHandler, let fallbackContent { contentHandler(fallbackContent) }
  }
}
`,
	}
	changed := make([]string, 0, len(files))
	for name, contents := range files {
		path := filepath.Join(directory, name)
		existing, readErr := os.ReadFile(path)
		if string(existing) == contents {
			continue
		}
		if readErr == nil {
			return nil, fmt.Errorf("%s already exists with different content; preserve it and configure the native target manually", filepath.ToSlash(filepath.Join(targetDir, name)))
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return nil, err
		}
		changed = append(changed, filepath.ToSlash(filepath.Join(targetDir, name)))
	}
	return changed, nil
}

func verifyExpoConfig(root string, requireNSE bool) error {
	contents, err := os.ReadFile(filepath.Join(root, "app.json"))
	if err != nil {
		return errors.New("app.json is required for deterministic Expo notification verification")
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		return err
	}
	expo := object(document, "expo")
	if !hasPlugin(anyList(expo["plugins"]), "expo-notifications") {
		return errors.New("the Expo configuration does not include expo-notifications")
	}
	ios := object(expo, "ios")
	if !contains(stringList(object(ios, "infoPlist")["UIBackgroundModes"]), "remote-notification") {
		return errors.New("the Expo configuration does not enable the remote-notification background mode")
	}
	if requireNSE {
		if !hasPlugin(anyList(expo["plugins"]), "@bacons/apple-targets") {
			return errors.New("the Expo configuration does not include the Apple target generator")
		}
		if _, err := os.Stat(filepath.Join(root, targetDir, "NotificationService.swift")); err != nil {
			return errors.New("the generated Notification Service Extension source is missing")
		}
	}
	return nil
}

func dependency(manifest packageManifest, name string) bool {
	return manifest.Dependencies[name] != "" || manifest.DevDependencies[name] != ""
}

func object(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func stringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func anyList(value any) []any {
	values, _ := value.([]any)
	return append([]any{}, values...)
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}
	return append(values, value)
}

func appendPlugin(values []any, name string) []any {
	if hasPlugin(values, name) {
		return values
	}
	return append(values, name)
}

func hasPlugin(values []any, name string) bool {
	for _, value := range values {
		if value == name {
			return true
		}
		if configured, ok := value.([]any); ok && len(configured) > 0 && configured[0] == name {
			return true
		}
	}
	return false
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func findPBXProject(root string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "ios", "*.xcodeproj", "project.pbxproj"))
	if err != nil || len(matches) != 1 {
		return "", errors.New("exactly one bare React Native Xcode project is required")
	}
	return matches[0], nil
}
