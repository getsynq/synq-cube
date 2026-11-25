// Package quality sends a snapshot of a Cube data model to the Coalesce Quality ingest API.
package quality

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	ingestcubev1grpc "buf.build/gen/go/getsynq/api/grpc/go/synq/ingest/cube/v1/cubev1grpc"
	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	qualityoauth "github.com/getsynq/quality-oauth-go"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// MaxSnapshotSize is the largest snapshot that one call can send.
	MaxSnapshotSize = 32 * 1024 * 1024
	// How many times a call is sent again after the API answers that it is unavailable.
	uploadRetries = 3
	// The time limit of one call.
	uploadTimeout = 2 * time.Minute
	// The wait before the first retry. Each retry doubles it.
	defaultRetryBackoff = 2 * time.Second
	// The user name of the token exchange. The integration token is the password.
	tokenExchangeUser = "synq"
)

// Uploader sends snapshots with the token of one Cube integration.
type Uploader struct {
	conn         *grpc.ClientConn
	client       ingestcubev1grpc.CubeServiceClient
	retryBackoff time.Duration
}

type options struct {
	tokenURL     string
	dialTarget   string
	dialOptions  []grpc.DialOption
	plaintext    *bool
	retryBackoff time.Duration
}

// Option changes how NewUploader connects.
type Option func(*options)

// WithTokenURL sets the URL of the token exchange. The default comes from the endpoint.
func WithTokenURL(tokenURL string) Option {
	return func(o *options) { o.tokenURL = tokenURL }
}

// WithDialOptions adds gRPC dial options and sets the gRPC target that is dialed, for a test that
// serves the API in the process. The endpoint of the target still names the API in messages.
func WithDialOptions(dialTarget string, dialOptions ...grpc.DialOption) Option {
	return func(o *options) {
		o.dialTarget = dialTarget
		o.dialOptions = append(o.dialOptions, dialOptions...)
	}
}

// WithPlaintext sets if the connection has no TLS. The default is TLS, and no TLS for localhost.
func WithPlaintext(plaintext bool) Option {
	return func(o *options) { o.plaintext = &plaintext }
}

// WithRetryBackoff sets the wait before the first retry.
func WithRetryBackoff(backoff time.Duration) Option {
	return func(o *options) { o.retryBackoff = backoff }
}

// NewUploader connects to the Coalesce Quality API of the target. token is the token of a Cube
// integration. Each call exchanges it for a short-lived access token when the last one expired.
func NewUploader(ctx context.Context, target qualityoauth.Target, token string, opts ...Option) (*Uploader, error) {
	if token == "" {
		return nil, fmt.Errorf("the Coalesce Quality token is not set: set %s to the token of the Cube integration", qualityoauth.TokenEnvVar)
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	o := &options{tokenURL: qualityoauth.OAuthTokenURL(target), dialTarget: target.Endpoint, retryBackoff: defaultRetryBackoff}
	for _, opt := range opts {
		opt(o)
	}
	plaintext := isLocal(target.Host)
	if o.plaintext != nil {
		plaintext = *o.plaintext
	}

	exchange := &exchangeTokenSource{ctx: context.WithoutCancel(ctx), tokenURL: o.tokenURL, token: token}
	// The first exchange runs here, so a wrong token fails before the upload starts.
	first, err := exchange.Token()
	if err != nil {
		return nil, fmt.Errorf("cannot get an access token for Coalesce Quality: %w", err)
	}
	perRPC := &accessToken{source: oauth2.ReuseTokenSource(first, exchange), secure: !plaintext}

	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if plaintext {
		transport = insecure.NewCredentials()
	}
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(perRPC),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(MaxSnapshotSize)),
	}
	if !plaintext {
		dialOptions = append(dialOptions, grpc.WithAuthority(target.Host))
	}
	dialOptions = append(dialOptions, o.dialOptions...)
	conn, err := grpc.NewClient(o.dialTarget, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the Coalesce Quality API: %w", err)
	}
	return &Uploader{conn: conn, client: ingestcubev1grpc.NewCubeServiceClient(conn), retryBackoff: o.retryBackoff}, nil
}

// Close closes the connection.
func (u *Uploader) Close() error {
	return u.conn.Close()
}

// Upload sends one snapshot. It sends the call again if the API answers that it is unavailable.
func (u *Uploader) Upload(ctx context.Context, request *ingestcubev1.IngestSnapshotRequest) error {
	if size := proto.Size(request); size > MaxSnapshotSize {
		return fmt.Errorf("the snapshot has %d bytes, and the limit of one upload is %d bytes", size, MaxSnapshotSize)
	}
	backoff := u.retryBackoff
	for attempt := 0; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
		_, err := u.client.IngestSnapshot(callCtx, request)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt >= uploadRetries || status.Code(err) != codes.Unavailable {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// exchangeTokenSource exchanges the integration token for a short-lived access token.
type exchangeTokenSource struct {
	// oauth2.TokenSource has no context parameter, so the source keeps one.
	ctx      context.Context //nolint:containedctx // oauth2.TokenSource.Token takes no context
	tokenURL string
	token    string
}

func (s *exchangeTokenSource) Token() (*oauth2.Token, error) {
	config := oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: s.tokenURL, AuthStyle: oauth2.AuthStyleInParams}}
	token, err := config.PasswordCredentialsToken(s.ctx, tokenExchangeUser, s.token)
	if err != nil {
		var retrieveError *oauth2.RetrieveError
		if errors.As(err, &retrieveError) && retrieveError.Response != nil {
			// The body of the response can repeat the request, so the error keeps only the status.
			return nil, fmt.Errorf("the token exchange at %s returned %s", s.tokenURL, retrieveError.Response.Status)
		}
		return nil, fmt.Errorf("the token exchange at %s failed: %w", s.tokenURL, err)
	}
	return token, nil
}

// accessToken puts the access token on each call.
type accessToken struct {
	source oauth2.TokenSource
	secure bool
}

func (a *accessToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	token, err := a.source.Token()
	if err != nil {
		return nil, err
	}
	return map[string]string{"authorization": token.Type() + " " + token.AccessToken}, nil
}

func (a *accessToken) RequireTransportSecurity() bool {
	return a.secure
}
