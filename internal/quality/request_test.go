package quality

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	}
	requireEachFieldSet(t, snapshot)
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
