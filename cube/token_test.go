package cube_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

type TokenSuite struct {
	suite.Suite
}

func TestTokenSuite(t *testing.T) {
	suite.Run(t, new(TokenSuite))
}

func (s *TokenSuite) TestStaticToken() {
	tokens, err := cube.NewStaticTokenSource("  abc.def.ghi\n")
	s.Require().NoError(err)
	header, err := tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.Equal("abc.def.ghi", header)

	_, err = cube.NewStaticTokenSource(" ")
	s.Error(err)
}

func (s *TokenSuite) TestAPISecretSignsAShortLivedHS256Token() {
	tokens, err := cube.NewAPISecretTokenSource("the-secret", `{"tenant_id": 42, "roles": ["admin"], "exp": 1}`)
	s.Require().NoError(err)
	header, err := tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.False(strings.HasPrefix(header, "Bearer "))

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(header, claims, func(*jwt.Token) (any, error) {
		return []byte("the-secret"), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	s.Require().NoError(err)
	s.True(parsed.Valid)
	s.EqualValues(42, claims["tenant_id"])
	s.Equal([]any{"admin"}, claims["roles"])

	expires, err := claims.GetExpirationTime()
	s.Require().NoError(err)
	issued, err := claims.GetIssuedAt()
	s.Require().NoError(err)
	s.WithinDuration(time.Now(), issued.Time, time.Minute)
	s.Greater(expires.Sub(time.Now()), time.Minute)
	s.Less(expires.Sub(time.Now()), time.Hour)

	again, err := tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.Equal(header, again, "the token is used again while it is valid")
}

func (s *TokenSuite) TestAPISecretWithEmptySecurityContext() {
	tokens, err := cube.NewAPISecretTokenSource("the-secret", "")
	s.Require().NoError(err)
	header, err := tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(header, claims, func(*jwt.Token) (any, error) { return []byte("the-secret"), nil })
	s.Require().NoError(err)
	s.Len(claims, 2)
}

func (s *TokenSuite) TestAPISecretRejectsBadInput() {
	_, err := cube.NewAPISecretTokenSource("", "")
	s.Error(err)
	for _, securityContext := range []string{`[1]`, `"text"`, `{`, `null`} {
		_, err = cube.NewAPISecretTokenSource("the-secret", securityContext)
		s.Error(err, securityContext)
	}
}

// fakeCloud is the part of the Cube Cloud Platform API that the token source calls.
type fakeCloud struct {
	*httptest.Server

	mu            sync.Mutex
	tokenRequests []fakeTokenRequest
	listRequests  int
	token         string
	tokenStatus   int
	restURL       string
}

type fakeTokenRequest struct {
	Path string
	Body map[string]any
}

func newFakeCloud(restURL string) *fakeCloud {
	f := &fakeCloud{token: unsignedToken(time.Now().Add(time.Hour)), restURL: restURL}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer platform-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/deployments/7/environments":
			f.listRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"id": 11, "type": "staging", "api_credentials": []any{
					map[string]any{"type": "rest", "url": "https://staging.example.com/cubejs-api/v1"},
				}},
				map[string]any{"id": 12, "type": "production", "api_credentials": []any{
					map[string]any{"type": "sql", "url": "postgres://ignored"},
					map[string]any{"type": "rest", "url": f.restURL},
				}},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens-for-meta-sync"):
			body, _ := io.ReadAll(r.Body)
			request := fakeTokenRequest{Path: r.URL.Path}
			_ = json.Unmarshal(body, &request.Body)
			f.tokenRequests = append(f.tokenRequests, request)
			if f.tokenStatus != 0 {
				w.WriteHeader(f.tokenStatus)
				_, _ = w.Write([]byte(`{"error":"no access"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"token": f.token}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return f
}

// unsignedToken has the form of a JSON Web Token with an `exp` claim. No test checks its signature.
func unsignedToken(expires time.Time) string {
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": expires.Unix()}).SignedString([]byte("cloud"))
	if err != nil {
		panic(err)
	}
	return token
}

func (s *TokenSuite) TestCloudFindsTheProductionEnvironment() {
	cloud := newFakeCloud("https://prod.example.com/cubejs-api/v1")
	defer cloud.Close()

	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{
		URL:             cloud.URL + "/",
		APIKey:          "platform-key",
		DeploymentID:    7,
		SecurityContext: `{"tenant": "a"}`,
	})
	s.Require().NoError(err)

	header, err := tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.Equal("Bearer "+cloud.token, header)

	s.Require().Len(cloud.tokenRequests, 1)
	s.Equal("/api/v1/deployments/7/environments/12/tokens-for-meta-sync", cloud.tokenRequests[0].Path)
	s.Equal(map[string]any{"tenant": "a"}, cloud.tokenRequests[0].Body["security_context"])
	s.ElementsMatch([]any{"meta-private", "meta"}, cloud.tokenRequests[0].Body["scopes"])

	// The token is used again while it is valid.
	_, err = tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.Len(cloud.tokenRequests, 1)

	apiURL, err := tokens.APIURL(s.T().Context())
	s.Require().NoError(err)
	s.Equal("https://prod.example.com/cubejs-api", apiURL)
	s.Equal(1, cloud.listRequests)
}

func (s *TokenSuite) TestCloudWithASetEnvironment() {
	cloud := newFakeCloud("https://prod.example.com/cubejs-api/v1")
	defer cloud.Close()

	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7, EnvironmentID: 11})
	s.Require().NoError(err)
	_, err = tokens.Authorization(s.T().Context())
	s.Require().NoError(err)
	s.Equal("/api/v1/deployments/7/environments/11/tokens-for-meta-sync", cloud.tokenRequests[0].Path)
	s.Equal(map[string]any{}, cloud.tokenRequests[0].Body["security_context"])
	s.Zero(cloud.listRequests, "a set environment needs no list request")

	apiURL, err := tokens.APIURL(s.T().Context())
	s.Require().NoError(err)
	s.Equal("https://staging.example.com/cubejs-api", apiURL)
}

func (s *TokenSuite) TestCloudAsksForANewTokenBeforeTheOldOneExpires() {
	cloud := newFakeCloud("")
	defer cloud.Close()
	cloud.token = unsignedToken(time.Now().Add(30 * time.Second))

	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7, EnvironmentID: 12})
	s.Require().NoError(err)
	for range 2 {
		_, err = tokens.Authorization(s.T().Context())
		s.Require().NoError(err)
	}
	s.Len(cloud.tokenRequests, 2)
}

func (s *TokenSuite) TestCloudFailures() {
	cloud := newFakeCloud("")
	defer cloud.Close()

	wrongKey, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "wrong", DeploymentID: 7})
	s.Require().NoError(err)
	_, err = wrongKey.Authorization(s.T().Context())
	var apiError *cube.APIError
	s.Require().ErrorAs(err, &apiError)
	s.Equal(http.StatusUnauthorized, apiError.StatusCode)

	cloud.tokenStatus = http.StatusForbidden
	refused, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7})
	s.Require().NoError(err)
	_, err = refused.Authorization(s.T().Context())
	s.Require().ErrorAs(err, &apiError)
	s.Equal("no access", apiError.Message)

	_, err = refused.APIURL(s.T().Context())
	s.Error(err, "the environment has no REST URL")

	for _, config := range []cube.CloudConfig{
		{APIKey: "k", DeploymentID: 1},
		{URL: cloud.URL, DeploymentID: 1},
		{URL: cloud.URL, APIKey: "k"},
		{URL: cloud.URL, APIKey: "k", DeploymentID: 1, SecurityContext: "[]"},
	} {
		_, err = cube.NewCloudTokenSource(config)
		s.Error(err)
	}
}

func (s *TokenSuite) TestCollectWithCubeCloudAddsTheModelExport() {
	server := cubetest.NewServer()
	defer server.Close()
	cloud := newFakeCloud(server.APIURL() + "/v1")
	defer cloud.Close()
	server.Authorization = "Bearer " + cloud.token
	server.ModelExport = []byte(`{"data":[{"name":"orders"}],"pagination":{"total":1,"offset":0,"limit":100}}`)

	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7})
	s.Require().NoError(err)
	apiURL, err := tokens.APIURL(s.T().Context())
	s.Require().NoError(err)
	client, err := cube.NewClient(apiURL, tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)
	s.True(client.Cloud())

	request, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	s.Equal(254, report.Members)
	s.Zero(report.MemberErrors)
	s.True(report.ModelExport)
	s.Empty(report.Warnings)
	s.Equal(server.ModelExport, request.ModelExport)
	s.Equal(1, server.Requests()[cubetest.BasePath+"/v1/model-export"])
}

func (s *TokenSuite) TestModelExportFailureIsAWarning() {
	server := cubetest.NewServer()
	defer server.Close()
	cloud := newFakeCloud(server.APIURL())
	defer cloud.Close()

	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7})
	s.Require().NoError(err)
	client, err := cube.NewClient(server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)

	// The fake answers /v1/model-export with status 404, as Cube Core does.
	request, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	s.Nil(request.ModelExport)
	s.False(report.ModelExport)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "model export")

	// A response with more entities than one page holds stays in the snapshot, with a warning.
	server.ModelExport = []byte(`{"data":[{"name":"orders"}],"pagination":{"total":5,"offset":0,"limit":1}}`)
	request, report, err = cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	s.Equal(server.ModelExport, request.ModelExport)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "1 of 5")
}

func (s *TokenSuite) TestClientChecksItsInput() {
	tokens, err := cube.NewStaticTokenSource("t")
	s.Require().NoError(err)

	client, err := cube.NewClient(" https://cube.example.com/cubejs-api/v1/ ", tokens)
	s.Require().NoError(err)
	s.Equal("https://cube.example.com/cubejs-api", client.APIURL())
	s.False(client.Cloud())

	for _, apiURL := range []string{"", "cube.example.com/cubejs-api", "ftp://cube.example.com"} {
		_, err = cube.NewClient(apiURL, tokens)
		s.Error(err, apiURL)
	}
	_, err = cube.NewClient("https://cube.example.com/cubejs-api", nil)
	s.Error(err)
}
