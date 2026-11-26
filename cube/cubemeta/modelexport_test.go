package cubemeta_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube/cubemeta"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

type ModelExportSuite struct {
	suite.Suite
}

func TestModelExportSuite(t *testing.T) {
	suite.Run(t, new(ModelExportSuite))
}

const publicOrders = `{"cubes": [
  {"name": "orders", "type": "cube", "title": "Orders", "fileName": "cubes/orders.yml",
   "measures": [{"name": "orders.count", "aggType": "count"}],
   "dimensions": [{"name": "orders.status", "type": "string", "sql": "` + "`STATUS`" + `"}]}
], "compilerId": "abc"}`

func (s *ModelExportSuite) add(dataModel, modelExport string) (*cubemeta.Meta, []*cubemeta.ExportOnly, []byte) {
	export, err := cubemeta.ParseModelExport([]byte(modelExport))
	s.Require().NoError(err)
	merged, parts, err := export.AddTo([]byte(dataModel))
	s.Require().NoError(err)
	s.Require().True(json.Valid(merged), "%s", merged)
	meta, err := cubemeta.Parse(merged)
	s.Require().NoError(err)
	return meta, parts, merged
}

func (s *ModelExportSuite) TestAResponseThatIsNoExportDoesNotRead() {
	for name, body := range map[string]string{
		"not JSON":      `{"data": [`,
		"a list":        `[]`,
		"data, no list": `{"data": "none"}`,
	} {
		_, err := cubemeta.ParseModelExport([]byte(body))
		s.Error(err, name)
	}
	for name, body := range map[string]string{
		"no data":   `{}`,
		"null data": `{"data": null}`,
		"no entry":  `{"data": [], "pagination": {"total": 0}}`,
	} {
		export, err := cubemeta.ParseModelExport([]byte(body))
		s.Require().NoError(err, name)
		s.Empty(export.Entities, name)
	}
}

func (s *ModelExportSuite) TestAnEntryThatIsNotUsableIsNotRead() {
	export, err := cubemeta.ParseModelExport([]byte(`{"data": [
		null, 7, {"type": "cube", "title": "No name"}, {"name": ["orders"]},
		{"type": "cube", "name": "internal", "public": false, "calendar": true,
		 "table_references": [{"database": "DB", "schema": "S", "table": "T"}, null],
		 "measures": [null, {"public": false}, {"name": "count", "agg_type": "count"}, {"name": "count", "agg_type": "sum"}],
		 "dimensions": [{"name": "count"}, {"name": "id", "public": false}],
		 "segments": [{"name": "id"}, {"name": "recent"}]},
		{"type": "cube", "name": "internal", "measures": [{"name": "other"}]}
	]}`))
	s.Require().NoError(err)
	s.Require().Len(export.Entities, 1)
	entity := export.Entities[0]
	s.Equal("internal", entity.Name)
	s.False(entity.Public)
	s.True(entity.Calendar)
	s.False(entity.IsView())
	s.Equal([]cubemeta.ExportTable{{Database: "DB", Schema: "S", Table: "T"}}, entity.Tables)

	// A member name is unique in a cube, whatever its kind. The first one wins.
	s.Require().Len(entity.Measures, 1)
	s.Equal("count", entity.Measures[0].Name)
	s.True(entity.Measures[0].Public)
	s.JSONEq(`"count"`, string(entity.Measures[0].Properties["agg_type"]))
	s.Require().Len(entity.Dimensions, 1)
	s.Equal("id", entity.Dimensions[0].Name)
	s.False(entity.Dimensions[0].Public)
	s.Require().Len(entity.Segments, 1)
	s.Equal(cubemeta.KindSegment, entity.Segments[0].Kind)
}

