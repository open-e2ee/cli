package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

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
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
