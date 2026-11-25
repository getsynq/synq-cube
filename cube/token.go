package cube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// How long a token that this package signs is valid.
	signedTokenLifetime = 15 * time.Minute
	// How long before the end of its life a token is replaced.
	tokenRenewBefore = 2 * time.Minute
	// How long a Cube Cloud token is kept if it does not say when it expires.
	cloudTokenLifetimeFallback = 10 * time.Minute
	// The largest response body of the Cube Cloud Platform API that is read.
	cloudResponseLimit = 4 << 20
)

// TokenSource gives the credential of a request to the Cube REST API.
type TokenSource interface {
	// Authorization returns the value of the `Authorization` header.
	Authorization(ctx context.Context) (string, error)
}

// NewStaticTokenSource returns a TokenSource for a JSON Web Token that the caller made. The
// token goes into the header as it is, so a caller can add `Bearer ` if the deployment needs it.
func NewStaticTokenSource(token string) (TokenSource, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("cube: the API token is empty")
	}
	return staticTokenSource(token), nil
}

type staticTokenSource string

func (s staticTokenSource) Authorization(context.Context) (string, error) {
	return string(s), nil
}

// NewAPISecretTokenSource returns a TokenSource that signs a short-lived HS256 token with the API
// secret of the deployment (`CUBEJS_API_SECRET`). securityContext is a JSON object, and its
// properties become the claims of the token. Empty means an empty security context.
func NewAPISecretTokenSource(apiSecret string, securityContext string) (TokenSource, error) {
	if apiSecret == "" {
		return nil, errors.New("cube: the API secret is empty")
	}
	claims, err := ParseSecurityContext(securityContext)
	if err != nil {
		return nil, err
	}
	return &secretTokenSource{secret: []byte(apiSecret), claims: claims, now: time.Now}, nil
}

// ParseSecurityContext reads a security context. It accepts an empty text and a JSON object.
func ParseSecurityContext(securityContext string) (map[string]any, error) {
	claims := map[string]any{}
	if strings.TrimSpace(securityContext) == "" {
		return claims, nil
	}
	decoder := json.NewDecoder(strings.NewReader(securityContext))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil, fmt.Errorf("cube: the security context is not a JSON object: %w", err)
	}
	if claims == nil {
		return nil, errors.New("cube: the security context is not a JSON object")
	}
	return claims, nil
}

type secretTokenSource struct {
	secret []byte
	claims map[string]any
	now    func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (s *secretTokenSource) Authorization(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expires.Add(-tokenRenewBefore)) {
		return s.token, nil
	}
	claims := jwt.MapClaims{}
	for name, value := range s.claims {
		claims[name] = value
	}
	expires := now.Add(signedTokenLifetime)
	claims["iat"] = now.Unix()
	claims["exp"] = expires.Unix()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("cube: cannot sign the token: %w", err)
	}
	s.token = token
	s.expires = expires
	return token, nil
}

// CloudConfig is the access to a deployment on Cube Cloud.
type CloudConfig struct {
	// URL is the URL of the Cube Cloud account (`https://example.cubecloud.dev`).
	URL string
	// APIKey is an API key of the Cube Cloud Platform API.
	APIKey string
	// DeploymentID is the number in the URL of the deployment in Cube Cloud.
	DeploymentID int64
	// EnvironmentID is the id of the environment. Zero means the production environment.
	EnvironmentID int64
	// SecurityContext is a JSON object. Empty means an empty security context.
	SecurityContext string
	// HTTPClient makes the requests to the Platform API. Nil means a client with a timeout.
	HTTPClient *http.Client
}

// CloudTokenSource creates a token for the REST API of a Cube Cloud deployment with a Platform
// API key (`tokens-for-meta-sync`). The token has the scopes `meta` and `meta-private`.
type CloudTokenSource struct {
	baseURL         string
	apiKey          string
	deploymentID    int64
	securityContext map[string]any
	httpClient      *http.Client
	now             func() time.Time

	mu            sync.Mutex
	environmentID int64
	restURL       string
	token         string
	expires       time.Time
}

