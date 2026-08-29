package control

import "context"

type API interface {
	Health(context.Context) error
	StartAuthorization(context.Context, AuthorizationRequest) (Authorization, error)
	PollAuthorization(context.Context, Authorization) (Token, error)
	RefreshAuthorization(context.Context, string) (Token, error)
	BootstrapDevelopment(context.Context, CredentialRequest, BootstrapRequest) (Bootstrap, error)
	Activation(context.Context, CredentialRequest, string) (Activation, error)
	Plan(context.Context, CredentialRequest, PlanRequest) (Plan, error)
	Deploy(context.Context, CredentialRequest, DeployRequest) (Deployment, error)
	GetProject(context.Context, CredentialRequest, string) (Project, error)
}

type CredentialRequest struct {
	AccessToken string
	OperationID string
}

type AuthorizationRequest struct{}

type Authorization struct {
	ClientID         string `json:"-"`
	DeviceCode       string `json:"-"`
	ExpiresInSeconds int    `json:"-"`
	IntervalSeconds  int    `json:"intervalSeconds"`
	TokenEndpoint    string `json:"-"`
	UserCode         string `json:"userCode"`
	VerificationURL  string `json:"verificationUrl"`
}

type Token struct {
	Pending           bool   `json:"pending"`
	AccessToken       string `json:"accessToken,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	RefreshToken      string `json:"refreshToken,omitempty"`
	RetryAfterSeconds int    `json:"-"`
}

type BootstrapRequest struct {
	Policy      RelayPolicyRequest `json:"policy"`
	ProjectSlug string             `json:"project"`
	Writer      string             `json:"writer"`
}

type Bootstrap struct {
	ProjectID           string `json:"projectId"`
	ProjectSlug         string `json:"project"`
	Writer              string `json:"writer"`
	Revision            string `json:"revision"`
	DevelopmentRelayURL string `json:"developmentRelayUrl"`
	Environment         string `json:"environment"`
}

type Activation struct {
	FirstDevice       bool `json:"firstDevice"`
	FirstAcknowledged bool `json:"firstAcknowledgedMessage"`
}

type PlanRequest struct {
	Environment string             `json:"environment"`
	Policy      RelayPolicyRequest `json:"policy"`
	ProjectSlug string             `json:"project"`
	Writer      string             `json:"writer"`
}

type RelayPolicyRequest struct {
	AttachmentRetentionSeconds int `json:"attachmentRetentionSeconds"`
	DeliveryTtlSeconds         int `json:"deliveryTtlSeconds"`
}

type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type Plan struct {
	ID               string   `json:"id"`
	ProjectSlug      string   `json:"project"`
	Environment      string   `json:"environment"`
	ExpectedRevision string   `json:"expectedRevision"`
	Changes          []Change `json:"changes"`
	BillingReady     bool     `json:"billingReady"`
	BillingSetupURL  string   `json:"billingSetupUrl,omitempty"`
}

type DeployRequest struct {
	ExpectedRevision string             `json:"expectedRevision"`
	PlanID           string             `json:"planId"`
	Policy           RelayPolicyRequest `json:"policy"`
	ProjectSlug      string             `json:"project"`
	Writer           string             `json:"writer"`
}

type Deployment struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Status   string `json:"status"`
	RelayURL string `json:"relayUrl"`
}

type Project struct {
	Development *ProjectEnvironment `json:"development,omitempty"`
	Production  *ProjectEnvironment `json:"production,omitempty"`
	Slug        string              `json:"slug"`
	Writer      string              `json:"writer"`
}

type ProjectEnvironment struct {
	AttachmentRetentionSeconds int    `json:"attachmentRetentionSeconds"`
	DeliveryTtlSeconds         int    `json:"deliveryTtlSeconds"`
	RelayURL                   string `json:"relayUrl"`
	Revision                   string `json:"revision"`
}
