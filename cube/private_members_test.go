package cube_test

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubemeta"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// The private part of the recording: one cube, one view and their 14 members, and 19 members of
// the public cubes.
const (
	privateMembersOfTheRecording = 33
	publicMembersOfTheRecording  = 254 - privateMembersOfTheRecording
	// The public data model has 18 cubes and views. 17 have public measures and 18 have public
	// dimensions.
	publicBatchesOfTheRecording = 35
)

// productionClient is a client of a Cube Cloud deployment in production: `/v1/meta` returns only
// what is public, and the model export has each cube, view and member.
func (s *FilterSuite) productionClient() *cube.Client {
	client := s.cloudClient()
	s.server.Meta = cubetest.PublicMetaExtended()
	s.server.ModelExport = cubetest.ModelExport()
	return client
}

// privateMembers returns the members of the recording that are not public, or that are in a cube
// or a view that is not public.
func (s *FilterSuite) privateMembers() map[string]bool {
	meta, err := cubemeta.Parse(cubetest.MetaExtended())
	s.Require().NoError(err)
	private := map[string]bool{}
	for _, c := range meta.Cubes {
		for _, member := range c.Members() {
			if c.Hidden() || member.Hidden() {
				private[member.Name] = true
			}
		}
	}
	s.Require().Len(private, privateMembersOfTheRecording)
	return private
}

func (s *FilterSuite) TestThePublicDataModelOfTheRecording() {
	meta, err := cubemeta.Parse(cubetest.PublicMetaExtended())
	s.Require().NoError(err)
	members := 0
	for _, c := range meta.Cubes {
		s.False(c.Hidden(), c.Name)
		for _, member := range c.Members() {
			s.False(member.Hidden(), member.Name)
		}
		members += len(c.Members())
	}
	s.Len(meta.Cubes, 18)
	s.Equal(publicMembersOfTheRecording, members)
}

// A deployment in production returns the private cubes, views and members only in the model
// export. The snapshot has their SQL, as for each other member.
func (s *FilterSuite) TestThePrivateMembersOfTheModelExportGetTheirSQL() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.productionClient(), cube.Options{})
	s.Require().NoError(err)

	s.Equal(string(cubetest.PublicMetaExtended()), string(snapshot.DataModel))
	s.Equal(string(cubetest.ModelExport()), string(snapshot.ModelExport))
	s.True(report.ModelExport)
	s.False(report.ModelExportSkipped)
	s.Empty(report.Warnings)
	// The counts are the counts of the whole data model.
	s.Equal(14, report.Cubes)
	s.Equal(6, report.Views)
	s.Equal(254, report.Members)
	s.Equal(254, report.MembersWithSQL)
	s.Zero(report.MemberErrors)
	s.Equal(1, report.ModelExportCubes)
	s.Equal(1, report.ModelExportViews)
	s.Equal(privateMembersOfTheRecording, report.ModelExportMembers)

	names := membersOf(&s.Suite, snapshot)
	s.Len(names, 254)
	private := s.privateMembers()
	for _, want := range cubetest.Members() {
		generated, column := findGenerated(&s.Suite, snapshot, want.Member)
		s.Equal(cubetest.Alias(want.Member), column)
		s.Contains(generated.SQL, want.SQL, want.Member)
		s.True(generated.Ungrouped, want.Member)
		// A statement has private members or public members, never the two: a snapshot that
		// loses its model export loses whole statements.
		for _, other := range generated.Members {
			s.Equal(private[want.Member], private[other.Member], want.Member)
		}
	}
	s.Equal(1, s.server.Requests()[cubetest.BasePath+"/v1/model-export"])
	s.Equal(report.SQLRequests, s.server.Requests()[cubetest.BasePath+"/v1/sql"])

	described, err := cube.Describe(snapshot)
	s.Require().NoError(err)
	s.Equal(report.Cubes, described.Cubes)
	s.Equal(report.Views, described.Views)
	s.Equal(report.Members, described.Members)
	s.Equal(report.MembersWithSQL, described.MembersWithSQL)
	s.Equal(report.ModelExportCubes, described.ModelExportCubes)
	s.Equal(report.ModelExportViews, described.ModelExportViews)
	s.Equal(report.ModelExportMembers, described.ModelExportMembers)
}

// The model export request comes before the first `/v1/sql` request of a private member, and a
// batch size of 1 gives one request for each member, also for a private one.
func (s *FilterSuite) TestBatchSizeOneSendsOneRequestForEachPrivateMember() {
	_, report, err := cube.Collect(s.T().Context(), s.productionClient(), cube.Options{BatchSize: 1})
	s.Require().NoError(err)
	s.Equal(254, report.SQLRequests)
	s.Equal(254, report.Statements)
	for _, request := range s.server.SQLRequests() {
		s.Len(request.Members, 1)
	}
}

