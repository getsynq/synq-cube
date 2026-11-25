package quality

import (
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// collectFromTheFake collects a snapshot from the fake Cube.
func collectFromTheFake(t *testing.T, server *cubetest.Server, options cube.Options) *cube.Snapshot {
	t.Helper()
	tokens, err := cube.NewStaticTokenSource("the-token")
	require.NoError(t, err)
	client, err := cube.NewClient(server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	require.NoError(t, err)
	snapshot, _, err := cube.Collect(t.Context(), client, options)
	require.NoError(t, err)
	return snapshot
}

// The message of a collected snapshot passes the `buf.validate` rules of the proto file, which
// the API checks. The fake Cube gives each kind of result: statements of many members, a member
// that needs a query without `ungrouped`, a member with an error, and a member whose output
// column a response does not name.
func TestACollectedSnapshotIsAValidMessage(t *testing.T) {
	modelPath := "model"
	for name, test := range map[string]struct {
		prepare func(*cubetest.Server)
		options cube.Options
		version string
		time    string
	}{
		"each part": {
			prepare: func(server *cubetest.Server) {
				server.ErrorMembers = map[string]string{"customers.lifetime_revenue": "Can't find join path"}
				server.GroupedOnlyMembers = map[string]bool{"orders.status_revenue_rank": true}
				server.NoAliasMembers = map[string]bool{"orders.revenue": true}
			},
			options: cube.Options{
				CubeVersion:          "1.8.30",
				SecurityContextLabel: "full-read",
				DataSources: []cube.DataSource{
					{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "https://ab12345.eu-west-1:443"},
					{Name: "postgres"},
				},
				Git:       &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"},
				ModelPath: &modelPath,
			},
			version: "0.1.0",
			time:    "2026-03-01T00:00:00Z",
		},
		"no optional part, and a build with no stamps": {
			prepare: func(*cubetest.Server) {},
			// A git context with no URL is not sent.
			options: cube.Options{Git: &cube.GitContext{Branch: "main"}},
			time:    "unknown",
		},
		"a Cube that names no output column": {
			prepare: func(server *cubetest.Server) { server.NoAliases = true },
			options: cube.Options{BatchSize: 50},
		},
		"one request for each member": {
			prepare: func(server *cubetest.Server) {
				server.ErrorMembers = map[string]string{"customers.lifetime_revenue": ""}
			},
			options: cube.Options{BatchSize: 1},
		},
		"no SQL": {
			prepare: func(*cubetest.Server) {},
			options: cube.Options{Filter: cube.Filter{SkipMemberSQL: true, SkipHidden: true}},
		},
	} {
		server := cubetest.NewServer()
		test.prepare(server)
		snapshot := collectFromTheFake(t, server, test.options)
		server.Close()

		request := NewRequest(snapshot, test.version, test.time)
		require.NoError(t, protovalidate.Validate(request), name)

		// Each member of the data model is in one statement or in the errors, but for a
		// collection with no SQL.
		if !test.options.Filter.SkipMemberSQL {
			members := len(request.GetMemberErrors())
			for _, generated := range request.GetGeneratedSql() {
				members += len(generated.GetMembers())
				require.NotNil(t, generated.Ungrouped, name)
			}
			require.Equal(t, 254, members, name)
		}
		// The message of the snapshot file passes too.
		require.NoError(t, protovalidate.Validate(NewRequest(NewSnapshot(request), test.version, test.time)), name)
	}
}

// The rules are in the generated code, so the test above checks something: a message that
// breaks a rule does not pass.
func TestTheRulesOfTheMessageAreChecked(t *testing.T) {
	valid := func() *cube.Snapshot {
		return &cube.Snapshot{
			DataModel:    []byte(`{"cubes":[]}`),
			GeneratedSQL: []cube.GeneratedSQL{{SQL: "SELECT 1", Members: []cube.GeneratedSQLMember{{Member: "orders.count", Column: "orders__count"}}}},
			CollectedAt:  time.Now(),
		}
	}
	require.NoError(t, protovalidate.Validate(NewRequest(valid(), "0.1.0", "")))

	noColumn := valid()
	noColumn.GeneratedSQL[0].Members[0].Column = ""
	require.Error(t, protovalidate.Validate(NewRequest(noColumn, "0.1.0", "")))

	noTime := valid()
	noTime.CollectedAt = time.Time{}
	require.Error(t, protovalidate.Validate(NewRequest(noTime, "0.1.0", "")))

	emptyOptional := NewRequest(valid(), "0.1.0", "")
	empty := ""
	emptyOptional.CubeVersion = &empty
	require.Error(t, protovalidate.Validate(emptyOptional))
}
