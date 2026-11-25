package cube_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

type CollectSuite struct {
	suite.Suite
	server *cubetest.Server
}

func TestCollectSuite(t *testing.T) {
	suite.Run(t, new(CollectSuite))
}

func (s *CollectSuite) SetupTest() {
	s.server = cubetest.NewServer()
	s.T().Cleanup(s.server.Close)
}

func (s *CollectSuite) client(options ...cube.ClientOption) *cube.Client {
	tokens, err := cube.NewStaticTokenSource("the-token")
	s.Require().NoError(err)
	options = append([]cube.ClientOption{cube.WithRetryBackoff(time.Millisecond)}, options...)
	client, err := cube.NewClient(s.server.APIURL(), tokens, options...)
	s.Require().NoError(err)
	return client
}

func (s *CollectSuite) memberSQL(snapshot *cube.Snapshot, name string) cube.MemberSQL {
	for _, member := range snapshot.MemberSQL {
		if member.Member == name {
			return member
		}
	}
	s.FailNow("no member " + name)
	return cube.MemberSQL{}
}

func (s *CollectSuite) TestSnapshotOfTheRecording() {
	s.server.Authorization = "the-token"
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{
		SecurityContextLabel: "full-read",
		CubeVersion:          "1.8.30",
		Now:                  func() time.Time { return at },
	})
	s.Require().NoError(err)

	s.Equal(14, report.Cubes)
	s.Equal(6, report.Views)
	s.Equal(254, report.Members)
	s.Zero(report.MemberErrors)
	s.Zero(report.GroupedMembers)
	s.False(report.ModelExport)
	s.Empty(report.Warnings)

	s.Equal(cubetest.MetaExtended(), snapshot.Meta)
	s.Nil(snapshot.ModelExport)
	s.Equal(at, snapshot.CollectedAt)
	s.Equal("full-read", snapshot.SecurityContextLabel)
	s.Equal("1.8.30", snapshot.CubeVersion)

	recorded := cubetest.Members()
	s.Require().Len(snapshot.MemberSQL, len(recorded))
	byName := map[string]cubetest.Member{}
	for _, member := range recorded {
		byName[member.Member] = member
	}
	for _, member := range snapshot.MemberSQL {
		want, found := byName[member.Member]
		s.Require().True(found, member.Member)
		s.Equal(want.SQL, member.SQL, member.Member)
		s.Equal(want.DataSource, member.DataSource, member.Member)
		s.True(member.Ungrouped, member.Member)
		s.Empty(member.Error, member.Member)
	}

	// The members keep the order of the data model: the measures of a cube, then its dimensions.
	s.Equal("pg_products.count", snapshot.MemberSQL[0].Member)
	revenue := s.memberSQL(snapshot, "orders_overview.revenue")
	s.Equal("default", revenue.DataSource)
	s.Contains(revenue.SQL, "DWH_TESTING")
	s.Equal("postgres", s.memberSQL(snapshot, "pg_orders.count").DataSource)

	// Only the two endpoints that return the data model. Never /v1/load.
	requests := s.server.Requests()
	s.Equal(map[string]int{
		cubetest.BasePath + "/v1/meta": 1,
		cubetest.BasePath + "/v1/sql":  254,
	}, requests)
}

func (s *CollectSuite) TestUnsetOptionalFields() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Empty(snapshot.CubeVersion)
	s.Empty(snapshot.SecurityContextLabel)
	s.WithinDuration(time.Now(), snapshot.CollectedAt, time.Minute)
}

func (s *CollectSuite) TestGroupedFallbackErrorsAndRetries() {
	s.server.GroupedOnlyMembers = map[string]bool{"orders.status_revenue_rank": true}
	s.server.ErrorMembers = map[string]string{"customers.lifetime_revenue": "Can't find join path to join 'customers', 'orders'"}
	s.server.ContinueWaitMembers = map[string]bool{"orders.count": true}
	s.server.UnavailableMembers = map[string]bool{"orders.revenue": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)

	s.Equal(254, report.Members)
	s.Equal(1, report.MemberErrors)
	s.Equal(1, report.GroupedMembers)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "customers.lifetime_revenue")
	s.Len(snapshot.MemberSQL, 254)

	grouped := s.memberSQL(snapshot, "orders.status_revenue_rank")
	s.False(grouped.Ungrouped)
	s.NotEmpty(grouped.SQL)
	s.Empty(grouped.Error)

	broken := s.memberSQL(snapshot, "customers.lifetime_revenue")
	s.Equal("Can't find join path to join 'customers', 'orders'", broken.Error)
	s.Empty(broken.SQL)
	s.Empty(broken.DataSource)
	s.False(broken.Ungrouped)

	// "Continue wait" and status 503 are sent again, and the ungrouped query then passes.
	for _, name := range []string{"orders.count", "orders.revenue"} {
		member := s.memberSQL(snapshot, name)
		s.True(member.Ungrouped, name)
		s.NotEmpty(member.SQL, name)
	}
	// 254 members, one more snapshot for each of: the grouped fallback, the two requests of the
	// broken member, "Continue wait" and status 503.
	s.Equal(254+1+1+1+1, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
}