// Cube Core has no model export. The collection sends no request for it, and the snapshot has
// what `/v1/meta` returns.
func (s *FilterSuite) TestWithNoCubeCloudTheSnapshotHasWhatMetaReturns() {
	s.server.Meta = cubetest.PublicMetaExtended()
	s.server.ModelExport = cubetest.ModelExport()

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)

	s.Nil(snapshot.ModelExport)
	s.False(report.ModelExport)
	s.Empty(report.Warnings)
	s.Equal(13, report.Cubes)
	s.Equal(5, report.Views)
	s.Equal(publicMembersOfTheRecording, report.Members)
	s.Zero(report.ModelExportCubes + report.ModelExportViews + report.ModelExportMembers)
	s.Len(membersOf(&s.Suite, snapshot), publicMembersOfTheRecording)
	s.Len(snapshot.GeneratedSQL, publicBatchesOfTheRecording)
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/model-export"])
}

// A model export that Cube Cloud refuses, or that is no JSON, is a warning. The snapshot then has
// what `/v1/meta` returns, as before.
func (s *FilterSuite) TestAModelExportThatIsNotReadLeavesThePublicDataModel() {
	for name, export := range map[string][]byte{
		"refused":  nil,
		"not JSON": []byte(`<html>`),
	} {
		s.Run(name, func() {
			s.SetupTest()
			client := s.productionClient()
			s.server.ModelExport = export

			snapshot, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
			s.Require().NoError(err)

			s.Nil(snapshot.ModelExport)
			s.False(report.ModelExport)
			s.False(report.ModelExportSkipped)
			s.Require().Len(report.Warnings, 1)
			s.Contains(report.Warnings[0], "the Cube Cloud model export is not in the snapshot")
			s.Equal(publicMembersOfTheRecording, report.Members)
			s.Len(membersOf(&s.Suite, snapshot), publicMembersOfTheRecording)
			s.Zero(report.ModelExportMembers)
		})
	}
	s.SetupTest()
	client := s.productionClient()
	s.server.ModelExport = nil
	_, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	s.Contains(report.Warnings[0], "so the snapshot has no private cube, view or member")
}

// A model export with no `data` list is in the snapshot as it is. It names no
// private member.
func (s *FilterSuite) TestAModelExportWithAnotherShapeNamesNoPrivateMember() {
	client := s.productionClient()
	s.server.ModelExport = []byte(`{"cubes": []}`)

	snapshot, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)
	s.NotNil(snapshot.ModelExport)
	s.Zero(report.ModelExportMembers)
	s.Len(membersOf(&s.Suite, snapshot), publicMembersOfTheRecording)
}

// An entry of the model export with no name is ignored, an entry that is there two times is read
// one time, and a member that `/v1/meta` has is not asked for again. A member for which Cube
// generates no SQL is an error of that member only.
func (s *FilterSuite) TestTheModelExportIsReadWithEntriesThatAreNotUsable() {
	client := s.productionClient()
	s.server.ModelExport = []byte(`{"data": [
		{"type": "cube", "public": false, "measures": [{"name": "count"}]},
		{"type": "cube", "name": "orders", "public": true,
			"measures": [{"name": "count", "public": true}, {"name": "revenue_thousands", "public": false}, {"name": "revenue_thousands"}, {"public": false}],
			"dimensions": [{"name": "amount_bucket", "public": false}, {"name": "no_such_member", "public": false}]},
		{"type": "cube", "name": "orders", "measures": [{"name": "debug_row_count", "public": false}]},
		{"type": "cube", "name": "calendar", "public": true, "dimensions": []}
	]}`)

	snapshot, report, err := cube.Collect(s.T().Context(), client, cube.Options{})
	s.Require().NoError(err)

	s.Equal(3, report.ModelExportMembers)
	s.Zero(report.ModelExportCubes)
	s.Equal(publicMembersOfTheRecording+3, report.Members)
	names := membersOf(&s.Suite, snapshot)
	s.Len(names, publicMembersOfTheRecording+2)
	s.Contains(names, "orders.revenue_thousands")
	s.Contains(names, "orders.amount_bucket")
	s.NotContains(names, "orders.debug_row_count")
	s.Require().Len(snapshot.MemberErrors, 1)
	s.Equal("orders.no_such_member", snapshot.MemberErrors[0].Member)
	s.Contains(snapshot.MemberErrors[0].Error, "not found")
}

