package cube_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubemeta"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// The recording has 14 cubes and 6 views with 254 members. One cube (product_snapshots, 3
// members) and one view (events_internal, 11 members) are not public, and 19 members of the
// other cubes are not public.
type FilterSuite struct {
	suite.Suite
	server *cubetest.Server
}

func TestFilterSuite(t *testing.T) {
	suite.Run(t, new(FilterSuite))
}

func (s *FilterSuite) SetupTest() {
	s.server = cubetest.NewServer()
	s.T().Cleanup(s.server.Close)
}

func (s *FilterSuite) client() *cube.Client {
	tokens, err := cube.NewStaticTokenSource("the-token")
	s.Require().NoError(err)
	client, err := cube.NewClient(s.server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)
	return client
}

func (s *FilterSuite) names(snapshot *cube.Snapshot) []string {
	meta, err := cubemeta.Parse(snapshot.DataModel)
	s.Require().NoError(err)
	names := make([]string, 0, len(meta.Cubes))
	for _, c := range meta.Cubes {
		names = append(names, c.Name)
	}
	return names
}

// requestedMembers returns the count of members in the `/v1/sql` requests. No test of this
// suite makes Cube return an error, so each member is in one request.
func (s *FilterSuite) requestedMembers() int {
	count := 0
	for _, request := range s.server.SQLRequests() {
		count += len(request.Members)
	}
	return count
}

// requireOnlyTheDataModelPaths asserts the request paths: one `/v1/meta`, the given count of
// `/v1/sql`, and no other path. So never `/v1/load`.
func (s *FilterSuite) requireOnlyTheDataModelPaths(sql int) {
	want := map[string]int{cubetest.BasePath + "/v1/meta": 1}
	if sql > 0 {
		want[cubetest.BasePath+"/v1/sql"] = sql
	}
	s.Equal(want, s.server.Requests())
}

func (s *FilterSuite) TestNoFilterKeepsTheBytesOfMeta() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Equal(cubetest.MetaExtended(), snapshot.DataModel)
	s.Zero(report.ExcludedCubes + report.ExcludedViews + report.ExcludedMembers)
	s.Zero(report.HiddenCubes + report.HiddenViews + report.HiddenMembers)
	s.False(report.MemberSQLSkipped)
	s.False(report.ModelExportSkipped)
}

func (s *FilterSuite) TestAFilterThatRemovesNothingKeepsTheBytesOfMeta() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Include: []string{"*"}, Exclude: []string{"no_such_cube"}}})
	s.Require().NoError(err)
	s.Equal(cubetest.MetaExtended(), snapshot.DataModel)
}

func (s *FilterSuite) TestExclude() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Exclude: []string{"pg_*", "calendar"}}})
	s.Require().NoError(err)

	// pg_products (10), pg_customers (4), pg_order_items (6), pg_orders (7) and calendar (4) are
	// cubes. pg_catalog (5) is a view.
	s.Equal(5, report.ExcludedCubes)
	s.Equal(1, report.ExcludedViews)
	s.Equal(36, report.ExcludedMembers)
	s.Equal(9, report.Cubes)
	s.Equal(5, report.Views)
	s.Equal(254-36, report.Members)

	names := s.names(snapshot)
	s.Len(names, 14)
	for _, name := range names {
		s.False(strings.HasPrefix(name, "pg_") || name == "calendar", name)
	}
	members := membersOf(&s.Suite, snapshot)
	s.Len(members, 254-36)
	for _, member := range members {
		s.False(strings.HasPrefix(member, "pg_") || strings.HasPrefix(member, "calendar."), member)
	}
	// No SQL request for a member of a cube that was left out.
	s.requireOnlyTheDataModelPaths(report.SQLRequests)
	s.Equal(254-36, s.requestedMembers())

	// Two views include members of calendar. They stay, with a warning each.
	s.Contains(names, "orders_overview")
	s.Require().Len(report.Warnings, 2)
	s.Contains(report.Warnings[0], "orders_overview_finance")
	s.Contains(report.Warnings[0], "calendar")
	s.Contains(report.Warnings[1], "the view orders_overview includes members of calendar")
}

func (s *FilterSuite) TestInclude() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Include: []string{"orders", "order[s_]*"}}})
	s.Require().NoError(err)

	// orders, order_items, order_items_detail, orders_overview, orders_overview_finance.
	s.Equal([]string{"orders", "order_items", "orders_overview_finance", "order_items_detail", "orders_overview"}, s.names(snapshot))
	s.Equal(2, report.Cubes)
	s.Equal(3, report.Views)
	s.Equal(12, report.ExcludedCubes)
	s.Equal(3, report.ExcludedViews)
	s.Len(membersOf(&s.Suite, snapshot), report.Members)
	s.Equal(report.Members, report.MembersWithSQL)
	s.Equal(254, report.Members+report.ExcludedMembers)
	s.requireOnlyTheDataModelPaths(report.SQLRequests)
	s.Equal(report.Members, s.requestedMembers())
}

func (s *FilterSuite) TestExcludeWinsOverInclude() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Include: []string{"orders*"}, Exclude: []string{"*_finance"}}})
	s.Require().NoError(err)
	s.Equal([]string{"orders", "orders_overview"}, s.names(snapshot))
}

func (s *FilterSuite) TestPatternsAreCaseSensitiveAndMatchTheWholeName() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Exclude: []string{"ORDERS", "order", "customer?"}}})
	s.Require().NoError(err)
	names := s.names(snapshot)
	s.Contains(names, "orders")
	s.Contains(names, "order_items")
	s.NotContains(names, "customers")
	s.Len(names, 19)
}

