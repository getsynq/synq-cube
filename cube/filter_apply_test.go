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
	wholeMeta := append([]byte(nil), whole.Meta...)

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

		s.Equal(string(applied.Meta), string(collected.Meta), name)
		s.Equal(applied, collected, name)
		s.Equal(filtered.LeftOut, report.LeftOut, name)

		described, err := cube.Describe(applied)
		s.Require().NoError(err, name)
		s.Equal(report.Cubes, described.Cubes, name)
		s.Equal(report.Views, described.Views, name)
		s.Equal(report.Members, described.Members, name)
		s.Equal(report.MemberErrors, described.MemberErrors, name)
		s.Equal(report.GroupedMembers, described.GroupedMembers, name)
		// The warnings of the filter come first in the report of Collect.
		s.Require().GreaterOrEqual(len(report.Warnings), len(filtered.Warnings), name)
		for i, warning := range filtered.Warnings {
			s.Equal(warning, report.Warnings[i], name)
		}
	}
	// Apply did not change its input.
	s.Equal(wholeMeta, whole.Meta)
	s.Len(whole.MemberSQL, 254)
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
	s.Equal(whole.Meta, applied.Meta)
	s.Len(applied.MemberSQL, 254)
}

func (s *FilterSuite) TestApplyErrors() {
	whole := &cube.Snapshot{Meta: cubetest.MetaExtended()}

	_, _, err := cube.Filter{Include: []string{"no_such_*"}}.Apply(whole)
	s.Require().ErrorIs(err, cube.ErrNothingLeft)

	_, _, err = cube.Filter{Exclude: []string{"["}}.Apply(whole)
	s.Require().Error(err)
	s.Contains(err.Error(), "pattern")

	_, _, err = cube.Filter{SkipHidden: true}.Apply(&cube.Snapshot{Meta: []byte(`[]`)})
	s.Require().Error(err)

	s.True(cube.Filter{}.IsZero())
	s.False(cube.Filter{SkipModelExport: true}.IsZero())
}
