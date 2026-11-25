package cube

import (
	"sort"
	"time"
)

// Snapshot is the data model of a Cube deployment, as Collect read it.
//
// The fields are the fields of the message `synq.ingest.cube.v1.IngestSnapshotRequest`, without
// the fields that name the uploader. Snapshot is a plain struct, and this package imports no
// generated proto package: a program that has its own generated copy of that message can import
// this package. The caller builds the message from the Snapshot.
type Snapshot struct {
	// DataModel is the response body of `GET /v1/meta?extended`, as Cube returned it (JSON).
	DataModel []byte
	// ModelExport is the response body of `POST /v1/model-export` of the Cube Cloud Metadata
	// API (JSON). Nil means that the snapshot has no model export.
	ModelExport []byte
	// GeneratedSQL is the SQL that Cube generates for the measures and the dimensions, in the
	// order of the data model. One entry is the SQL of one query: of many members of one cube
	// or of one view, or of one member. A member is in one entry at most.
	GeneratedSQL []GeneratedSQL
	// MemberErrors holds the members with no SQL, in the order of the data model: Cube returned
	// an error, or the response did not name the output column of the member. A member is in GeneratedSQL or in MemberErrors, never in the two.
	MemberErrors []MemberError
	// DataSources says what each data source of the deployment is, each name one time. Nil
	// means that it is not known. Filter.Apply keeps the list as it is.
	DataSources []DataSource
	// Git names the git repository that holds the files of the data model. Nil means that the
	// caller does not know it.
	Git *GitContext
	// ModelPath is the path of the directory of the data model, from the root of the
	// repository (`model`), with `/` between the parts. An empty text means the root of the
	// repository. Nil means that the caller does not know the directory.
	ModelPath *string
	// CollectedAt is the time at which the collection started, before the first request to
	// Cube.
	CollectedAt time.Time
	// CubeVersion is the version of Cube. Empty means that the caller did not give it.
	CubeVersion string
	// SecurityContextLabel is a name for the security context of the token. Empty means that
	// the caller did not give it.
	SecurityContextLabel string
}

// UsedDataSources returns the names of the data sources that the entries of GeneratedSQL use,
// in the order of their names.
func (s *Snapshot) UsedDataSources() []string {
	used := map[string]bool{}
	for _, generated := range s.GeneratedSQL {
		if generated.DataSource != "" {
			used[generated.DataSource] = true
		}
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GeneratedSQL is the SQL statement that Cube generates for one query. The query has one member
// or many members of one cube or of one view. All members are measures, or all members are
// dimensions. The statement has one output column for each member.
type GeneratedSQL struct {
	// SQL is the SQL text, in the dialect of the data source.
	SQL string
	// Members holds the members of the query, each with its output column. A filter can remove
	// a member from the list. The SQL text then still has the output column of that member.
	Members []GeneratedSQLMember
	// DataSource is the data source that Cube runs the query on (`default`). Empty means that
	// Cube did not report it.
	DataSource string
	// Ungrouped is true if the query had `"ungrouped": true`. An ungrouped query reads the
	// tables of the cube. A grouped query can read a pre-aggregation table.
	Ungrouped bool
}

// GeneratedSQLMember is one member of a query, and where its value is in the statement.
type GeneratedSQLMember struct {
	// Member is the member with its cube or view (`orders.revenue`).
	Member string
	// Column is the name of the output column of the member in the statement
	// (`orders__revenue`). It is the key of the member in `sql.aliasNameToMember` of the
	// response of Cube, as Cube sent it. Collect never builds it from the name of the member.
	Column string
}

// MemberError is a member with no SQL.
type MemberError struct {
	// Member is the member with its cube or view (`orders.revenue`).
	Member string
	// Error says why there is no SQL: the error text of Cube, or what Collect could not read.
	// Collect never sets an empty text.
	Error string
}

// GitContext names a commit of a git repository.
type GitContext struct {
	// CloneURL is the URL of the repository (`https://github.com/example/analytics.git`). It
	// must have no user name and no password.
	CloneURL string
	// Branch is the name of the branch. Empty means that it is not known.
	Branch string
	// CommitSHA is the commit. Empty means that it is not known.
	CommitSHA string
}
