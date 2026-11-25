// Package cube reads the compiled data model of a Cube deployment from the Cube REST API and
// builds the snapshot that the Coalesce Quality ingest API takes.
//
// The package calls `/v1/meta` and `/v1/sql`, and `/v1/model-export` on Cube Cloud. Cube compiles
// a query for `/v1/sql` and does not run it. The package never calls `/v1/load`, so it reads no
// data rows.
package cube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultRequestTimeout is the time limit of one request to Cube.
	DefaultRequestTimeout = 60 * time.Second
	// DefaultRetries is how many times a request is sent again after a failure that can pass.
	DefaultRetries = 3
	// DefaultRetryBackoff is the wait before the first retry. Each retry doubles it.
	DefaultRetryBackoff = 500 * time.Millisecond

	// The text of the response that Cube sends while it still works on a request.
	continueWait = "Continue wait"
	// How many times a request is sent again after "Continue wait".
	maxContinueWaits = 60
	// The largest response body that is read.
	responseLimit = 256 << 20
	// The longest error text that is kept from a response that is not JSON.
	errorTextLimit = 500
)

// APIError is a response of Cube that is not a success.
type APIError struct {
	StatusCode int
	// Message is the `error` property of the response, or the start of the body.
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// retryable is true for a failure that a later request can pass.
func (e *APIError) retryable() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// Client calls the REST API of one Cube deployment.
type Client struct {
	apiURL         string
	tokens         TokenSource
	httpClient     *http.Client
	requestTimeout time.Duration
	retries        int
	retryBackoff   time.Duration
	// noPostSQL is true after Cube answered `POST /v1/sql` with status 404 or 405.
	noPostSQL atomic.Bool
}

// ClientOption changes a Client.
type ClientOption func(*Client)

// WithHTTPClient sets the HTTP client. The default is a client with no proxy change and no
// timeout of its own, as each request has a time limit.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) { c.httpClient = httpClient }
}

// WithRequestTimeout sets the time limit of one request.
func WithRequestTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) { c.requestTimeout = timeout }
}

// WithRetries sets how many times a request is sent again after status 5xx, status 429 or a
// network error.
func WithRetries(retries int) ClientOption {
	return func(c *Client) { c.retries = retries }
}

// WithRetryBackoff sets the wait before the first retry. Each retry doubles it.
func WithRetryBackoff(backoff time.Duration) ClientOption {
	return func(c *Client) { c.retryBackoff = backoff }
}

// NewClient returns a Client. apiURL is the base URL of the REST API with the base path
// (`https://cube.example.com/cubejs-api`). A `/v1` at the end is accepted and removed.
func NewClient(apiURL string, tokens TokenSource, options ...ClientOption) (*Client, error) {
	normalized := normalizeAPIURL(apiURL)
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("cube: the API URL %q is not an http or https URL", apiURL)
	}
	if tokens == nil {
		return nil, errors.New("cube: the token source is not set")
	}
	client := &Client{
		apiURL:         normalized,
		tokens:         tokens,
		httpClient:     &http.Client{},
		requestTimeout: DefaultRequestTimeout,
		retries:        DefaultRetries,
		retryBackoff:   DefaultRetryBackoff,
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

// APIURL returns the base URL of the REST API, without `/v1`.
func (c *Client) APIURL() string {
	return c.apiURL
}

// Cloud is true if the token source is a CloudTokenSource. Only Cube Cloud has `/v1/model-export`.
func (c *Client) Cloud() bool {
	_, ok := c.tokens.(*CloudTokenSource)
	return ok
}

// Meta returns the response body of `GET /v1/meta?extended`, as Cube sent it.
func (c *Client) Meta(ctx context.Context) ([]byte, error) {
	body, err := c.do(ctx, http.MethodGet, "/v1/meta?extended", nil)
	if err != nil {
		return nil, fmt.Errorf("cube: GET /v1/meta: %w", err)
	}
	return body, nil
}

// SQLQuery is a query for `/v1/sql`. It has measures or dimensions, never the two.
type SQLQuery struct {
	Measures   []string `json:"measures,omitempty"`
	Dimensions []string `json:"dimensions,omitempty"`
	// Ungrouped asks for the expression of the member with no aggregation. Cube then reads the
	// tables of the cube and not a pre-aggregation table.
	Ungrouped bool `json:"ungrouped,omitempty"`
	Limit     int  `json:"limit,omitempty"`
}

// SQLResult is the SQL that Cube generates for a query.
type SQLResult struct {
	// DataSource is the data source that Cube runs the query on (`default`).
	DataSource string
	// SQL is the SQL text in the dialect of the data source.
	SQL string
	// AliasNameToMember maps the name of each output column of the SQL (`orders__revenue`) to
	// its member (`orders.revenue`). Nil means that Cube did not report it.
	AliasNameToMember map[string]string
}

// SQL returns the SQL that Cube generates for the query, from `GET /v1/sql`. Cube compiles the
// query and does not run it.
func (c *Client) SQL(ctx context.Context, query SQLQuery) (*SQLResult, error) {
	encoded, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("cube: cannot write the query: %w", err)
	}
	body, err := c.do(ctx, http.MethodGet, "/v1/sql?query="+url.QueryEscape(string(encoded)), nil)
	if err != nil {
		return nil, err
	}
	return readSQLResult(body)
}

