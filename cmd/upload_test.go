package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ingestcubev1grpc "buf.build/gen/go/getsynq/api/grpc/go/synq/ingest/cube/v1/cubev1grpc"
	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/getsynq/synq-cube/cmd"
	"github.com/getsynq/synq-cube/cube/cubetest"
	"github.com/getsynq/synq-cube/internal/quality"
)

const (
	integrationToken = "integration-token"
	accessToken      = "short-lived-access-token"
	apiSecret        = "the-api-secret"
)

// fakeCubeService is the Coalesce Quality ingest API, served in the process.
type fakeCubeService struct {
	ingestcubev1grpc.UnimplementedCubeServiceServer //nolint:forbidigo // test fake serving IngestSnapshot

	mu             sync.Mutex
	requests       []*ingestcubev1.IngestSnapshotRequest
	authorizations []string
	unavailable    int
	failWith       codes.Code
}

func (f *fakeCubeService) IngestSnapshot(
	ctx context.Context,
	request *ingestcubev1.IngestSnapshotRequest,
) (*ingestcubev1.IngestSnapshotResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	md, _ := metadata.FromIncomingContext(ctx)
	f.authorizations = append(f.authorizations, md.Get("authorization")...)
	if f.unavailable > 0 {
		f.unavailable--
		return nil, status.Error(codes.Unavailable, "try again")
	}
	if f.failWith != codes.OK {
		return nil, status.Error(f.failWith, "refused")
	}
	f.requests = append(f.requests, request)
	return &ingestcubev1.IngestSnapshotResponse{}, nil
}

type UploadSuite struct {
	suite.Suite

	cube    *cubetest.Server
	service *fakeCubeService
	// tokenExchanges holds the form of each request to the token endpoint.
	tokenExchanges []map[string]string
	tokenStatus    int
	options        []quality.Option
	stdin          io.Reader
	stdout         bytes.Buffer
	stderr         bytes.Buffer
}

func TestUploadSuite(t *testing.T) {
	suite.Run(t, new(UploadSuite))
}

func (s *UploadSuite) SetupTest() {
	t := s.T()
	s.stdin = nil
	s.stdout.Reset()
	s.stderr.Reset()
	s.tokenExchanges = nil
	s.tokenStatus = http.StatusOK

	// No variable of the machine that runs the test reaches the command.
	for _, name := range []string{
		"CUBE_API_URL", "CUBE_API_SECRET", "CUBE_API_TOKEN", "CUBE_SECURITY_CONTEXT", "CUBE_SECURITY_CONTEXT_LABEL", "CUBE_VERSION",
		"CUBE_CLOUD_URL", "CUBE_CLOUD_API_KEY", "CUBE_CLOUD_DEPLOYMENT_ID", "CUBE_CLOUD_ENVIRONMENT_ID",
		"CUBE_INCLUDE", "CUBE_EXCLUDE", "CUBE_SKIP_HIDDEN", "CUBE_SKIP_MEMBER_SQL", "CUBE_SKIP_MODEL_EXPORT",
		"QUALITY_TOKEN", "QUALITY_API_ENDPOINT", "QUALITY_REGION", "SYNQ_TOKEN", "SYNQ_API_ENDPOINT", "SYNQ_REGION",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("QUALITY_HOME", t.TempDir())

	s.cube = cubetest.NewServer()
	t.Cleanup(s.cube.Close)

	s.service = &fakeCubeService{}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(quality.MaxSnapshotSize))
	ingestcubev1grpc.RegisterCubeServiceServer(server, s.service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.tokenExchanges = append(s.tokenExchanges, map[string]string{
			"path":       r.URL.Path,
			"grant_type": r.PostForm.Get("grant_type"),
			"username":   r.PostForm.Get("username"),
			"password":   r.PostForm.Get("password"),
		})
		w.Header().Set("Content-Type", "application/json")
		if s.tokenStatus != http.StatusOK {
			w.WriteHeader(s.tokenStatus)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(tokenEndpoint.Close)

	s.options = []quality.Option{
		quality.WithTokenURL(tokenEndpoint.URL + "/oauth2/token"),
		quality.WithPlaintext(true),
		quality.WithRetryBackoff(time.Millisecond),
		quality.WithDialOptions("passthrough:///bufconn", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		})),
	}
}

