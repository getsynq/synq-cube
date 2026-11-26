// Package cubetest is a fake Cube REST API for tests. It serves a recorded response of
// `/v1/meta?extended` and answers `/v1/sql` from the recorded SQL of each member. It answers a
// query of many members with one statement that it builds from the recorded SQL of each member.
// The recording is from a sample project on Cube Core.
package cubetest

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// ErrorMembers gives the error text of members for which no query compiles. A query of many
	// members that holds one of them does not compile.
	ErrorMembers map[string]string
	// GroupedOnlyMembers lists members for which only a query without `ungrouped` compiles.
	GroupedOnlyMembers map[string]bool
	// NoAliasMembers lists members that are not in `aliasNameToMember` of the response of a
	// query of many members. The response of a query of the member alone has it.
	NoAliasMembers map[string]bool
	// NoAliases makes each response have no `aliasNameToMember`.
	NoAliases bool
	// NoPostSQL makes `POST /v1/sql` answer with status 404, as a Cube that serves only the GET
	// form.
	NoPostSQL bool
	// MaxURLLength is the longest URL of a GET request. A longer URL gets status 431. Zero
	// means no limit.
	MaxURLLength int
	// ContinueWaitMembers lists members whose first request gets the "Continue wait" response.
	ContinueWaitMembers map[string]bool
	// UnavailableMembers lists members whose first request gets status 503.
	UnavailableMembers map[string]bool
	// ModelExport is the response of `POST /v1/model-export`. Nil means status 404, as on Cube Core.
	ModelExport []byte
	// DataSources is the response of `GET /v1/data-sources`. Nil means status 404, as on Cube Core.
	DataSources []byte
	// SQLDelay is how long each `/v1/sql` request takes.
	SQLDelay time.Duration

	members map[string]Member

	mu        sync.Mutex
	queries   []SQLRequest
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

// SQLRequest is one `/v1/sql` request that the fake got.
type SQLRequest struct {
	// Method is GET or POST.
	Method string
	// Members holds the measures or the dimensions of the query.
	Members []string
	// Ungrouped is the `ungrouped` property of the query.
	Ungrouped bool
}

// SQLRequests returns each `/v1/sql` request with a query that the fake could read, in the
// order of arrival.
func (s *Server) SQLRequests() []SQLRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SQLRequest(nil), s.queries...)
}