// The response of `/v1/meta` is the source of what it has. The export adds only the rest, and the
// bytes of the response outside the member lists stay as they are.
func (s *ModelExportSuite) TestTheExportAddsOnlyWhatMetaDoesNotHave() {
	meta, parts, merged := s.add(publicOrders, `{"data": [{
		"type": "cube", "name": "orders", "public": false, "title": "Another title",
		"measures": [
			{"name": "count", "public": false, "agg_type": "sum"},
			{"name": "debug_count", "public": false, "title": "Another title Debug Count", "agg_type": "count", "sql": "CASE WHEN A < 1 THEN 1 END"}
		],
		"dimensions": [{"name": "status", "sql": "OTHER"}, {"name": "id", "is_primary_key": true, "public": false}],
		"segments": [{"name": "status", "sql": "1 = 1"}, {"name": "recent", "public": false, "sql": "${CUBE}.AGE < 7"}]
	}]}`)

	s.Require().Len(parts, 1)
	s.False(parts[0].Whole)
	s.Equal(3, parts[0].Members())
	s.Len(parts[0].Cube.Members(), 2)

	s.Require().Len(meta.Cubes, 1)
	orders := meta.Cubes[0]
	s.Equal("Orders", orders.Title)
	s.True(orders.Public)
	s.Equal("cubes/orders.yml", orders.FileName)

	s.Require().Len(orders.Measures, 2)
	s.Equal("count", orders.Measures[0].AggType)
	s.True(orders.Measures[0].Public)
	debug := orders.Measures[1]
	s.Equal("orders.debug_count", debug.Name)
	s.Equal("count", debug.AggType)
	s.False(debug.Public)
	s.Equal("CASE WHEN A < 1 THEN 1 END", debug.SQL)
	// The short title is the title of the member with no title of its cube.
	s.Equal("Debug Count", debug.ShortTitle)

	s.Require().Len(orders.Dimensions, 2)
	s.Equal("`STATUS`", orders.Dimensions[0].SQL)
	s.True(orders.Dimensions[1].PrimaryKey)
	s.Require().Len(orders.Segments, 1)
	s.Equal("orders.recent", orders.Segments[0].Name)

	s.Contains(string(merged), `"fileName": "cubes/orders.yml"`)
	s.Contains(string(merged), `], "compilerId": "abc"}`)
	// The SQL is not escaped.
	s.Contains(string(merged), `A < 1`)
}

func (s *ModelExportSuite) TestACubeThatOnlyTheExportHasIsAddedWhole() {
	meta, parts, _ := s.add(publicOrders, `{"data": [
		{"type": "cube", "name": "orders", "measures": [{"name": "count"}], "dimensions": [{"name": "status"}]},
		{"type": "cube", "name": "internal", "public": false, "title": "Internal", "description": "Private",
		 "sql": "SELECT 1", "calendar": true, "table_references": [{"schema": "S", "table": "T"}],
		 "folders": [{"name": "Keys", "members": ["id"]}],
		 "joins": [{"cube": "orders", "relationship": "many_to_one", "sql": "${CUBE}.ID = ${orders.id}"}, {"relationship": "one_to_one"}, null],
		 "dimensions": [{"name": "id", "title": "Internal ID", "type": "number", "sql": "ID", "is_primary_key": true,
		                 "granularities": [{"name": "week", "interval": "1 week"}]}]}
	]}`)

	s.Require().Len(parts, 1)
	s.True(parts[0].Whole)
	s.Require().Len(meta.Cubes, 2)
	internal := meta.Cubes[1]
	s.Equal("internal", internal.Name)
	s.False(internal.Public)
	s.True(internal.Hidden())
	s.Equal("Internal", internal.Title)
	s.Equal("Private", internal.Description)
	s.Equal("SELECT 1", internal.SQL)
	s.Empty(internal.FileName)

	s.Require().Len(internal.Joins, 1)
	s.Equal("orders", internal.Joins[0].Name)
	s.Equal("many_to_one", internal.Joins[0].Relationship)
	s.Empty(internal.Folders)
	s.Require().Len(internal.NestedFolders, 1)
	s.Equal([]string{"id"}, internal.NestedFolders[0].Members)

	s.Require().Len(internal.Dimensions, 1)
	id := internal.Dimensions[0]
	s.Equal("internal.id", id.Name)
	s.Equal("ID", id.ShortTitle)
	s.True(id.PrimaryKey)
	// A member with no `public` property is not private.
	s.True(id.Public)
	s.Require().Len(id.Granularities, 1)

	// The entry keeps the properties that `/v1/meta` has no name for.
	var entry map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(internal.Raw, &entry))
	s.Contains(entry, "table_references")
	s.Contains(entry, "calendar")
	s.NotContains(entry, "folders")
}

