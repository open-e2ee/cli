package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testAccessToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800}`))
	return fmt.Sprintf("%s.%s.signature", header, payload)
}

func TestWorkOSDeviceAuthorizationAndRefreshStayOffTheControlOrigin(t *testing.T) {
	var polls atomic.Int32
	var refreshes atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/auth/configuration":
			json.NewEncoder(response).Encode(authConfiguration{
				ClientID: "client_test", DeviceAuthorizationEndpoint: server.URL + "/user_management/authorize/device",
				SchemaVersion: 1, TokenEndpoint: server.URL + "/user_management/authenticate",
			})
		case "/user_management/authorize/device":
			if err := request.ParseForm(); err != nil || request.Form.Get("client_id") != "client_test" {
				t.Fatalf("invalid device authorization form: %v %#v", err, request.Form)
			}
			json.NewEncoder(response).Encode(deviceAuthorizationResponse{
				DeviceCode: "device-secret", ExpiresIn: 300, Interval: 1,
				UserCode: "ABCD-EFGH", VerificationURIComplete: "https://auth.example/device?user_code=ABCD-EFGH",
			})
		case "/user_management/authenticate":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch request.Form.Get("grant_type") {
			case "urn:ietf:params:oauth:grant-type:device_code":
				if request.Form.Get("device_code") != "device-secret" {
					t.Fatal("device code changed")
				}
				switch polls.Add(1) {
				case 1:
					response.WriteHeader(http.StatusBadRequest)
					response.Write([]byte(`{"error":"authorization_pending"}`))
					return
				case 2:
					response.WriteHeader(http.StatusBadRequest)
					response.Write([]byte(`{"error":"slow_down"}`))
					return
				}
				json.NewEncoder(response).Encode(oauthTokenResponse{AccessToken: testAccessToken(), RefreshToken: "refresh-one"})
			case "refresh_token":
				if request.Form.Get("refresh_token") != "refresh-one" {
					t.Fatal("refresh token changed")
				}
				if refreshes.Add(1) < 3 {
					response.WriteHeader(http.StatusServiceUnavailable)
					response.Write([]byte(`{"error":"temporarily_unavailable"}`))
					return
				}
				json.NewEncoder(response).Encode(oauthTokenResponse{AccessToken: testAccessToken(), RefreshToken: "refresh-two"})
			default:
				t.Fatalf("unexpected grant %q", request.Form.Get("grant_type"))
			}
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := client.StartAuthorization(context.Background(), AuthorizationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := client.PollAuthorization(context.Background(), authorization)
	if err != nil || !pending.Pending {
		t.Fatalf("pending poll: %#v %v", pending, err)
	}
	slowed, err := client.PollAuthorization(context.Background(), authorization)
	if err != nil || !slowed.Pending || slowed.RetryAfterSeconds != 6 {
		t.Fatalf("slow-down poll: %#v %v", slowed, err)
	}
	issued, err := client.PollAuthorization(context.Background(), authorization)
	if err != nil || issued.AccessToken == "" || issued.RefreshToken != "refresh-one" || issued.ExpiresAt == "" {
		t.Fatalf("issued token: %#v %v", issued, err)
	}
	refreshed, err := client.RefreshAuthorization(context.Background(), issued.RefreshToken)
	if err != nil || refreshed.RefreshToken != "refresh-two" || refreshes.Load() != 3 {
		t.Fatalf("refreshed token: %#v %v", refreshed, err)
	}
}

func TestRefreshClassifiesInvalidGrantAsExpiredSession(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/auth/configuration" {
			json.NewEncoder(response).Encode(authConfiguration{
				ClientID: "client_test", DeviceAuthorizationEndpoint: server.URL + "/user_management/authorize/device",
				SchemaVersion: 1, TokenEndpoint: server.URL + "/user_management/authenticate",
			})
			return
		}
		response.WriteHeader(http.StatusBadRequest)
		response.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.RefreshAuthorization(context.Background(), "expired-refresh")
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("invalid_grant was not classified as expired: %v", err)
	}
}

func TestMutationsCarryBearerAndIdempotencyHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("unexpected authorization header %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "operation-1" {
			t.Errorf("unexpected idempotency key %q", got)
		}
		if request.URL.Path != "/v1/projects/bootstrap" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		json.NewEncoder(response).Encode(Bootstrap{ProjectSlug: "chat", Writer: "config"})
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.BootstrapDevelopment(context.Background(), CredentialRequest{AccessToken: "secret-token", OperationID: "operation-1"}, BootstrapRequest{ProjectSlug: "chat", Writer: "config"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostWithoutIdempotencyKeyIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/auth/configuration" {
			json.NewEncoder(response).Encode(authConfiguration{
				ClientID: "client_test", DeviceAuthorizationEndpoint: server.URL + "/user_management/authorize/device",
				SchemaVersion: 1, TokenEndpoint: server.URL + "/user_management/authenticate",
			})
			return
		}
		calls.Add(1)
		http.Error(response, `{"code":"temporary","message":"try later"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.StartAuthorization(context.Background(), AuthorizationRequest{})
	if err == nil {
		t.Fatal("expected authorization failure")
	}
	if calls.Load() != 1 {
		t.Fatalf("non-idempotent POST was retried %d times", calls.Load())
	}
}

func TestPostWithIdempotencyKeyRetriesTransientFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(response, `{"code":"temporary","message":"try later"}`, http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		json.NewEncoder(response).Encode(Bootstrap{ProjectSlug: "chat", Writer: "config"})
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.BootstrapDevelopment(context.Background(), CredentialRequest{OperationID: "stable-operation"}, BootstrapRequest{ProjectSlug: "chat", Writer: "config"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("want 3 calls, got %d", calls.Load())
	}
}

func TestErrorDoesNotEchoBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		response.Write([]byte(`{"code":"unauthorized","message":"credential rejected"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetProject(context.Background(), CredentialRequest{AccessToken: "secret-token"}, "chat")
	if err == nil {
		t.Fatal("expected health failure")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("error exposed bearer token")
	}
}

func TestNewRejectsNonHTTPURLs(t *testing.T) {
	if _, err := New("file:///tmp/control", nil); err == nil {
		t.Fatal("accepted non-HTTP control URL")
	}
}
