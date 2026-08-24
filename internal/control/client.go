package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(baseURL string, client *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid control URL %q", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{baseURL: parsed, http: client}, nil
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/health", CredentialRequest{}, nil, nil)
}

func (c *Client) StartAuthorization(ctx context.Context, request AuthorizationRequest) (Authorization, error) {
	var response Authorization
	err := c.do(ctx, http.MethodPost, "/v1/cli/authorizations", CredentialRequest{}, request, &response)
	return response, err
}

func (c *Client) PollAuthorization(ctx context.Context, id string) (Token, error) {
	var response Token
	err := c.do(ctx, http.MethodGet, "/v1/cli/authorizations/"+url.PathEscape(id), CredentialRequest{}, nil, &response)
	return response, err
}

func (c *Client) BootstrapDevelopment(ctx context.Context, credential CredentialRequest, request BootstrapRequest) (Bootstrap, error) {
	var response Bootstrap
	err := c.do(ctx, http.MethodPost, "/v1/projects/bootstrap", credential, request, &response)
	return response, err
}

func (c *Client) Activation(ctx context.Context, credential CredentialRequest, project string) (Activation, error) {
	var response Activation
	err := c.do(ctx, http.MethodGet, projectPath(project, "activation"), credential, nil, &response)
	return response, err
}

func (c *Client) Plan(ctx context.Context, credential CredentialRequest, request PlanRequest) (Plan, error) {
	var response Plan
	err := c.do(ctx, http.MethodPost, projectPath(request.ProjectSlug, "deploys/plan"), credential, request, &response)
	return response, err
}

func (c *Client) Deploy(ctx context.Context, credential CredentialRequest, request DeployRequest) (Deployment, error) {
	var response Deployment
	err := c.do(ctx, http.MethodPost, "/v1/deploys", credential, request, &response)
	return response, err
}

func (c *Client) ListProjects(ctx context.Context, credential CredentialRequest) ([]Project, error) {
	var response struct {
		Projects []Project `json:"projects"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/projects", credential, nil, &response)
	return response.Projects, err
}

func (c *Client) GetProject(ctx context.Context, credential CredentialRequest, project string) (Project, error) {
	var response Project
	err := c.do(ctx, http.MethodGet, projectPath(project, ""), credential, nil, &response)
	return response, err
}

func (c *Client) ListProviders(ctx context.Context, credential CredentialRequest, project string) ([]Provider, error) {
	var response struct {
		Providers []Provider `json:"providers"`
	}
	err := c.do(ctx, http.MethodGet, projectPath(project, "providers"), credential, nil, &response)
	return response.Providers, err
}

func (c *Client) SetProvider(ctx context.Context, credential CredentialRequest, request ProviderRequest) (Provider, error) {
	var response Provider
	err := c.do(ctx, http.MethodPut, projectPath(request.ProjectSlug, "providers/"+url.PathEscape(request.Environment)), credential, request, &response)
	return response, err
}

func (c *Client) ListSecrets(ctx context.Context, credential CredentialRequest, project string) ([]Secret, error) {
	var response struct {
		Secrets []Secret `json:"secrets"`
	}
	err := c.do(ctx, http.MethodGet, projectPath(project, "secrets"), credential, nil, &response)
	return response.Secrets, err
}

func (c *Client) SetSecret(ctx context.Context, credential CredentialRequest, request SecretRequest) error {
	return c.do(ctx, http.MethodPut, projectPath(request.ProjectSlug, "secrets/"+url.PathEscape(request.Name)), credential, request, nil)
}

func (c *Client) DeleteSecret(ctx context.Context, credential CredentialRequest, project, name string) error {
	return c.do(ctx, http.MethodDelete, projectPath(project, "secrets/"+url.PathEscape(name)), credential, nil, nil)
}

func (c *Client) do(ctx context.Context, method, endpoint string, credential CredentialRequest, body, output any) error {
	encoded, err := encodeBody(body)
	if err != nil {
		return err
	}
	retryable := method != http.MethodPost || credential.OperationID != ""
	for attempt := 0; attempt < 3; attempt++ {
		requestURL := *c.baseURL
		requestURL.Path = path.Join(strings.TrimSuffix(c.baseURL.Path, "/"), endpoint)
		request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(encoded))
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if credential.AccessToken != "" {
			request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
		}
		if credential.OperationID != "" {
			request.Header.Set("Idempotency-Key", credential.OperationID)
		}
		response, err := c.http.Do(request)
		if err != nil {
			if attempt < 2 && retryable {
				continue
			}
			return sanitizeCredentialError(fmt.Errorf("control request failed: %w", err), credential.AccessToken)
		}
		err = decodeResponse(response, output)
		response.Body.Close()
		if err == nil {
			return nil
		}
		var statusError *statusError
		if attempt < 2 && retryable && errors.As(err, &statusError) && statusError.retryable {
			continue
		}
		return sanitizeCredentialError(err, credential.AccessToken)
	}
	return errors.New("control request exhausted retries")
}

func sanitizeCredentialError(err error, accessToken string) error {
	if err == nil || accessToken == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), accessToken, "[redacted]"))
}

type statusError struct {
	status    int
	message   string
	retryable bool
}

func (e *statusError) Error() string { return e.message }

func decodeResponse(response *http.Response, output any) error {
	limited := io.LimitReader(response.Body, 1<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem apiError
		_ = json.NewDecoder(limited).Decode(&problem)
		message := strings.TrimSpace(problem.Message)
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		if problem.Code != "" {
			message = problem.Code + ": " + message
		}
		return &statusError{status: response.StatusCode, message: message, retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("decode control response: %w", err)
	}
	return nil
}

func encodeBody(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode control request: %w", err)
	}
	return encoded, nil
}

func projectPath(project, suffix string) string {
	base := "/v1/projects/" + url.PathEscape(project)
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}
