package control

import "context"

type API interface {
	Health(context.Context) error
	StartAuthorization(context.Context, AuthorizationRequest) (Authorization, error)
	PollAuthorization(context.Context, string) (Token, error)
	BootstrapDevelopment(context.Context, CredentialRequest, BootstrapRequest) (Bootstrap, error)
	Activation(context.Context, CredentialRequest, string) (Activation, error)
	Plan(context.Context, CredentialRequest, PlanRequest) (Plan, error)
	Deploy(context.Context, CredentialRequest, DeployRequest) (Deployment, error)
	ListProjects(context.Context, CredentialRequest) ([]Project, error)
	GetProject(context.Context, CredentialRequest, string) (Project, error)
	ListProviders(context.Context, CredentialRequest, string) ([]Provider, error)
	SetProvider(context.Context, CredentialRequest, ProviderRequest) (Provider, error)
	ListSecrets(context.Context, CredentialRequest, string) ([]Secret, error)
	SetSecret(context.Context, CredentialRequest, SecretRequest) error
	DeleteSecret(context.Context, CredentialRequest, string, string) error
}

type CredentialRequest struct {
	AccessToken string
	OperationID string
}

type AuthorizationRequest struct {
	Scopes []string `json:"scopes"`
}

type Authorization struct {
	ID              string `json:"id"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
	IntervalSeconds int    `json:"intervalSeconds"`
}

type Token struct {
	Pending     bool     `json:"pending"`
	AccessToken string   `json:"accessToken,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
}

type BootstrapRequest struct {
	ProjectSlug      string `json:"project"`
	Writer           string `json:"writer"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
}

type Bootstrap struct {
	ProjectID            string `json:"projectId"`
	ProjectSlug          string `json:"project"`
	Writer               string `json:"writer"`
	Revision             string `json:"revision"`
	DevelopmentPublicKey string `json:"developmentPublishableKey"`
	ProductionPublicKey  string `json:"productionPublishableKey"`
	Environment          string `json:"environment"`
}

type Activation struct {
	FirstDevice       bool `json:"firstDevice"`
	FirstAcknowledged bool `json:"firstAcknowledgedMessage"`
}

type PlanRequest struct {
	ProjectSlug      string `json:"project"`
	Environment      string `json:"environment"`
	Writer           string `json:"writer"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
	Config           any    `json:"config"`
}

type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type Plan struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"projectId"`
	Environment      string   `json:"environment"`
	ExpectedRevision string   `json:"expectedRevision"`
	Changes          []Change `json:"changes"`
	BillingReady     bool     `json:"billingReady"`
	BillingSetupURL  string   `json:"billingSetupUrl,omitempty"`
}

type DeployRequest struct {
	PlanID           string `json:"planId"`
	Writer           string `json:"writer"`
	ExpectedRevision string `json:"expectedRevision"`
}

type Deployment struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Status   string `json:"status"`
}

type Project struct {
	ID                        string `json:"id"`
	Slug                      string `json:"slug"`
	Writer                    string `json:"writer"`
	Revision                  string `json:"revision"`
	DevelopmentPublishableKey string `json:"developmentPublishableKey,omitempty"`
	ProductionPublishableKey  string `json:"productionPublishableKey,omitempty"`
}

type Provider struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Issuer      string `json:"issuer"`
	Environment string `json:"environment"`
}

type ProviderRequest struct {
	ProjectSlug string `json:"project"`
	Environment string `json:"environment"`
	Kind        string `json:"kind"`
	Issuer      string `json:"issuer,omitempty"`
}

type Secret struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

type SecretRequest struct {
	ProjectSlug string `json:"project"`
	Environment string `json:"environment"`
	Name        string `json:"name"`
	Value       string `json:"value"`
}