// SQLOfMembers returns the SQL that Cube generates for a query of many members, from
// `POST /v1/sql`. The query is in the request body, so the length limit of a URL does not apply.
//
// A Cube that does not serve `POST /v1/sql` answers with status 404 or 405. The client then
// sends this query and each later query with `GET /v1/sql`.
func (c *Client) SQLOfMembers(ctx context.Context, query SQLQuery) (*SQLResult, error) {
	if c.noPostSQL.Load() {
		return c.SQL(ctx, query)
	}
	encoded, err := json.Marshal(struct {
		Query SQLQuery `json:"query"`
	}{Query: query})
	if err != nil {
		return nil, fmt.Errorf("cube: cannot write the query: %w", err)
	}
	body, err := c.do(ctx, http.MethodPost, "/v1/sql", encoded)
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusNotFound || apiError.StatusCode == http.StatusMethodNotAllowed) {
			c.noPostSQL.Store(true)
			return c.SQL(ctx, query)
		}
		return nil, err
	}
	return readSQLResult(body)
}

func readSQLResult(body []byte) (*SQLResult, error) {
	var response struct {
		SQL struct {
			SQL               []json.RawMessage `json:"sql"`
			DataSource        string            `json:"dataSource"`
			AliasNameToMember json.RawMessage   `json:"aliasNameToMember"`
		} `json:"sql"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("cannot read the response of /v1/sql: %w", err)
	}
	if len(response.SQL.SQL) == 0 {
		return nil, errors.New("the response of /v1/sql has no SQL")
	}
	var text string
	if err := json.Unmarshal(response.SQL.SQL[0], &text); err != nil || text == "" {
		return nil, errors.New("the response of /v1/sql has no SQL text")
	}
	result := &SQLResult{DataSource: response.SQL.DataSource, SQL: text}
	// A map that is not a map of texts is the same as no map: the caller then has no alias.
	var aliases map[string]string
	if len(response.SQL.AliasNameToMember) > 0 && json.Unmarshal(response.SQL.AliasNameToMember, &aliases) == nil {
		result.AliasNameToMember = aliases
	}
	return result, nil
}

// sqlRequestCounterKey is the context key of the counter of `/v1/sql` requests.
type sqlRequestCounterKey struct{}

// withSQLRequestCounter returns a context that counts each `/v1/sql` request that is sent with
// it, also a request that is sent again.
func withSQLRequestCounter(ctx context.Context, counter *atomic.Int64) context.Context {
	return context.WithValue(ctx, sqlRequestCounterKey{}, counter)
}

// ModelExport returns the response body of `POST /v1/model-export` of the Cube Cloud Metadata
// API. Cube Core answers with status 404.
func (c *Client) ModelExport(ctx context.Context) ([]byte, error) {
	body, err := c.do(ctx, http.MethodPost, "/v1/model-export", []byte("{}"))
	if err != nil {
		return nil, fmt.Errorf("cube: POST /v1/model-export: %w", err)
	}
	return body, nil
}

// do sends a request and retries it after a failure that can pass.
//
// "Continue wait" is not a failure: Cube still works on the request, and the Cube docs tell a
// client to send the same request again until it gets a result. It has its own, larger limit and
// does not use up the retries.
func (c *Client) do(ctx context.Context, method string, path string, body []byte) ([]byte, error) {
	backoff := c.retryBackoff
	attempt, waits := 0, 0
	for {
		data, err := c.once(ctx, method, path, body)
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		wait := backoff
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Message == continueWait {
			waits++
			if waits > maxContinueWaits {
				return nil, err
			}
			wait = c.retryBackoff
		} else {
			if attempt >= c.retries || !canRetry(err) {
				return nil, err
			}
			attempt++
			backoff *= 2
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) once(ctx context.Context, method string, path string, body []byte) ([]byte, error) {
	authorization, err := c.tokens.Authorization(ctx)
	if err != nil {
		return nil, &permanentError{err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, reader)
	if err != nil {
		return nil, &permanentError{err: err}
	}
	if counter, ok := ctx.Value(sqlRequestCounterKey{}).(*atomic.Int64); ok && strings.HasPrefix(path, "/v1/sql") {
		counter.Add(1)
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit))
	if err != nil {
		return nil, err
	}
	message := errorMessage(data)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, &APIError{StatusCode: response.StatusCode, Message: message}
	}
	// Cube answers a request that it still works on with status 200 and this error.
	if message == continueWait {
		return nil, &APIError{StatusCode: response.StatusCode, Message: message}
	}
	return data, nil
}

// permanentError is a failure before the request left. A retry does not change it.
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func canRetry(err error) bool {
	var permanent *permanentError
	if errors.As(err, &permanent) {
		return false
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.retryable()
	}
	// A network error or the time limit of one request.
	return true
}

// errorMessage reads the `error` property of a response. For a body that is not a JSON object
// with that property and is not a success body, the caller uses the start of the body.
func errorMessage(data []byte) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err == nil {
		if len(body.Error) == 0 {
			if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
				return ""
			}
		} else {
			var text string
			if err := json.Unmarshal(body.Error, &text); err == nil {
				return text
			}
			return string(body.Error)
		}
	}
	text := strings.TrimSpace(string(data))
	if len(text) > errorTextLimit {
		text = text[:errorTextLimit]
	}
	return text
}

// normalizeAPIURL removes the slash and the `/v1` at the end of an API URL.
func normalizeAPIURL(apiURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	normalized = strings.TrimSuffix(normalized, "/v1")
	return strings.TrimRight(normalized, "/")
}