// Alias returns the name of the output column of a member in a statement of the fake
// (`orders__revenue` for `orders.revenue`), as Cube names it.
func Alias(member string) string {
	return strings.ReplaceAll(member, ".", "__")
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
		if s.MaxURLLength > 0 && len(r.URL.RequestURI()) > s.MaxURLLength {
			writeError(w, http.StatusRequestHeaderFieldsTooLarge, "Request Header Fields Too Large")
			return
		}
		s.serveSQL(w, r, []byte(r.URL.Query().Get("query")))
	case r.Method == http.MethodPost && r.URL.Path == BasePath+"/v1/sql" && !s.NoPostSQL:
		var body struct {
			Query json.RawMessage `json:"query"`
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			writeError(w, http.StatusBadRequest, "Query param is required")
			return
		}
		s.serveSQL(w, r, body.Query)
	case r.Method == http.MethodGet && r.URL.Path == BasePath+"/v1/data-sources":
		if s.DataSources == nil {
			writeError(w, http.StatusNotFound, "Not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.DataSources)
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

func (s *Server) serveSQL(w http.ResponseWriter, r *http.Request, queryText []byte) {
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
	if err := json.Unmarshal(queryText, &query); err != nil {
		writeError(w, http.StatusBadRequest, "Query param is required")
		return
	}
	if len(query.Measures) > 0 && len(query.Dimensions) > 0 {
		writeError(w, http.StatusBadRequest, "the fake takes a query of measures or a query of dimensions")
		return
	}
	names, wantKind := query.Dimensions, "dimension"
	if len(query.Measures) > 0 {
		names, wantKind = query.Measures, "measure"
	}
	if len(names) == 0 || query.Limit != 1 {
		writeError(w, http.StatusBadRequest, "the fake takes a query of one member or more with limit 1")
		return
	}
	s.mu.Lock()
	s.queries = append(s.queries, SQLRequest{Method: r.Method, Members: names, Ungrouped: query.Ungrouped})
	s.mu.Unlock()

	members := make([]Member, 0, len(names))
	for _, name := range names {
		member, found := s.members[name]
		if !found || member.Kind != wantKind {
			writeError(w, http.StatusBadRequest, "'"+name+"' not found")
			return
		}
		if member.Cube != members0(members, member).Cube {
			writeError(w, http.StatusBadRequest, "the fake takes a query of members of one cube or of one view")
			return
		}
		members = append(members, member)
	}

	// The first request that holds a member is the first request of each member of the query.
	continueWait, unavailable := false, false
	s.mu.Lock()
	for _, name := range names {
		first := !s.firstSeen[name]
		s.firstSeen[name] = true
		continueWait = continueWait || (first && s.ContinueWaitMembers[name])
		unavailable = unavailable || (first && s.UnavailableMembers[name])
	}
	s.mu.Unlock()
	if continueWait {
		writeError(w, http.StatusOK, "Continue wait")
		return
	}
	if unavailable {
		writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	for _, name := range names {
		if text, broken := s.ErrorMembers[name]; broken {
			writeError(w, http.StatusBadRequest, text)
			return
		}
	}
	for _, name := range names {
		if query.Ungrouped && s.GroupedOnlyMembers[name] {
			writeError(w, http.StatusBadRequest, "Ungrouped query is not supported for "+name)
			return
		}
	}

	// The SQL of one member is the recording. The SQL of many members is one text with the
	// output column and the recorded SQL of each member.
	text := members[0].SQL
	if len(members) > 1 {
		parts := make([]string, 0, len(members))
		for _, member := range members {
			parts = append(parts, "-- \""+Alias(member.Member)+"\"\n"+member.SQL)
		}
		text = strings.Join(parts, "\n")
	}
	sql := map[string]any{
		"sql":             []any{text, []any{}},
		"dataSource":      members[0].DataSource,
		"external":        false,
		"preAggregations": []any{},
	}
	if !s.NoAliases {
		aliases := map[string]string{}
		for _, member := range members {
			if len(members) == 1 || !s.NoAliasMembers[member.Member] {
				aliases[Alias(member.Member)] = member.Member
			}
		}
		sql["aliasNameToMember"] = aliases
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sql": sql})
}

// members0 returns the first member of the list, or the fallback for an empty list.
func members0(members []Member, fallback Member) Member {
	if len(members) > 0 {
		return members[0]
	}
	return fallback
}

func writeError(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
}

// PublicMetaExtended returns the recorded response of `/v1/meta?extended` without the cubes, the
// views and the members that are not public. It is the response of a Cube deployment in
// production. The recording is from a Cube in development mode, which returns them.
func PublicMetaExtended() []byte {
	var meta map[string]json.RawMessage
	mustUnmarshal(metaExtended, &meta)
	var entries []map[string]json.RawMessage
	mustUnmarshal(meta["cubes"], &entries)
	kept := make([]map[string]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if hidden(entry) {
			continue
		}
		for _, list := range []string{"measures", "dimensions", "segments"} {
			var members []map[string]json.RawMessage
			mustUnmarshal(entry[list], &members)
			public := make([]map[string]json.RawMessage, 0, len(members))
			for _, member := range members {
				if !hidden(member) {
					public = append(public, member)
				}
			}
			entry[list] = mustMarshal(public)
		}
		kept = append(kept, entry)
	}
	meta["cubes"] = mustMarshal(kept)
	return mustMarshal(meta)
}

// ModelExport returns a response of `POST /v1/model-export` for the recording: each cube, each
// view and each member, also those that are not public. The properties have the names of the
// Cube Cloud Metadata API: a member has its name with no cube, and `agg_type` and
// `is_primary_key` are in snake case.
func ModelExport() []byte {
	var meta struct {
		Cubes []map[string]json.RawMessage `json:"cubes"`
	}
	mustUnmarshal(metaExtended, &meta)
	entities := make([]map[string]any, 0, len(meta.Cubes))
	for _, entry := range meta.Cubes {
		entity := map[string]any{
			"type":        entry["type"],
			"name":        entry["name"],
			"public":      !hidden(entry),
			"title":       entry["title"],
			"description": entry["description"],
		}
		for _, list := range []string{"measures", "dimensions", "segments"} {
			var members []map[string]json.RawMessage
			mustUnmarshal(entry[list], &members)
			exported := make([]map[string]any, 0, len(members))
			for _, member := range members {
				var name, sql string
				mustUnmarshal(member["name"], &name)
				_, short, _ := strings.Cut(name, ".")
				item := map[string]any{"name": short, "public": !hidden(member), "title": member["title"], "type": member["type"]}
				if raw, found := member["sql"]; found {
					mustUnmarshal(raw, &sql)
					item["sql"] = strings.Trim(sql, "`")
				}
				if raw, found := member["aggType"]; found {
					item["agg_type"] = raw
				}
				if raw, found := member["primaryKey"]; found {
					item["is_primary_key"] = raw
				}
				exported = append(exported, item)
			}
			entity[list] = exported
		}
		entities = append(entities, entity)
	}
	return mustMarshal(map[string]any{
		"data":       entities,
		"pagination": map[string]int{"total": len(entities), "offset": 0, "limit": len(entities)},
	})
}

// hidden is true for an entry of the recording that is not public.
func hidden(entry map[string]json.RawMessage) bool {
	return string(entry["public"]) == "false" || string(entry["isVisible"]) == "false"
}

func mustUnmarshal(data []byte, target any) {
	if len(data) == 0 {
		return
	}
	if err := json.Unmarshal(data, target); err != nil {
		panic(err)
	}
}

func mustMarshal(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