func (s *CollectSuite) TestContinueWaitDoesNotUseTheRetries() {
	s.server.Meta = []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}]}]}`)
	s.server.ContinueWaitMembers = map[string]bool{"orders.count": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(cube.WithRetries(0)), cube.Options{})
	s.Require().NoError(err)
	s.Zero(report.MemberErrors)
	s.True(snapshot.MemberSQL[0].Ungrouped)
	s.Equal(2, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
}

func (s *CollectSuite) TestConcurrencyLimit() {
	s.server.SQLDelay = 2 * time.Millisecond

	_, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Concurrency: 3})
	s.Require().NoError(err)
	s.LessOrEqual(s.server.MaxConcurrentSQL(), 3)
	s.Greater(s.server.MaxConcurrentSQL(), 1)
}

func (s *CollectSuite) TestDefaultConcurrency() {
	s.server.SQLDelay = 2 * time.Millisecond

	_, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.LessOrEqual(s.server.MaxConcurrentSQL(), cube.DefaultConcurrency)
}

func (s *CollectSuite) TestMetaFailureFailsTheSnapshot() {
	s.server.MetaStatus = http.StatusForbidden

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().Error(err)
	s.Nil(snapshot)
	s.Nil(report)
	var apiError *cube.APIError
	s.Require().ErrorAs(err, &apiError)
	s.Equal(http.StatusForbidden, apiError.StatusCode)
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/sql"])
	// Status 403 does not pass on a retry.
	s.Equal(1, s.server.Requests()[cubetest.BasePath+"/v1/meta"])
}

func (s *CollectSuite) TestMetaIsRetriedOnServerError() {
	s.server.MetaStatus = http.StatusBadGateway

	_, _, err := cube.Collect(s.T().Context(), s.client(cube.WithRetries(2)), cube.Options{})
	s.Require().Error(err)
	s.Equal(3, s.server.Requests()[cubetest.BasePath+"/v1/meta"])
}

func (s *CollectSuite) TestMetaThatIsNotADataModel() {
	s.server.Meta = []byte(`{"unexpected": true}`)

	_, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().Error(err)
}

func (s *CollectSuite) TestWrongTokenMakesEachMemberAnErrorNotAFailure() {
	// The fake checks the token on /v1/meta too, so use a meta that passes and a model with
	// one member whose SQL snapshot fails with status 403.
	s.server.Meta = []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}],"dimensions":[]}]}`)
	s.server.ErrorMembers = map[string]string{"orders.count": "API scope is missing: sql"}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Equal(1, report.MemberErrors)
	s.Equal("API scope is missing: sql", snapshot.MemberSQL[0].Error)
}

func (s *CollectSuite) TestCancelledContext() {
	s.server.SQLDelay = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(s.T().Context(), 30*time.Millisecond)
	defer cancel()

	snapshot, _, err := cube.Collect(ctx, s.client(), cube.Options{Concurrency: 1})
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Nil(snapshot)
	s.Less(s.server.Requests()[cubetest.BasePath+"/v1/sql"], 254)
}

func (s *CollectSuite) TestRequestTimeoutOfOneMemberIsAMemberError() {
	s.server.Meta = []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}]}]}`)
	s.server.SQLDelay = 50 * time.Millisecond

	snapshot, report, err := cube.Collect(
		s.T().Context(),
		s.client(cube.WithRequestTimeout(5*time.Millisecond), cube.WithRetries(1)),
		cube.Options{},
	)
	s.Require().NoError(err)
	s.Equal(1, report.MemberErrors)
	s.NotEmpty(snapshot.MemberSQL[0].Error)
}
