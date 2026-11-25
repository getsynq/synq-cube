package cubemeta_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube/cubemeta"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

type CubeMetaSuite struct {
	suite.Suite
	meta *cubemeta.Meta
}

func TestCubeMetaSuite(t *testing.T) {
	suite.Run(t, new(CubeMetaSuite))
}

func (s *CubeMetaSuite) SetupTest() {
	meta, err := cubemeta.Parse(cubetest.MetaExtended())
	s.Require().NoError(err)
	s.meta = meta
}

func (s *CubeMetaSuite) cube(name string) *cubemeta.Cube {
	for _, c := range s.meta.Cubes {
		if c.Name == name {
			return c
		}
	}
	s.FailNow("no cube " + name)
	return nil
}

func (s *CubeMetaSuite) member(members []*cubemeta.Member, name string) *cubemeta.Member {
	for _, m := range members {
		if m.Name == name {
			return m
		}
	}
	s.FailNow("no member " + name)
	return nil
}

func (s *CubeMetaSuite) TestCountsOfTheRecording() {
	cubes, views, members, segments := 0, 0, 0, 0
	for _, c := range s.meta.Cubes {
		if c.IsView() {
			views++
		} else {
			cubes++
		}
		members += len(c.Members())
		segments += len(c.Segments)
	}
	s.Equal(14, cubes)
	s.Equal(6, views)
	s.Equal(254, members)
	s.Equal(13, segments)
}

func (s *CubeMetaSuite) TestCube() {
	orders := s.cube("orders")
	s.Equal(cubemeta.TypeCube, orders.Type)
	s.False(orders.IsView())
	s.Equal("Orders", orders.Title)
	s.Equal("One row for each customer order", orders.Description)
	s.True(orders.Public)
	s.Equal("cubes/orders.yml", orders.FileName)
	s.Require().NotNil(orders.ConnectedComponent)
	s.EqualValues(1, *orders.ConnectedComponent)
	s.JSONEq(`{"domain":"sales","owner":"sales-analytics","tier":"gold"}`, string(orders.Meta))
	s.Len(orders.Measures, 37)
	s.Len(orders.Dimensions, 11)
	s.Len(orders.Segments, 2)
	s.Empty(orders.SQL)
	s.Contains(string(orders.Raw), `"connectedComponent"`)

	s.Contains(s.cube("events").SQL, "FROM DWH_TESTING.PIPELINES.EVENTS_RAW")
	s.Equal("products", s.cube("products_snapshot_extended").Extends)
	s.False(s.cube("product_snapshots").Public)
}

func (s *CubeMetaSuite) TestMembers() {
	orders := s.cube("orders")

	count := s.member(orders.Measures, "orders.count")
	s.Equal(cubemeta.KindMeasure, count.Kind)
	s.Equal("count", count.ShortName())
	s.Equal("Orders Count", count.Title)
	s.Equal("Count", count.ShortTitle)
	s.Equal("number", count.Type)
	s.Equal("count", count.AggType)
	s.Equal([]string{"orders.id", "orders.status", "orders.amount", "orders.ordered_at"}, count.DrillMembers)

	id := s.member(orders.Dimensions, "orders.id")
	s.Equal(cubemeta.KindDimension, id.Kind)
	s.True(id.PrimaryKey)
	s.Equal("`ID`", id.SQL)
	s.Equal("Unique order identifier", id.Description)
	s.JSONEq(`{"type":"custom-numeric","value":".0f","alias":"id"}`, string(id.Format))

	amount := s.member(orders.Dimensions, "orders.amount")
	s.JSONEq(`"currency"`, string(amount.Format))
	s.False(amount.PrimaryKey)

	newCount := s.member(orders.Measures, "orders.new_count")
	s.JSONEq(`{"generated_by":"status_measures","status":"new"}`, string(newCount.Meta))
	s.Contains(string(newCount.Raw), `"filters"`)

	s.False(s.member(orders.Dimensions, "orders.customer_id").Public)

	eventAt := s.member(s.cube("events").Dimensions, "events.event_at")
	s.Require().Len(eventAt.Granularities, 4)
	s.Equal("half_hour", eventAt.Granularities[0].Name)
	s.Equal("30 minutes", eventAt.Granularities[0].Interval)
	s.Equal("-1 day", eventAt.Granularities[1].Offset)
	s.Equal("2026-02-01", eventAt.Granularities[2].Origin)

	completed := s.member(orders.Segments, "orders.completed")
	s.Equal(cubemeta.KindSegment, completed.Kind)
	s.Contains(completed.SQL, "STATUS IN ('shipped', 'delivered')")
}

