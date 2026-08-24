package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const service = "dev.open-e2ee.cli"

var ErrNotFound = errors.New("credential not found")

type Credential struct {
	AccessToken string   `json:"accessToken"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	Source      string   `json:"-"`
}

type Store interface {
	Get(profile string) (Credential, error)
	Set(profile string, value Credential) error
	Delete(profile string) error
}

type Keychain struct{}

func (Keychain) Get(profile string) (Credential, error) {
	encoded, err := keyring.Get(service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("read OS keychain: %w", err)
	}
	var value Credential
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return Credential{}, fmt.Errorf("decode OS keychain credential: %w", err)
	}
	if value.AccessToken == "" {
		return Credential{}, errors.New("OS keychain credential is empty")
	}
	value.Source = "keychain"
	return value, nil
}

func (Keychain) Set(profile string, value Credential) error {
	if value.AccessToken == "" {
		return errors.New("refuse to store an empty access token")
	}
	copyValue := value
	copyValue.Source = ""
	sort.Strings(copyValue.Scopes)
	encoded, err := json.Marshal(copyValue)
	if err != nil {
		return err
	}
	if err := keyring.Set(service, profile, string(encoded)); err != nil {
		return fmt.Errorf("write OS keychain: %w", err)
	}
	return nil
}

func (Keychain) Delete(profile string) error {
	err := keyring.Delete(service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete OS keychain credential: %w", err)
	}
	return nil
}

func Resolve(store Store, profile string) (Credential, error) {
	if token := strings.TrimSpace(os.Getenv("OE_ACCESS_TOKEN")); token != "" {
		return Credential{
			AccessToken: token,
			Scopes:      splitScopes(os.Getenv("OE_ACCESS_TOKEN_SCOPES")),
			Source:      "environment",
		}, nil
	}
	return store.Get(profile)
}

func Profile(controlURL string) (string, error) {
	parsed, err := url.Parse(controlURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid control URL %q", controlURL)
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func RequireScope(value Credential, scope string) error {
	if len(value.Scopes) == 0 {
		// The control API remains authoritative. Older interactive credentials can
		// omit local scope metadata and are still checked by the server.
		return nil
	}
	for _, candidate := range value.Scopes {
		if candidate == scope || candidate == "*" {
			return nil
		}
	}
	return fmt.Errorf("credential does not include required scope %q", scope)
}

func splitScopes(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			result = append(result, field)
		}
	}
	sort.Strings(result)
	return result
}
