package cube_test

import (
	"time"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// The filter of Collect and Filter.Apply on a collection with no filter give the same snapshot
// and the same counts.
func (s *FilterSuite) TestCollectWithAFilterEqualsApplyOnTheWholeSnapshot() {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	now := func() time.Time { return at }
	s.server.ErrorMembers = map[string]string{"customers.lifetime_revenue": "Can't find join path"}
	s.server.GroupedOnlyMembers = map[string]bool{"orders.status_revenue_rank": true}

	whole, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Now: now, CubeVersion: "1.8.30"})
	s.Require().NoError(err)
	wholeMeta := append([]byte(nil), whole.DataModel...)

	for name, filter := range map[string]cube.Filter{
		"none":            {},
		"removes nothing": {Include: []string{"*"}, Exclude: []string{"no_such_cube"}},
		"exclude":         {Exclude: []string{"pg_*", "calendar"}},
		"include":         {Include: []string{"order*", "customers"}},
		"hidden":          {SkipHidden: true},
		"no member sql":   {SkipMemberSQL: true},
		"all":             {Include: []string{"*s"}, Exclude: []string{"pg_*"}, SkipHidden: true, SkipModelExport: true},
		"hidden, no sql":  {SkipHidden: true, SkipMemberSQL: true},
	} {
		collected, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Now: now, CubeVersion: "1.8.30", Filter: filter})
		s.Require().NoError(err, name)
		applied, filtered, err := filter.Apply(whole)
		s.Require().NoError(err, name)

		s.Equal(string(applied.DataModel), string(collected.DataModel), name)
		statements := len(collected.GeneratedSQL)
		if filter.SkipHidden && !filter.SkipMemberSQL {
			// Collect asks Cube only for the members that stay, so its statements have another
			// SQL text and can hold other lists of members. Apply keeps the SQL text of a
			// statement from which it removed a member. Each member has the same column, the
			// same data source and the same `ungrouped` value in the two.
			s.Equal(sqlOfEachMember(applied), sqlOfEachMember(collected), name)
			s.NotEqual(applied.GeneratedSQL, collected.GeneratedSQL, name)
			collected.GeneratedSQL = applied.GeneratedSQL
		}
		s.Equal(applied, collected, name)
		s.Equal(filtered.LeftOut, report.LeftOut, name)

		described, err := cube.Describe(applied)
		s.Require().NoError(err, name)
		s.Equal(report.Cubes, described.Cubes, name)
		s.Equal(report.Views, described.Views, name)
		s.Equal(report.Members, described.Members, name)
		s.Equal(report.MemberErrors, described.MemberErrors, name)
		s.Equal(report.MembersWithSQL, described.MembersWithSQL, name)
		s.Equal(statements, report.Statements, name)
		s.Equal(report.GroupedMembers, described.GroupedMembers, name)
		// The warnings of the filter come first in the report of Collect.
		s.Require().GreaterOrEqual(len(report.Warnings), len(filtered.Warnings), name)
		for i, warning := range filtered.Warnings {
			s.Equal(warning, report.Warnings[i], name)
		}
	}
	// Apply did not change its input.
	s.Equal(wholeMeta, whole.DataModel)
	s.Len(membersOf(&s.Suite, whole), 253)
	s.Len(whole.MemberErrors, 1)
}

// sqlOfEachMember returns the column, the data source and the `ungrouped` value of each member
// with SQL.
func sqlOfEachMember(snapshot *cube.Snapshot) map[string][3]string {
	out := map[string][3]string{}
	for _, generated := range snapshot.GeneratedSQL {
		for _, member := range generated.Members {
			ungrouped := "grouped"
			if generated.Ungrouped {
				ungrouped = "ungrouped"
			}
			out[member.Member] = [3]string{member.Column, generated.DataSource, ungrouped}
		}
	}
	return out
}

func (s *FilterSuite) TestApplyOnASnapshotWithAModelExport() {
	whole, _, err := cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{})
	s.Require().NoError(err)
	s.Require().NotNil(whole.ModelExport)

	// A filter that removes nothing keeps the model export.
	applied, report, err := cube.Filter{Exclude: []string{"no_such_cube"}}.Apply(whole)
	s.Require().NoError(err)
	s.Equal(whole, applied)
	s.False(report.ModelExportSkipped)

	// The model export cannot be filtered, so a snapshot with a part removed has none.
	applied, report, err = cube.Filter{Exclude: []string{"calendar"}}.Apply(whole)
	s.Require().NoError(err)
	s.Nil(applied.ModelExport)
	s.True(report.ModelExportSkipped)
	s.NotNil(whole.ModelExport)

	applied, report, err = cube.Filter{SkipModelExport: true}.Apply(whole)
	s.Require().NoError(err)
	s.Nil(applied.ModelExport)
	s.True(report.ModelExportSkipped)
	s.Equal(whole.DataModel, applied.DataModel)
	s.Len(membersOf(&s.Suite, applied), 254)
}