func (s *UploadSuite) run(args ...string) error {
	root := cmd.NewRootCommand(cmd.Deps{Stdin: s.stdin, Stdout: &s.stdout, Stderr: &s.stderr, QualityOptions: s.options})
	root.SetArgs(args)
	return root.ExecuteContext(s.T().Context())
}

func (s *UploadSuite) TestUploadSendsTheSnapshot() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("CUBE_SECURITY_CONTEXT", `{"tenant_id": 7}`)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	s.cube.ErrorMembers = map[string]string{"customers.lifetime_revenue": "Can't find join path"}

	err := s.run("upload", "--endpoint", "quality.example.com:443", "--security-context-label", "full-read", "--concurrency", "2")
	s.Require().NoError(err)

	// The integration token is exchanged one time, and the call has the access token.
	s.Require().Len(s.tokenExchanges, 1)
	s.Equal(map[string]string{
		"path":       "/oauth2/token",
		"grant_type": "password",
		"username":   "synq",
		"password":   integrationToken,
	}, s.tokenExchanges[0])
	s.Equal([]string{"Bearer " + accessToken}, s.service.authorizations)

	s.Require().Len(s.service.requests, 1)
	request := s.service.requests[0]
	s.Equal(cubetest.MetaExtended(), request.GetMeta())
	s.Len(request.GetMemberSql(), 254)
	s.Equal("full-read", request.GetSecurityContextLabel())
	s.Nil(request.CubeVersion)
	s.Nil(request.ModelExport)
	s.NotEmpty(request.GetUploaderVersion())
	s.WithinDuration(time.Now(), request.GetCollectedAt().AsTime(), time.Minute)
	withError := 0
	for _, member := range request.GetMemberSql() {
		if member.Error != nil {
			withError++
			s.Equal("customers.lifetime_revenue", member.GetMember())
			continue
		}
		s.NotEmpty(member.GetSql(), member.GetMember())
		s.NotEmpty(member.GetDataSource(), member.GetMember())
	}
	s.Equal(1, withError)
	s.LessOrEqual(s.cube.MaxConcurrentSQL(), 2)

	out := s.stdout.String()
	s.Contains(out, "Cubes: 14\n")
	s.Contains(out, "Views: 6\n")
	s.Contains(out, "Members: 254\n")
	s.Contains(out, "Members with an error: 1\n")
	s.Contains(out, "Sent the snapshot to Coalesce Quality, quality.example.com:443")
	s.Contains(s.stderr.String(), "warning: customers.lifetime_revenue")
	s.NotContains(out+s.stderr.String(), apiSecret)
	s.NotContains(out+s.stderr.String(), integrationToken)
}

