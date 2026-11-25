package cmd_test

import (
	"strings"

	"github.com/getsynq/synq-cube/cube/cubetest"
)

func (s *UploadSuite) setCollection() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
}

// requireOnlyTheDataModelPaths asserts the request paths to Cube: one `/v1/meta`, the given
// count of `/v1/sql`, and no other path. So never `/v1/load`.
func (s *UploadSuite) requireOnlyTheDataModelPaths(sql int) {
	want := map[string]int{cubetest.BasePath + "/v1/meta": 1}
	if sql > 0 {
		want[cubetest.BasePath+"/v1/sql"] = sql
	}
	s.Equal(want, s.cube.Requests())
}

func (s *UploadSuite) TestExcludeAndIncludeOnACollection() {
	s.setCollection()

	s.Require().NoError(s.run("upload", "--include", "pg_*", "--include", "orders*", "--exclude", "*_finance"))

	s.Require().Len(s.service.requests, 1)
	sent := s.service.requests[0]
	s.Equal([]string{"pg_products", "pg_catalog", "orders", "pg_customers", "pg_order_items", "pg_orders", "orders_overview"}, s.names(sent))
	for _, member := range sent.GetMemberSql() {
		s.True(strings.HasPrefix(member.GetMember(), "pg_") || strings.HasPrefix(member.GetMember(), "orders"), member.GetMember())
	}
	// No SQL request for a member of a cube that was left out.
	s.requireOnlyTheDataModelPaths(len(sent.GetMemberSql()))
	s.Less(len(sent.GetMemberSql()), 254)

	out := s.stdout.String()
	s.Contains(out, "Cubes: 5\n")
	s.Contains(out, "Views: 2\n")
	s.Contains(out, "Left out by --include and --exclude: cubes 9, views 4, members ")
	s.NotContains(out, "--skip-hidden")
	// orders_overview includes members of calendar and customers, which are not in the snapshot.
	s.Contains(s.stderr.String(), "warning: the view orders_overview includes members of calendar")
	s.Contains(s.stderr.String(), "warning: the view orders_overview includes members of customers")
}

func (s *UploadSuite) TestTheFilterVariables() {
	s.setCollection()
	s.T().Setenv("CUBE_EXCLUDE", "pg_*, calendar ,")
	s.T().Setenv("CUBE_SKIP_HIDDEN", "true")

	s.Require().NoError(s.run("upload"))
	s.Contains(s.stdout.String(), "Left out by --include and --exclude: cubes 5, views 1, members 36\n")
	s.Contains(s.stdout.String(), "Left out by --skip-hidden: cubes 1, views 1, members ")

	// A flag wins over its variable.
	s.stdout.Reset()
	s.Require().NoError(s.run("upload", "--exclude", "calendar", "--skip-hidden=false"))
	s.Contains(s.stdout.String(), "Left out by --include and --exclude: cubes 1, views 0, members 4\n")
	s.NotContains(s.stdout.String(), "--skip-hidden")

	s.T().Setenv("CUBE_SKIP_HIDDEN", "sometimes")
	err := s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "CUBE_SKIP_HIDDEN")
}

func (s *UploadSuite) TestSkipHiddenOnACollection() {
	s.setCollection()

	s.Require().NoError(s.run("upload", "--skip-hidden"))

	sent := s.service.requests[0]
	s.Len(s.names(sent), 18)
	s.Len(sent.GetMemberSql(), 254-33)
	s.requireOnlyTheDataModelPaths(254 - 33)
	s.Contains(s.stdout.String(), "Cubes: 13\n")
	s.Contains(s.stdout.String(), "Left out by --skip-hidden: cubes 1, views 1, members 33\n")
}

func (s *UploadSuite) TestSkipMemberSQLOnACollection() {
	s.setCollection()

	s.Require().NoError(s.run("upload", "--skip-member-sql", "--skip-model-export"))

	sent := s.service.requests[0]
	s.Equal(cubetest.MetaExtended(), sent.GetMeta())
	s.Empty(sent.GetMemberSql())
	s.requireOnlyTheDataModelPaths(0)
	s.Contains(s.stdout.String(), "Members: 254\n")
	s.Contains(s.stdout.String(), "Left out by --skip-member-sql: the SQL of each member\n")
	// The deployment is not on Cube Cloud, so no model export was left out.
	s.NotContains(s.stdout.String(), "model export")
}

func (s *UploadSuite) TestAnInvalidPatternFailsAtStartup() {
	s.setCollection()

	err := s.run("upload", "--exclude", "orders[")
	s.Require().Error(err)
	s.Contains(err.Error(), `"orders["`)

	s.T().Setenv("CUBE_INCLUDE", "ok_*,[")
	err = s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "include pattern")

	s.Empty(s.cube.Requests())
	s.Empty(s.tokenExchanges)
}

func (s *UploadSuite) TestAFilterThatLeavesNothingSendsNothing() {
	s.setCollection()

	err := s.run("upload", "--include", "no_such_*")
	s.Require().Error(err)
	s.Contains(err.Error(), "leave no cube and no view")
	s.Empty(s.service.requests)
	s.requireOnlyTheDataModelPaths(0)
}

func (s *UploadSuite) TestHelpSaysThatASnapshotIsTheWholeDataModel() {
	s.Require().NoError(s.run("upload", "--help"))
	help := s.stdout.String()
	for _, flag := range []string{"--include", "--exclude", "--skip-hidden", "--skip-member-sql", "--skip-model-export", "--input"} {
		s.Contains(help, flag)
	}
	s.Contains(help, "CUBE_INCLUDE")
	s.Contains(help, "is removed from Coalesce Quality")
	s.Contains(help, "no lineage to the warehouse tables")
}