func (s *ModelExportSuite) TestAMemberOfAViewStandsForTheMemberThatItsSQLNames() {
	meta, _, _ := s.add(publicOrders, `{"data": [{"type": "view", "name": "internal", "public": false,
		"measures": [
			{"name": "count", "sql": "${orders.count}"},
			{"name": "doubled", "sql": "${orders.count} * 2"},
			{"name": "own", "sql": "${count}"},
			{"name": "filtered", "sql": "${FILTER_PARAMS.orders.status}"},
			{"name": "called", "sql": "${orders.count.filter(x)}"}
		],
		"dimensions": [{"name": "region", "sql": " ${orders.customers.region} "}],
		"segments": [{"name": "recent", "sql": "${orders.recent}"}]
	}]}`)

	view := meta.Cubes[1]
	s.True(view.IsView())
	alias := map[string]string{}
	for _, members := range [][]*cubemeta.Member{view.Measures, view.Dimensions, view.Segments} {
		for _, member := range members {
			alias[member.ShortName()] = member.AliasMember
		}
	}
	s.Equal(map[string]string{
		"count": "orders.count", "doubled": "", "own": "", "filtered": "", "called": "",
		"region": "customers.region", "recent": "",
	}, alias)

	// A member of a cube stands for no member.
	meta, _, _ = s.add(publicOrders, `{"data": [{"type": "cube", "name": "internal",
		"measures": [{"name": "count", "sql": "${orders.count}"}]}]}`)
	s.Empty(meta.Cubes[1].Measures[0].AliasMember)
}

func (s *ModelExportSuite) TestAnExportWithNothingNewLeavesTheResponseAsItIs() {
	export, err := cubemeta.ParseModelExport([]byte(`{"data": [{"type": "cube", "name": "orders",
		"measures": [{"name": "count"}], "dimensions": [{"name": "status"}]}]}`))
	s.Require().NoError(err)
	merged, parts, err := export.AddTo([]byte(publicOrders))
	s.Require().NoError(err)
	s.Empty(parts)
	s.Equal(publicOrders, string(merged))
}

func (s *ModelExportSuite) TestTheExportIsTheDataModelOfAResponseWithNoCube() {
	export := `{"data": [{"type": "view", "name": "internal", "dimensions": [{"name": "id"}]}]}`
	for name, dataModel := range map[string]string{
		"an empty list": `{"cubes": []}`,
		"no list":       `{}`,
		"null":          `{"cubes": null, "compilerId": "abc"}`,
	} {
		meta, parts, _ := s.add(dataModel, export)
		s.Require().Len(meta.Cubes, 1, name)
		s.Equal("internal.id", meta.Cubes[0].Dimensions[0].Name, name)
		s.True(parts[0].Whole, name)
	}

	parsed, err := cubemeta.ParseModelExport([]byte(export))
	s.Require().NoError(err)
	_, _, err = parsed.AddTo([]byte(`[]`))
	s.Error(err)
}

// The merge of the sample of a Cube Cloud deployment in production gives each cube, view and
// member that the same deployment returns in development mode.
func (s *ModelExportSuite) TestTheSampleExportGivesTheNamesOfTheWholeDataModel() {
	whole, err := cubemeta.Parse(cubetest.MetaExtended())
	s.Require().NoError(err)
	export, err := cubemeta.ParseModelExport(cubetest.ModelExport())
	s.Require().NoError(err)
	merged, parts, err := export.AddTo(cubetest.PublicMetaExtended())
	s.Require().NoError(err)
	s.NotEmpty(parts)
	meta, err := cubemeta.Parse(merged)
	s.Require().NoError(err)

	names := func(meta *cubemeta.Meta) map[string]bool {
		out := map[string]bool{}
		for _, c := range meta.Cubes {
			out[c.Name] = true
			for _, member := range c.Members() {
				out[member.Name] = true
			}
		}
		return out
	}
	s.Equal(names(whole), names(meta))
}
