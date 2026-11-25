// Package cubetest is a fake Cube REST API for tests. It serves a recorded response of
// `/v1/meta?extended` and answers `/v1/sql` from the recorded SQL of each member. The recording
// is from a sample project on Cube Core.
package cubetest

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed testdata/meta-extended.json
var metaExtended []byte

//go:embed testdata/members.json
var membersJSON []byte

// BasePath is the base path of the REST API on the fake.
const BasePath = "/cubejs-api"

// Member is the recorded SQL of one member.
type Member struct {
	Cube       string `json:"cube"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
	Member     string `json:"member"`
	DataSource string `json:"data_source"`
	SQL        string `json:"sql"`
}

// MetaExtended returns the recorded response of `/v1/meta?extended`.
func MetaExtended() []byte {
	return append([]byte(nil), metaExtended...)
}

// Members returns the recorded SQL of each member, in the order of the recording.
func Members() []Member {
	var members []Member
	if err := json.Unmarshal(membersJSON, &members); err != nil {
		panic(err)
	}
	return members
}

// Server is a fake Cube. Set the exported fields before the first request.
type Server struct {
	*httptest.Server

	// Meta is the response of `/v1/meta`. The default is the recording.
	Meta []byte
	// MetaStatus is the status of `/v1/meta`. Zero means 200.
	MetaStatus int
	// Authorization is the `Authorization` header that a request must have. Empty means no check.
	Authorization string
	// ErrorMembers gives the error text of members for which no query compiles.
	ErrorMembers map[string]string
	// GroupedOnlyMembers lists members for which only a query without `ungrouped` compiles.
	GroupedOnlyMembers map[string]bool
	// ContinueWaitMembers lists members whose first request gets the "Continue wait" response.
	ContinueWaitMembers map[string]bool
	// UnavailableMembers lists members whose first request gets status 503.
	UnavailableMembers map[string]bool
	// ModelExport is the response of `POST /v1/model-export`. Nil means status 404, as on Cube Core.
	ModelExport []byte
	// SQLDelay is how long each `/v1/sql` request takes.
	SQLDelay time.Duration

	members map[string]Member

	mu        sync.Mutex
	paths     map[string]int
	firstSeen map[string]bool
	inFlight  atomic.Int64
	maxFlight atomic.Int64
}

// NewServer starts a fake Cube. The caller closes it.
func NewServer() *Server {
	s := &Server{
		Meta:      MetaExtended(),
		members:   map[string]Member{},
		paths:     map[string]int{},
		firstSeen: map[string]bool{},
	}
	for _, member := range Members() {
		s.members[member.Member] = member
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// APIURL returns the base URL of the REST API, with the base path.
func (s *Server) APIURL() string {
	return s.URL + BasePath
}

// Requests returns the count of requests for each path.
func (s *Server) Requests() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.paths))
	for path, count := range s.paths {
		out[path] = count
	}
	return out
}

// MaxConcurrentSQL returns the largest count of `/v1/sql` requests that ran at the same time.
func (s *Server) MaxConcurrentSQL() int {
	return int(s.maxFlight.Load())
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths[r.URL.Path]++
	s.mu.Unlock()

	if s.Authorization != "" && r.Header.Get("Authorization") != s.Authorization {
		writeError(w, http.StatusForbidden, "Invalid token")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == BasePath+"/v1/meta":
		if _, extended := r.URL.Query()["extended"]; !extended {
			writeError(w, http.StatusBadRequest, "the fake serves only /v1/meta?extended")
			return
		}
		if s.MetaStatus != 0 && s.MetaStatus != http.StatusOK {
			writeError(w, s.MetaStatus, "meta is not available")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.Meta)
	case r.Method == http.MethodGet && r.URL.Path == BasePath+"/v1/sql":
		s.serveSQL(w, r)
	case r.Method == http.MethodPost && r.URL.Path == BasePath+"/v1/model-export":
		if s.ModelExport == nil {
			writeError(w, http.StatusNotFound, "Not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.ModelExport)
	default:
		writeError(w, http.StatusNotFound, "Not found")
	}
}

func (s *Server) serveSQL(w http.ResponseWriter, r *http.Request) {
	current := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		seen := s.maxFlight.Load()
		if current <= seen || s.maxFlight.CompareAndSwap(seen, current) {
			break
		}
	}
	if s.SQLDelay > 0 {
		time.Sleep(s.SQLDelay)
	}

	var query struct {
		Measures   []string `json:"measures"`
		Dimensions []string `json:"dimensions"`
		Ungrouped  bool     `json:"ungrouped"`
		Limit      int      `json:"limit"`
	}
	if err := json.Unmarshal([]byte(r.URL.Query().Get("query")), &query); err != nil {
		writeError(w, http.StatusBadRequest, "Query param is required")
		return
	}
	names := append(append([]string{}, query.Measures...), query.Dimensions...)
	if len(names) != 1 || query.Limit != 1 {
		writeError(w, http.StatusBadRequest, "the fake takes a query of one member with limit 1")
		return
	}
	name := names[0]
	member, found := s.members[name]
	wantKind := "dimension"
	if len(query.Measures) == 1 {
		wantKind = "measure"
	}
	if !found || member.Kind != wantKind {
		writeError(w, http.StatusBadRequest, "'"+name+"' not found")
		return
	}

	s.mu.Lock()
	first := !s.firstSeen[name]
	s.firstSeen[name] = true
	s.mu.Unlock()
	if first && s.ContinueWaitMembers[name] {
		writeError(w, http.StatusOK, "Continue wait")
		return
	}
	if first && s.UnavailableMembers[name] {
		writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	if text, broken := s.ErrorMembers[name]; broken {
		writeError(w, http.StatusBadRequest, text)
		return
	}
	if query.Ungrouped && s.GroupedOnlyMembers[name] {
		writeError(w, http.StatusBadRequest, "Ungrouped query is not supported for "+name)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sql": map[string]any{
			"sql":             []any{member.SQL, []any{}},
			"dataSource":      member.DataSource,
			"external":        false,
			"preAggregations": []any{},
		},
	})
}

func writeError(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
}