func (s *UploadSuite) TestTheCubeTokenIsSignedWithTheAPISecret() {
	// The fake Cube accepts only the header that it is told, so read the header from a request.
	var authorization string
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"cubes":[]}`))
	}))
	defer probe.Close()
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	err := s.run("upload", "--cube-api-url", probe.URL+"/cubejs-api", "--cube-security-context", `{"tenant_id": 7}`, "--region", "eu")
	s.Require().NoError(err)

	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(authorization, claims, func(*jwt.Token) (any, error) { return []byte(apiSecret), nil })
	s.Require().NoError(err)
	s.EqualValues(7, claims["tenant_id"])
	s.Contains(s.stdout.String(), "Cubes: 0\n")
	s.Require().Len(s.service.requests, 1)
}

func (s *UploadSuite) TestAReadyTokenWinsOverTheAPISecret() {
	s.cube.Authorization = "ready-token"
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("CUBE_API_TOKEN", "ready-token")
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	s.Require().NoError(s.run("upload"))
	s.Require().Len(s.service.requests, 1)
	s.Len(s.service.requests[0].GetMemberSql(), 254)
}

func (s *UploadSuite) TestDryRunWritesTheSnapshotAndSendsNothing() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("CUBE_VERSION", "1.8.30")
	output := filepath.Join(s.T().TempDir(), "snapshot.json")

	// No token of Coalesce Quality is set: a dry run does not need it.
	err := s.run("upload", "--dry-run", "--output", output)
	s.Require().NoError(err)

	s.Empty(s.tokenExchanges)
	s.Empty(s.service.requests)
	s.Contains(s.stdout.String(), "Members: 254\n")
	s.Contains(s.stdout.String(), "Dry run: nothing was sent")

	data, err := os.ReadFile(output)
	s.Require().NoError(err)
	snapshot := &ingestcubev1.IngestSnapshotRequest{}
	s.Require().NoError(protojson.Unmarshal(data, snapshot))
	s.Equal(cubetest.MetaExtended(), snapshot.GetMeta())
	s.Len(snapshot.GetMemberSql(), 254)
	s.Equal("1.8.30", snapshot.GetCubeVersion())
	s.NotContains(string(data), apiSecret)

	s.Error(s.run("upload", "--dry-run"), "a dry run needs --output")
}

func (s *UploadSuite) TestMetaFailureFailsTheCommand() {
	s.cube.MetaStatus = http.StatusForbidden
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	err := s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot read the data model")
	s.Empty(s.service.requests)
}

func (s *UploadSuite) TestUploadIsSentAgainWhenTheAPIIsUnavailable() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	s.service.unavailable = 3

	s.Require().NoError(s.run("upload"))
	s.Len(s.service.requests, 1)
	s.Len(s.service.authorizations, 4)
	s.Len(s.tokenExchanges, 1)
}

func (s *UploadSuite) TestUploadFailureFailsTheCommand() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	s.service.unavailable = 4
	err := s.run("upload")
	s.Require().Error(err)
	s.Equal(codes.Unavailable, status.Code(err))
	s.Empty(s.service.requests)

	s.service.unavailable = 0
	s.service.failWith = codes.PermissionDenied
	s.service.authorizations = nil
	err = s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "the upload to Coalesce Quality")
	s.Len(s.service.authorizations, 1, "a refused call is not sent again")
}

func (s *UploadSuite) TestAWrongQualityTokenFailsBeforeTheRequestsToCube() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", "wrong")
	s.tokenStatus = http.StatusUnauthorized

	err := s.run("upload")
	s.Require().Error(err)
	s.NotContains(err.Error(), "wrong")
	s.Empty(s.cube.Requests())

	s.T().Setenv("QUALITY_TOKEN", "")
	err = s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "QUALITY_TOKEN")
}

func (s *UploadSuite) TestMissingCubeSettings() {
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	err := s.run("upload", "--cube-api-url", s.cube.APIURL())
	s.Require().Error(err)
	s.Contains(err.Error(), "CUBE_API_SECRET")

	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	err = s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "CUBE_API_URL")

	s.Error(s.run("upload", "--cube-api-url", s.cube.APIURL(), "--concurrency", "0"))
	s.Error(s.run("upload", "--cube-api-url", s.cube.APIURL(), "--region", "nowhere"))
	s.Error(s.run("upload", "--cube-api-url", s.cube.APIURL(), "--deployment", "production"), "the integration names the deployment")
}

func (s *UploadSuite) TestVersion() {
	s.Require().NoError(s.run("--version"))
	s.Contains(s.stdout.String(), "synq-cube version ")
}

func (s *UploadSuite) TestHelpUsesThePublicNames() {
	s.Require().NoError(s.run("upload", "--help"))
	help := s.stdout.String()
	s.Contains(help, "QUALITY_TOKEN")
	s.Contains(help, "Coalesce Quality")
	for _, old := range []string{"SYNQ", "Synq", "--synq-"} {
		s.NotContains(help, old)
	}
}