func (s *CubeMetaSuite) TestView() {
	view := s.cube("orders_overview")
	s.True(view.IsView())
	s.Nil(view.ConnectedComponent)
	s.Empty(view.Joins)

	count := s.member(view.Measures, "orders_overview.count")
	s.Equal("orders.count", count.AliasMember)
	s.Empty(count.SQL)

	s.Require().NotEmpty(view.Folders)
	s.Equal("Order", view.Folders[0].Name)
	s.Contains(view.Folders[0].Members, "orders_overview.order_status")

	s.Require().NotEmpty(view.Hierarchies)
	s.Equal("orders_overview.order_status", view.Hierarchies[0].Name)
	s.Equal("orders.order_status", view.Hierarchies[0].AliasMember)
	s.Equal([]string{"orders_overview.status_group", "orders_overview.order_status"}, view.Hierarchies[0].Levels)
	s.True(view.Hierarchies[0].Public)
}

func (s *CubeMetaSuite) TestNestedFolders() {
	view := s.cube("orders_overview_finance")
	var metrics *cubemeta.Folder
	for _, folder := range view.NestedFolders {
		if folder.Name == "Metrics" {
			metrics = folder
		}
	}
	s.Require().NotNil(metrics)
	s.Contains(metrics.Members, "orders_overview_finance.revenue")
	s.Require().Len(metrics.Folders, 1)
	s.Equal("Time comparison", metrics.Folders[0].Name)
	s.Contains(metrics.Folders[0].Members, "orders_overview_finance.cumulative_revenue")
}

func (s *CubeMetaSuite) TestJoinsHierarchiesAndPreAggregations() {
	orders := s.cube("orders")

	s.Require().NotEmpty(orders.Joins)
	s.Equal("customers", orders.Joins[0].Name)
	s.Equal("belongsTo", orders.Joins[0].Relationship)
	s.Contains(orders.Joins[0].SQL, "${customers.id}")

	s.Require().NotEmpty(orders.Hierarchies)
	s.False(orders.Hierarchies[0].Public)

	s.Require().NotEmpty(orders.PreAggregations)
	rollup := orders.PreAggregations[0]
	s.Equal("revenue_by_status_daily", rollup.Name)
	s.Equal("rollup", rollup.Type)
	s.Equal("day", rollup.Granularity)
	s.Equal("month", rollup.PartitionGranularity)
	s.Equal("CUBE.ordered_at", rollup.TimeDimensionReference)
	s.Equal("[CUBE.status]", rollup.DimensionReferences)
	s.Equal("[CUBE.count, CUBE.revenue, CUBE.returned_count]", rollup.MeasureReferences)
	s.True(rollup.External)
	s.False(rollup.ScheduledRefresh)
	s.JSONEq(`{"every":"1 day","incremental":true,"updateWindow":"7 day"}`, string(rollup.RefreshKey))
	s.Contains(string(rollup.Indexes), "statusAggregateIndex")
}

func (s *CubeMetaSuite) TestLenient() {
	meta, err := cubemeta.Parse([]byte(`{
		"newTopLevel": 1,
		"cubes": [
			"not an object",
			{"type": "cube"},
			{
				"name": "c", "type": "cube", "public": null, "title": 7, "futureProperty": {"a": [1]},
				"connectedComponent": "x", "meta": null,
				"measures": [{"name": "c.m", "aggType": ["x"], "sql": {"future": true}}, 5, {"title": "no name"}],
				"dimensions": {"not": "a list"},
				"folders": [{"name": "f", "members": ["c.m", 4, {"name": "g", "members": ["c.m"]}]}],
				"joins": [{"name": "d", "relationship": 3}],
				"preAggregations": [{"name": "p", "external": "yes"}]
			}
		]
	}`))
	s.Require().NoError(err)
	s.Require().Len(meta.Cubes, 1)
	c := meta.Cubes[0]
	s.Equal("c", c.Name)
	s.True(c.Public)
	s.Empty(c.Title)
	s.Nil(c.ConnectedComponent)
	s.Nil(c.Meta)
	s.Contains(string(c.Raw), "futureProperty")
	s.Require().Len(c.Measures, 1)
	s.Empty(c.Measures[0].AggType)
	s.Empty(c.Measures[0].SQL)
	s.Contains(string(c.Measures[0].Raw), `"future"`)
	s.Nil(c.Dimensions)
	s.Nil(c.Members()[0].Granularities)
	s.Equal([]string{"c.m"}, c.Folders[0].Members)
	s.Len(c.Folders[0].Folders, 1)
	s.Empty(c.Joins[0].Relationship)
	s.False(c.PreAggregations[0].External)
}

func (s *CubeMetaSuite) TestNotAModel() {
	for _, body := range []string{``, `[]`, `{"error": "Invalid token"}`, `<html>`} {
		_, err := cubemeta.Parse([]byte(body))
		s.Error(err, body)
	}
	meta, err := cubemeta.Parse([]byte(`{"cubes": []}`))
	s.Require().NoError(err)
	s.Empty(meta.Cubes)
}