// NewCloudTokenSource checks the configuration. It sends no request.
func NewCloudTokenSource(config CloudConfig) (*CloudTokenSource, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.URL), "/")
	if baseURL == "" {
		return nil, errors.New("cube: the Cube Cloud URL is empty")
	}
	if config.APIKey == "" {
		return nil, errors.New("cube: the Cube Cloud API key is empty")
	}
	if config.DeploymentID <= 0 {
		return nil, errors.New("cube: the Cube Cloud deployment id is not set")
	}
	securityContext, err := ParseSecurityContext(config.SecurityContext)
	if err != nil {
		return nil, err
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultRequestTimeout}
	}
	return &CloudTokenSource{
		baseURL:         baseURL,
		apiKey:          config.APIKey,
		deploymentID:    config.DeploymentID,
		securityContext: securityContext,
		httpClient:      httpClient,
		now:             time.Now,
		environmentID:   config.EnvironmentID,
	}, nil
}

// Authorization returns `Bearer ` and a token for the REST API of the deployment.
func (s *CloudTokenSource) Authorization(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expires.Add(-tokenRenewBefore)) {
		return "Bearer " + s.token, nil
	}
	if err := s.resolveEnvironment(ctx); err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"security_context": s.securityContext,
		"scopes":           []string{"meta-private", "meta"},
	})
	if err != nil {
		return "", fmt.Errorf("cube cloud: cannot write the token request: %w", err)
	}
	path := fmt.Sprintf("/api/v1/deployments/%d/environments/%d/tokens-for-meta-sync", s.deploymentID, s.environmentID)
	response, err := s.platform(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	token := cloudToken(response)
	if token == "" {
		return "", errors.New("cube cloud: the token response has no token")
	}
	s.token = token
	s.expires = tokenExpiry(token, now)
	return "Bearer " + token, nil
}

// APIURL returns the base URL of the REST API of the environment, as Cube Cloud reports it
// (`https://example.cubecloudapp.dev/cubejs-api`).
func (s *CloudTokenSource) APIURL(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restURL == "" {
		// A set environment id does not give the URL, so the list is read in both cases.
		if err := s.readEnvironments(ctx); err != nil {
			return "", err
		}
	}
	if s.restURL == "" {
		return "", errors.New("cube cloud: the environment has no REST API URL")
	}
	return s.restURL, nil
}

func (s *CloudTokenSource) resolveEnvironment(ctx context.Context) error {
	if s.environmentID > 0 {
		return nil
	}
	if err := s.readEnvironments(ctx); err != nil {
		return err
	}
	if s.environmentID <= 0 {
		return fmt.Errorf("cube cloud: deployment %d has no production environment", s.deploymentID)
	}
	return nil
}

// readEnvironments finds the environment (the production one if no id is set) and its REST URL.
func (s *CloudTokenSource) readEnvironments(ctx context.Context) error {
	response, err := s.platform(ctx, http.MethodGet, fmt.Sprintf("/api/v1/deployments/%d/environments", s.deploymentID), nil)
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			ID             json.Number `json:"id"`
			Type           string      `json:"type"`
			APICredentials []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"api_credentials"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.UseNumber()
	if err := decoder.Decode(&list); err != nil {
		return fmt.Errorf("cube cloud: cannot read the list of environments: %w", err)
	}
	for _, item := range list.Items {
		id, err := item.ID.Int64()
		if err != nil {
			continue
		}
		if s.environmentID > 0 && id != s.environmentID {
			continue
		}
		if s.environmentID <= 0 && item.Type != "production" {
			continue
		}
		s.environmentID = id
		for _, credential := range item.APICredentials {
			if credential.Type == "rest" && credential.URL != "" {
				s.restURL = normalizeAPIURL(credential.URL)
			}
		}
		return nil
	}
	return nil
}

func (s *CloudTokenSource) platform(ctx context.Context, method string, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("cube cloud: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cube cloud: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, cloudResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("cube cloud: %s %s: %w", method, path, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("cube cloud: %s %s: %w", method, path, &APIError{StatusCode: response.StatusCode, Message: errorMessage(data)})
	}
	return data, nil
}

// cloudToken reads the token from `{"token": ...}` and from `{"data": {"token": ...}}`.
func cloudToken(response []byte) string {
	var body struct {
		Token string `json:"token"`
		Data  struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response, &body); err != nil {
		return ""
	}
	if body.Data.Token != "" {
		return body.Data.Token
	}
	return body.Token
}

// tokenExpiry reads the `exp` claim of a token without a check of the signature. The claim says
// only when to ask for a new token.
func tokenExpiry(token string, now time.Time) time.Time {
	fallback := now.Add(cloudTokenLifetimeFallback)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return fallback
	}
	seconds, err := claims.Exp.Float64()
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Unix(int64(seconds), 0)
}
