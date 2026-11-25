package quality

import (
	"reflect"
	"testing"
	"time"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/getsynq/synq-cube/cube"
)

// requireEachFieldSet fails if a field of the struct has its zero value. A new field of the
// struct then fails the test until the test gives it a value and asserts where it goes.
func requireEachFieldSet(t *testing.T, value any) {
	t.Helper()
	v := reflect.ValueOf(value)
	for i := range v.NumField() {
		require.Falsef(t, v.Field(i).IsZero(), "%s.%s has no value in this test", v.Type().Name(), v.Type().Field(i).Name)
	}
}

// requireEachFieldPopulated fails if a field of the message is not set.
func requireEachFieldPopulated(t *testing.T, message protoreflect.Message, except ...protoreflect.Name) {
	t.Helper()
	skip := map[protoreflect.Name]bool{}
	for _, name := range except {
		skip[name] = true
	}
	fields := message.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		if skip[field.Name()] {
			continue
		}
		require.Truef(t, message.Has(field), "%s.%s is not set", message.Descriptor().Name(), field.Name())
	}
}

func TestNewRequestMapsEachField(t *testing.T) {
	collectedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	withSQL := cube.MemberSQL{Member: "orders.revenue", DataSource: "default", SQL: "SELECT 1", Ungrouped: true}
	withError := cube.MemberSQL{Member: "orders.broken", Error: "no join path"}
	snapshot := cube.Snapshot{
		Meta:                 []byte(`{"cubes":[]}`),
		MemberSQL:            []cube.MemberSQL{withSQL, withError},
		ModelExport:          []byte(`{"data":[]}`),
		CollectedAt:          collectedAt,
		CubeVersion:          "1.8.30",
		SecurityContextLabel: "full-read",
		DataSources: []cube.DataSource{
			{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1", Project: "example-project"},
			{Name: "postgres", Type: "postgres"},
		},
	}
	requireEachFieldSet(t, snapshot)
	requireEachFieldSet(t, snapshot.DataSources[0])
	// The two members together give each field of MemberSQL a value.
	requireEachFieldSet(t, cube.MemberSQL{
		Member: withSQL.Member, DataSource: withSQL.DataSource, SQL: withSQL.SQL, Error: withError.Error, Ungrouped: withSQL.Ungrouped,
	})

	request := NewRequest(&snapshot, "0.1.0", "2026-03-01T00:00:00Z")

	requireEachFieldPopulated(t, request.ProtoReflect())
	require.Equal(t, snapshot.Meta, request.GetMeta())
	require.Equal(t, snapshot.ModelExport, request.GetModelExport())
	require.Equal(t, collectedAt, request.GetCollectedAt().AsTime())
	require.Equal(t, "1.8.30", request.GetCubeVersion())
	require.Equal(t, "full-read", request.GetSecurityContextLabel())
	require.Equal(t, "0.1.0", request.GetUploaderVersion())
	require.Equal(t, "2026-03-01T00:00:00Z", request.GetUploaderBuildTime())

	require.Len(t, request.GetMemberSql(), 2)
	first := request.GetMemberSql()[0]
	requireEachFieldPopulated(t, first.ProtoReflect(), "error")
	require.Equal(t, "orders.revenue", first.GetMember())
	require.Equal(t, "default", first.GetDataSource())
	require.Equal(t, "SELECT 1", first.GetSql())
	require.True(t, first.GetUngrouped())
	require.Nil(t, first.Error)

	second := request.GetMemberSql()[1]
	require.Equal(t, "orders.broken", second.GetMember())
	require.Equal(t, "no join path", second.GetError())
	require.Nil(t, second.Sql)
	require.Nil(t, second.DataSource)
	require.False(t, second.GetUngrouped())

	require.Len(t, request.GetDataSources(), 2)
	source := request.GetDataSources()[0]
	requireEachFieldPopulated(t, source.ProtoReflect())
	require.Equal(t, "default", source.GetName())
	require.Equal(t, "snowflake", source.GetType())
	require.Equal(t, "ANALYTICS", source.GetDatabase())
	require.Equal(t, "ab12345.eu-west-1", source.GetHost())
	require.Equal(t, "example-project", source.GetProject())
	// An empty field of a data source is an absent field of the message.
	bare := request.GetDataSources()[1]
	require.Equal(t, "postgres", bare.GetName())
	require.Nil(t, bare.Database)
	require.Nil(t, bare.Host)
	require.Nil(t, bare.Project)
}

func TestNewRequestKeepsAbsentFieldsAbsent(t *testing.T) {
	request := NewRequest(&cube.Snapshot{
		Meta:        []byte(`{"cubes":[]}`),
		MemberSQL:   []cube.MemberSQL{{Member: "orders.count"}},
		CollectedAt: time.Now(),
	}, "", "")

	require.Nil(t, request.ModelExport)
	require.Nil(t, request.CubeVersion)
	require.Nil(t, request.SecurityContextLabel)

	// A member with no error has SQL, also if the text is empty.
	member := request.GetMemberSql()[0]
	require.NotNil(t, member.Sql)
	require.Nil(t, member.DataSource)
	require.Nil(t, member.Error)
}

func TestNewSnapshotIsTheReverseOfNewRequest(t *testing.T) {
	for name, snapshot := range map[string]cube.Snapshot{
		"each field": {
			Meta: []byte(`{"cubes":[]}`),
			MemberSQL: []cube.MemberSQL{
				{Member: "orders.revenue", DataSource: "default", SQL: "SELECT 1", Ungrouped: true},
				{Member: "orders.grouped", SQL: "SELECT 2"},
				{Member: "orders.empty"},
				{Member: "orders.broken", Error: "no join path"},
			},
			ModelExport: []byte(`{"data":[]}`),
			CollectedAt: time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC),
			DataSources: []cube.DataSource{
				{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1"},
				{Name: "bigquery", Type: "bigquery", Project: "example-project"},
				{Name: "bare"},
			},
			CubeVersion:          "1.8.30",
			SecurityContextLabel: "full-read",
		},
		"only the required fields": {
			Meta:        []byte(`{"cubes":[]}`),
			CollectedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		},
	} {
		request := NewRequest(&snapshot, "0.1.0", "2026-03-01T00:00:00Z")
		back := NewSnapshot(request)
		require.Equal(t, &snapshot, back, name)

		// The message of the snapshot that came back is the same message.
		again := NewRequest(back, "0.1.0", "2026-03-01T00:00:00Z")
		require.True(t, proto.Equal(request, again), name)
	}
}

func TestNewSnapshotOfAMessageThatAPersonEdited(t *testing.T) {
	sql, text, source := "SELECT 1", "no join path", "default"
	request := &ingestcubev1.IngestSnapshotRequest{
		Meta: []byte(`{"cubes":[]}`),
		MemberSql: []*ingestcubev1.MemberSql{
			{Member: "orders.both", Sql: &sql, Error: &text, DataSource: &source},
			{Member: "orders.neither"},
		},
		UploaderVersion: "9.9.9",
	}

	snapshot := NewSnapshot(request)

	require.True(t, snapshot.CollectedAt.IsZero())
	require.Equal(t, []cube.MemberSQL{
		{Member: "orders.both", Error: "no join path"},
		{Member: "orders.neither"},
	}, snapshot.MemberSQL)
	// A message with no collected_at stays a message with no collected_at.
	require.Nil(t, NewRequest(snapshot, "", "").CollectedAt)
}
