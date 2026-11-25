package cube

import "time"

// Snapshot is the data model of a Cube deployment, as Collect read it.
//
// The fields are the fields of the message `synq.ingest.cube.v1.IngestSnapshotRequest`, without
// the fields that name the uploader. Snapshot is a plain struct, and this package imports no
// generated proto package: a program that has its own generated copy of that message can import
// this package. The caller builds the message from the Snapshot.
type Snapshot struct {
	// Meta is the response body of `GET /v1/meta?extended`, as Cube returned it (JSON).
	Meta []byte
	// MemberSQL is the SQL that Cube generates for each measure and each dimension, in the
	// order of the data model.
	MemberSQL []MemberSQL
	// ModelExport is the response body of `POST /v1/model-export` of the Cube Cloud Metadata
	// API (JSON). Nil means that the snapshot has no model export.
	ModelExport []byte
	// CollectedAt is the time at which the snapshot was read from Cube.
	CollectedAt time.Time
	// CubeVersion is the version of Cube. Empty means that the caller did not give it.
	CubeVersion string
	// SecurityContextLabel is a name for the security context of the token. Empty means that
	// the caller did not give it.
	SecurityContextLabel string
	// DataSources says what each data source of the deployment is. Nil means that it is not
	// known. Filter.Apply keeps the list as it is.
	DataSources []DataSource
}

// MemberSQL is the SQL that Cube generates for a query of one member.
//
// Error says which of the two results the member has. If Error is empty, Cube generated SQL and
// the SQL field holds it. If Error is not empty, Cube generated no SQL, and SQL and DataSource
// are empty.
type MemberSQL struct {
	// Member is the member with its cube or view (`orders.revenue`).
	Member string
	// DataSource is the data source that Cube runs the query on (`default`). Empty means that
	// Cube did not report it.
	DataSource string
	// SQL is the SQL text, in the dialect of the data source.
	SQL string
	// Error is the error text, if Cube did not generate SQL for the member. Collect never sets
	// an empty error text for a member that failed.
	Error string
	// Ungrouped is true if the query had `"ungrouped": true`. An ungrouped query reads the
	// tables of the cube. A grouped query can read a pre-aggregation table.
	Ungrouped bool
}