func (s *FilterSuite) TestAnInvalidPatternFailsBeforeARequest() {
	for _, options := range []cube.Options{
		{Filter: cube.Filter{Include: []string{"orders["}}},
		{Filter: cube.Filter{Exclude: []string{"a", "[]"}}},
		{Filter: cube.Filter{Exclude: []string{""}}},
	} {
		s.Require().Error(options.Filter.Validate())
		_, _, err := cube.Collect(s.T().Context(), s.client(), options)
		s.Require().Error(err)
		s.Contains(err.Error(), "pattern")
	}
	s.Empty(s.server.Requests())
	s.NoError(cube.Filter{Include: []string{"orders*", "pg_[a-c]?"}}.Validate())
}

func (s *FilterSuite) TestNothingLeftIsAnError() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Include: []string{"no_such_*"}}})
	s.Require().ErrorIs(err, cube.ErrNothingLeft)
	s.Nil(snapshot)
	s.Nil(report)
	s.requireOnlyTheDataModelPaths(0)

	// A data model that has no cube and no view is not an error: no setting removed a thing.
	s.server.Meta = []byte(`{"cubes":[]}`)
	_, _, err = cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Include: []string{"no_such_*"}}})
	s.Require().NoError(err)
}

func (s *FilterSuite) TestSkipHidden() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{SkipHidden: true}})
	s.Require().NoError(err)

	s.Equal(1, report.HiddenCubes)
	s.Equal(1, report.HiddenViews)
	s.Equal(3+11+19, report.HiddenMembers)
	s.Equal(13, report.Cubes)
	s.Equal(5, report.Views)
	s.Equal(254-33, report.Members)
	s.Zero(report.ExcludedCubes)

	meta, err := cubemeta.Parse(snapshot.DataModel)
	s.Require().NoError(err)
	s.Len(meta.Cubes, 18)
	for _, c := range meta.Cubes {
		s.False(c.Hidden(), c.Name)
		for _, member := range append(c.Members(), c.Segments...) {
			s.False(member.Hidden(), member.Name)
		}
	}
	s.Len(membersOf(&s.Suite, snapshot), 254-33)
	s.requireOnlyTheDataModelPaths(report.SQLRequests)
	s.Equal(254-33, s.requestedMembers())
}

func (s *FilterSuite) TestTheLegacyVisibleFlagIsHidden() {
	s.server.Meta = []byte(`{"cubes":[
		{"name":"orders","type":"cube","measures":[{"name":"orders.count"},{"name":"orders.secret","isVisible":false}]},
		{"name":"old","type":"cube","isVisible":false,"measures":[{"name":"old.count"}]}
	]}`)
	s.server.ErrorMembers = map[string]string{"orders.count": "not in the recording"}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{SkipHidden: true}})
	s.Require().NoError(err)
	s.Equal(1, report.HiddenCubes)
	s.Equal(2, report.HiddenMembers)
	s.Equal(1, report.Members)
	s.JSONEq(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}]}]}`, string(snapshot.DataModel))
}

func (s *FilterSuite) TestAFilterKeepsThePropertiesItDoesNotKnow() {
	s.server.Meta = []byte(`{ "futureKey": {"b": 1, "a": [1,2]},
  "cubes": [
    {"name":"a","type":"cube","future":{"z":1,"y":2},"measures":[]},
    {"name":"b","type":"cube","measures":[]},
    {"name":"c","type":"view",  "x-unknown": [3, 2, 1] ,"dimensions":[]}
  ],
  "compilerId": "abc" }`)

	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{Exclude: []string{"b"}}})
	s.Require().NoError(err)
	// The order of the properties and the bytes of the entries that stay do not change.
	s.Equal(`{ "futureKey": {"b": 1, "a": [1,2]},
  "cubes": [{"name":"a","type":"cube","future":{"z":1,"y":2},"measures":[]},{"name":"c","type":"view",  "x-unknown": [3, 2, 1] ,"dimensions":[]}],
  "compilerId": "abc" }`, string(snapshot.DataModel))
	s.True(json.Valid(snapshot.DataModel))
}

func (s *FilterSuite) TestSkipMemberSQL() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{SkipMemberSQL: true}})
	s.Require().NoError(err)
	s.True(report.MemberSQLSkipped)
	s.Equal(254, report.Members)
	s.Zero(report.MemberErrors)
	s.Empty(report.Warnings)
	s.Nil(snapshot.GeneratedSQL)
	s.Nil(snapshot.MemberErrors)
	s.Zero(report.SQLRequests)
	s.Equal(cubetest.MetaExtended(), snapshot.DataModel)
	s.requireOnlyTheDataModelPaths(0)
}

func (s *FilterSuite) cloudClient() *cube.Client {
	cloud := newFakeCloud(s.server.APIURL() + "/v1")
	s.T().Cleanup(cloud.Close)
	s.server.Authorization = "Bearer " + cloud.token
	s.server.DataSources = []byte(`{"data":{"data_sources":[]}}`)
	s.server.ModelExport = []byte(`{"data":[{"name":"orders"}],"pagination":{"total":1,"offset":0,"limit":100}}`)
	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7})
	s.Require().NoError(err)
	client, err := cube.NewClient(s.server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)
	return client
}

func (s *FilterSuite) TestSkipModelExport() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{Filter: cube.Filter{SkipModelExport: true, SkipMemberSQL: true}})
	s.Require().NoError(err)
	s.Nil(snapshot.ModelExport)
	s.False(report.ModelExport)
	s.True(report.ModelExportSkipped)
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/model-export"])
}

func (s *FilterSuite) TestANameFilterLeavesOutTheModelExport() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{Filter: cube.Filter{Exclude: []string{"calendar"}, SkipMemberSQL: true}})
	s.Require().NoError(err)
	s.Nil(snapshot.ModelExport)
	s.True(report.ModelExportSkipped)
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/model-export"])
}