// The filter of Collect and Filter.Apply give the same snapshot for a deployment with private
// cubes, views and members too. The model export cannot be filtered: a filter that removes a
// part of it removes all of it, with the SQL of the private members.
func (s *FilterSuite) TestCollectWithAFilterEqualsApplyWithPrivateMembers() {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	now := func() time.Time { return at }
	client := s.productionClient()
	s.server.ErrorMembers = map[string]string{"orders.amount_bucket": "Can't find join path"}

	whole, wholeReport, err := cube.Collect(s.T().Context(), client, cube.Options{Now: now})
	s.Require().NoError(err)
	s.Equal(privateMembersOfTheRecording, wholeReport.ModelExportMembers)
	s.Require().Len(whole.MemberErrors, 1)

	for name, c := range map[string]struct {
		filter cube.Filter
		// export is true if the snapshot keeps the model export and the private members.
		export bool
	}{
		"none":                      {cube.Filter{}, true},
		"removes nothing":           {cube.Filter{Include: []string{"*"}, Exclude: []string{"no_such_cube"}}, true},
		"no member sql":             {cube.Filter{SkipMemberSQL: true}, true},
		"exclude a public cube":     {cube.Filter{Exclude: []string{"calendar"}}, false},
		"exclude the private cube":  {cube.Filter{Exclude: []string{"product_snapshots"}}, false},
		"exclude the private view":  {cube.Filter{Exclude: []string{"events_*"}}, false},
		"include the public cubes":  {cube.Filter{Include: []string{"order*", "customers"}}, false},
		"hidden":                    {cube.Filter{SkipHidden: true}, false},
		"no model export":           {cube.Filter{SkipModelExport: true}, false},
		"hidden, no sql, no export": {cube.Filter{SkipHidden: true, SkipMemberSQL: true, SkipModelExport: true}, false},
	} {
		collected, report, err := cube.Collect(s.T().Context(), client, cube.Options{Now: now, Filter: c.filter})
		s.Require().NoError(err, name)
		applied, filtered, err := c.filter.Apply(whole)
		s.Require().NoError(err, name)

		s.Equal(applied, collected, name)
		s.Equal(filtered.LeftOut, report.LeftOut, name)
		s.Equal(c.export, collected.ModelExport != nil, name)
		s.Equal(!c.export, report.ModelExportSkipped, name)

		private := s.privateMembers()
		for _, member := range membersOf(&s.Suite, collected) {
			s.True(c.export || !private[member], "%s: %s", name, member)
		}
		for _, member := range collected.MemberErrors {
			s.True(c.export || !private[member.Member], "%s: %s", name, member.Member)
		}

		described, err := cube.Describe(applied)
		s.Require().NoError(err, name)
		s.Equal(report.Cubes, described.Cubes, name)
		s.Equal(report.Views, described.Views, name)
		s.Equal(report.Members, described.Members, name)
		s.Equal(report.MemberErrors, described.MemberErrors, name)
		s.Equal(report.MembersWithSQL, described.MembersWithSQL, name)
		s.Equal(report.ModelExportCubes, described.ModelExportCubes, name)
		s.Equal(report.ModelExportViews, described.ModelExportViews, name)
		s.Equal(report.ModelExportMembers, described.ModelExportMembers, name)
		if !c.export {
			s.Zero(report.ModelExportCubes+report.ModelExportViews+report.ModelExportMembers, name)
		}
	}
	// Apply did not change its input.
	s.NotNil(whole.ModelExport)
	s.Len(membersOf(&s.Suite, whole), 253)
}

// A snapshot of an older version of the tool has a model export and no SQL of the private
// members. Apply removes the model export in the same way.
func (s *FilterSuite) TestApplyOnAModelExportWithPrivateMembersAndNoSQLOfThem() {
	s.server.Meta = cubetest.PublicMetaExtended()
	whole, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	whole.ModelExport = cubetest.ModelExport()

	applied, report, err := cube.Filter{SkipHidden: true}.Apply(whole)
	s.Require().NoError(err)
	s.Nil(applied.ModelExport)
	s.True(report.ModelExportSkipped)
	s.Equal(whole.GeneratedSQL, applied.GeneratedSQL)

	// A model export that is no JSON names no private member, so only the settings decide.
	whole.ModelExport = []byte(`not JSON`)
	applied, report, err = cube.Filter{SkipHidden: true}.Apply(whole)
	s.Require().NoError(err)
	s.NotNil(applied.ModelExport)
	s.False(report.ModelExportSkipped)
}

func (s *FilterSuite) TestTheModelExportOfTheRecording() {
	var export struct {
		Data []struct {
			Name     string `json:"name"`
			Public   bool   `json:"public"`
			Measures []struct {
				Name string `json:"name"`
			} `json:"measures"`
		} `json:"data"`
	}
	s.Require().NoError(json.Unmarshal(cubetest.ModelExport(), &export))
	s.Len(export.Data, 20)
	for _, entity := range export.Data {
		s.Equal(entity.Name != "product_snapshots" && entity.Name != "events_internal", entity.Public, entity.Name)
		for _, measure := range entity.Measures {
			// A member of the model export has its name with no cube.
			s.False(strings.Contains(measure.Name, "."), measure.Name)
		}
	}
}