// A filter removes the members that it leaves out from each SQL statement, and a statement with
// no member left. The SQL text of a statement stays as it is.
func (s *FilterSuite) TestApplyOnTheGeneratedSQL() {
	git := &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"}
	modelPath := "model"
	whole := &cube.Snapshot{
		DataModel: []byte(`{"cubes":[
			{"name":"orders","type":"cube",
			 "measures":[{"name":"orders.count"},{"name":"orders.secret","public":false},{"name":"orders.broken"},{"name":"orders.hidden_broken","public":false}],
			 "dimensions":[{"name":"orders.id","public":false}]},
			{"name":"tmp_orders","type":"cube","measures":[{"name":"tmp_orders.count"},{"name":"tmp_orders.broken"}],"dimensions":[{"name":"tmp_orders.id"}]}
		]}`),
		GeneratedSQL: []cube.GeneratedSQL{
			{SQL: "SELECT measures of orders", DataSource: "default", Ungrouped: true, Members: []cube.GeneratedSQLMember{
				{Member: "orders.count", Column: "orders__count"}, {Member: "orders.secret", Column: "orders__secret"},
			}},
			{SQL: "SELECT dimensions of orders", DataSource: "default", Ungrouped: true, Members: []cube.GeneratedSQLMember{{Member: "orders.id", Column: "orders__id"}}},
			{SQL: "SELECT measures of tmp_orders", DataSource: "postgres", Members: []cube.GeneratedSQLMember{{Member: "tmp_orders.count", Column: "tmp_orders__count"}}},
			{SQL: "SELECT dimensions of tmp_orders", DataSource: "postgres", Ungrouped: true, Members: []cube.GeneratedSQLMember{{Member: "tmp_orders.id", Column: "tmp_orders__id"}}},
		},
		MemberErrors: []cube.MemberError{
			{Member: "orders.broken", Error: "no join path"},
			{Member: "orders.hidden_broken", Error: "no join path"},
			{Member: "tmp_orders.broken", Error: "no join path"},
		},
		Git:       git,
		ModelPath: &modelPath,
	}
	before := *whole
	beforeMembers := len(whole.GeneratedSQL[0].Members)

	// Exclude removes a cube: its statements and its errors go.
	applied, report, err := cube.Filter{Exclude: []string{"tmp_*"}}.Apply(whole)
	s.Require().NoError(err)
	s.Equal(whole.GeneratedSQL[:2], applied.GeneratedSQL)
	s.Equal(whole.MemberErrors[:2], applied.MemberErrors)
	s.Equal(1, report.ExcludedCubes)
	s.Equal(3, report.ExcludedMembers)
	s.Equal(git, applied.Git)
	s.Equal(&modelPath, applied.ModelPath)

	// Skip hidden removes a member from a statement, and a statement with no member left.
	applied, report, err = cube.Filter{SkipHidden: true}.Apply(whole)
	s.Require().NoError(err)
	s.Equal(3, report.HiddenMembers)
	s.Require().Len(applied.GeneratedSQL, 3)
	s.Equal(cube.GeneratedSQL{
		SQL: "SELECT measures of orders", DataSource: "default", Ungrouped: true,
		Members: []cube.GeneratedSQLMember{{Member: "orders.count", Column: "orders__count"}},
	}, applied.GeneratedSQL[0])
	s.Equal(whole.GeneratedSQL[2:], applied.GeneratedSQL[1:])
	s.Equal([]cube.MemberError{{Member: "orders.broken", Error: "no join path"}, {Member: "tmp_orders.broken", Error: "no join path"}}, applied.MemberErrors)

	described, err := cube.Describe(applied)
	s.Require().NoError(err)
	s.Equal(5, described.Members)
	s.Equal(3, described.MembersWithSQL)
	s.Equal(3, described.Statements)
	s.Equal(2, described.MemberErrors)
	s.Equal(1, described.GroupedMembers)

	// The two together, and no SQL at all.
	applied, _, err = cube.Filter{Exclude: []string{"tmp_*"}, SkipHidden: true}.Apply(whole)
	s.Require().NoError(err)
	s.Len(applied.GeneratedSQL, 1)
	s.Len(applied.MemberErrors, 1)
	applied, report, err = cube.Filter{SkipMemberSQL: true}.Apply(whole)
	s.Require().NoError(err)
	s.Nil(applied.GeneratedSQL)
	s.Nil(applied.MemberErrors)
	s.True(report.MemberSQLSkipped)

	// Apply did not change its input.
	s.Equal(before, *whole)
	s.Len(whole.GeneratedSQL[0].Members, beforeMembers)
	s.Equal("orders.secret", whole.GeneratedSQL[0].Members[1].Member)
}

func (s *FilterSuite) TestApplyErrors() {
	whole := &cube.Snapshot{DataModel: cubetest.MetaExtended()}

	_, _, err := cube.Filter{Include: []string{"no_such_*"}}.Apply(whole)
	s.Require().ErrorIs(err, cube.ErrNothingLeft)

	_, _, err = cube.Filter{Exclude: []string{"["}}.Apply(whole)
	s.Require().Error(err)
	s.Contains(err.Error(), "pattern")

	_, _, err = cube.Filter{SkipHidden: true}.Apply(&cube.Snapshot{DataModel: []byte(`[]`)})
	s.Require().Error(err)

	s.True(cube.Filter{}.IsZero())
	s.False(cube.Filter{SkipModelExport: true}.IsZero())
}
