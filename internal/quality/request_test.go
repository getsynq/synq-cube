package quality

import (
	"reflect"
	"testing"
	"time"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
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
	modelPath := "model"
	snapshot := cube.Snapshot{
		DataModel:   []byte(`{"cubes":[]}`),
		ModelExport: []byte(`{"data":[]}`),
		GeneratedSQL: []cube.GeneratedSQL{
			{
				SQL:        "SELECT 1",
				DataSource: "default",
				Ungrouped:  true,
				Members: []cube.GeneratedSQLMember{
					{Member: "orders.revenue", Column: "orders__revenue"},
					{Member: "orders.count", Column: "orders__count"},
				},
			},
			{SQL: "SELECT 2", Members: []cube.GeneratedSQLMember{{Member: "orders.rank", Column: "orders__rank"}}},
		},
		MemberErrors: []cube.MemberError{{Member: "orders.broken", Error: "no join path"}},
		DataSources: []cube.DataSource{
			{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1", Project: "example-project"},
			{Name: "postgres", Type: "postgres"},
		},
		Git:                  &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"},
		ModelPath:            &modelPath,
		CollectedAt:          collectedAt,
		CubeVersion:          "1.8.30",
		SecurityContextLabel: "full-read",
	}
	requireEachFieldSet(t, snapshot)
	requireEachFieldSet(t, snapshot.GeneratedSQL[0])
	requireEachFieldSet(t, snapshot.GeneratedSQL[0].Members[0])
	requireEachFieldSet(t, snapshot.MemberErrors[0])
	requireEachFieldSet(t, snapshot.DataSources[0])
	requireEachFieldSet(t, *snapshot.Git)

	request := NewRequest(&snapshot, "0.1.0", "2026-03-01T00:00:00Z")

	requireEachFieldPopulated(t, request.ProtoReflect())
	require.Equal(t, snapshot.DataModel, request.GetDataModel())
	require.Equal(t, snapshot.ModelExport, request.GetModelExport())
	require.Equal(t, collectedAt, request.GetCollectedAt().AsTime())
	require.Equal(t, "1.8.30", request.GetCubeVersion())
	require.Equal(t, "full-read", request.GetSecurityContextLabel())
	require.Equal(t, "0.1.0", request.GetUploaderVersion())
	require.Equal(t, "2026-03-01T00:00:00Z", request.GetUploaderBuildTime())
	require.Equal(t, "model", request.GetModelPath())

	require.Len(t, request.GetGeneratedSql(), 2)
	first := request.GetGeneratedSql()[0]
	requireEachFieldPopulated(t, first.ProtoReflect())
	require.Equal(t, "SELECT 1", first.GetSql())
	require.Equal(t, "default", first.GetDataSource())
	require.True(t, first.GetUngrouped())
	require.Len(t, first.GetMembers(), 2)
	requireEachFieldPopulated(t, first.GetMembers()[0].ProtoReflect())
	require.Equal(t, "orders.revenue", first.GetMembers()[0].GetMember())
	require.Equal(t, "orders__revenue", first.GetMembers()[0].GetColumn())
	require.Equal(t, "orders.count", first.GetMembers()[1].GetMember())
	require.Equal(t, "orders__count", first.GetMembers()[1].GetColumn())
	// An empty data source is an absent field of the message. The value of `ungrouped` is
	// always set, also if it is false.
	second := request.GetGeneratedSql()[1]
	require.Equal(t, "SELECT 2", second.GetSql())
	require.Nil(t, second.DataSource)
	require.NotNil(t, second.Ungrouped)
	require.False(t, second.GetUngrouped())
	require.Len(t, second.GetMembers(), 1)

	require.Len(t, request.GetMemberErrors(), 1)
	requireEachFieldPopulated(t, request.GetMemberErrors()[0].ProtoReflect())
	require.Equal(t, "orders.broken", request.GetMemberErrors()[0].GetMember())
	require.Equal(t, "no join path", request.GetMemberErrors()[0].GetError())

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
	require.NotNil(t, bare.Type)
	require.Nil(t, bare.Database)
	require.Nil(t, bare.Host)
	require.Nil(t, bare.Project)

	requireEachFieldPopulated(t, request.GetGitContext().ProtoReflect())
	require.Equal(t, "https://github.com/example/analytics", request.GetGitContext().GetCloneUrl())
	require.Equal(t, "main", request.GetGitContext().GetBranch())
	require.Equal(t, "0123abc", request.GetGitContext().GetCommitSha())
}

// An optional field with an empty value is not set: the API refuses an empty text there.
func TestNewRequestKeepsAbsentFieldsAbsent(t *testing.T) {
	request := NewRequest(&cube.Snapshot{
		DataModel:    []byte(`{"cubes":[]}`),
		ModelExport:  []byte{},
		GeneratedSQL: []cube.GeneratedSQL{{SQL: "SELECT 1", Members: []cube.GeneratedSQLMember{{Member: "orders.count", Column: "orders__count"}}}},
		DataSources:  []cube.DataSource{{Name: "default"}},
		Git:          &cube.GitContext{Branch: "main"},
		CollectedAt:  time.Now(),
	}, "", "")

	require.Nil(t, request.ModelExport)
	require.Nil(t, request.MemberErrors)
	require.Nil(t, request.GitContext)
	require.Nil(t, request.ModelPath)
	require.Nil(t, request.CubeVersion)
	require.Nil(t, request.SecurityContextLabel)
	require.Nil(t, request.UploaderVersion)
	require.Nil(t, request.UploaderBuildTime)
	require.Nil(t, request.GetGeneratedSql()[0].DataSource)
	source := request.GetDataSources()[0]
	require.Nil(t, source.Type)
	require.Nil(t, source.Database)
	require.Nil(t, source.Host)
	require.Nil(t, source.Project)

	empty := NewRequest(&cube.Snapshot{DataModel: []byte(`{"cubes":[]}`), CollectedAt: time.Now()}, "", "")
	require.Nil(t, empty.GeneratedSql)
	require.Nil(t, empty.DataSources)
}

// The root of the repository is an empty model path that is set. A directory that is not known
// is a model path that is not set.
func TestNewRequestKeepsTheRootOfTheRepository(t *testing.T) {
	root := ""
	request := NewRequest(&cube.Snapshot{DataModel: []byte(`{"cubes":[]}`), ModelPath: &root, CollectedAt: time.Now()}, "", "")
	require.NotNil(t, request.ModelPath)
	require.Empty(t, request.GetModelPath())
	require.Equal(t, &root, NewSnapshot(request).ModelPath)
}

func TestNewRequestWritesTheBuildTimeInRFC3339(t *testing.T) {
	for text, want := range map[string]string{
		"2026-03-01T00:00:00Z":      "2026-03-01T00:00:00Z",
		"2026-03-01T02:00:00+02:00": "2026-03-01T00:00:00Z",
		"2026-03-01 00:00:00":       "",
		"1772323200":                "",
		"unknown":                   "",
		"":                          "",
	} {
		request := NewRequest(&cube.Snapshot{DataModel: []byte(`{"cubes":[]}`)}, "0.1.0", text)
		if want == "" {
			require.Nil(t, request.UploaderBuildTime, text)
			continue
		}
		require.Equal(t, want, request.GetUploaderBuildTime(), text)
	}
}

// The error text of a member is never empty.
func TestNewRequestGivesEachMemberErrorAText(t *testing.T) {
	request := NewRequest(&cube.Snapshot{
		DataModel:    []byte(`{"cubes":[]}`),
		MemberErrors: []cube.MemberError{{Member: "orders.broken"}},
	}, "", "")
	require.NotEmpty(t, request.GetMemberErrors()[0].GetError())
}

func TestNewSnapshotIsTheReverseOfNewRequest(t *testing.T) {
	modelPath := "model"
	for name, snapshot := range map[string]cube.Snapshot{
		"each field": {
			DataModel:   []byte(`{"cubes":[]}`),
			ModelExport: []byte(`{"data":[]}`),
			GeneratedSQL: []cube.GeneratedSQL{
				{SQL: "SELECT 1", DataSource: "default", Ungrouped: true, Members: []cube.GeneratedSQLMember{
					{Member: "orders.revenue", Column: "orders__revenue"},
					{Member: "orders.count", Column: "orders__count"},
				}},
				{SQL: "SELECT 2", Members: []cube.GeneratedSQLMember{{Member: "orders.grouped", Column: "orders__grouped"}}},
			},
			MemberErrors: []cube.MemberError{{Member: "orders.broken", Error: "no join path"}},
			DataSources: []cube.DataSource{
				{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1"},
				{Name: "bigquery", Type: "bigquery", Project: "example-project"},
				{Name: "bare"},
			},
			Git:                  &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"},
			ModelPath:            &modelPath,
			CollectedAt:          time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC),
			CubeVersion:          "1.8.30",
			SecurityContextLabel: "full-read",
		},
		"a repository with no branch and no commit": {
			DataModel:   []byte(`{"cubes":[]}`),
			Git:         &cube.GitContext{CloneURL: "git@github.com:example/analytics.git"},
			CollectedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		},
		"only the required fields": {
			DataModel:   []byte(`{"cubes":[]}`),
			CollectedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		},
	} {
		request := NewRequest(&snapshot, "0.1.0", "2026-03-01T00:00:00Z")
		back := NewSnapshot(request)
		require.Equal(t, &snapshot, back, name)

		// The message of the snapshot that came back is the same message.
		again := NewRequest(back, "0.1.0", "2026-03-01T00:00:00Z")
		require.True(t, proto.Equal(request, again), name)

		// The snapshot file is the protojson form of the message. It gives the same snapshot.
		data, err := protojson.Marshal(request)
		require.NoError(t, err, name)
		fromFile := &ingestcubev1.IngestSnapshotRequest{}
		require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(data, fromFile), name)
		require.Equal(t, &snapshot, NewSnapshot(fromFile), name)
	}
}

func TestNewSnapshotOfAMessageWithNoCollectedAt(t *testing.T) {
	version := "9.9.9"
	request := &ingestcubev1.IngestSnapshotRequest{
		DataModel:       []byte(`{"cubes":[]}`),
		UploaderVersion: &version,
	}

	snapshot := NewSnapshot(request)

	require.True(t, snapshot.CollectedAt.IsZero())
	// A message with no collected_at stays a message with no collected_at.
	require.Nil(t, NewRequest(snapshot, "", "").CollectedAt)
}
