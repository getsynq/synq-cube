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

// The recording has 20 cubes and views. 19 have measures and 20 have dimensions, so a
// collection with the default batch size sends 39 `/v1/sql` requests.
const batchesOfTheRecording = 39

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

// generated returns the entry that holds the member, and the output column of the member.
func (s *CollectSuite) generated(snapshot *cube.Snapshot, name string) (cube.GeneratedSQL, string) {
	return findGenerated(&s.Suite, snapshot, name)
}

func findGenerated(s *suite.Suite, snapshot *cube.Snapshot, name string) (cube.GeneratedSQL, string) {
	for _, generated := range snapshot.GeneratedSQL {
		for _, member := range generated.Members {
			if member.Member == name {
				return generated, member.Column
			}
		}
	}
	s.FailNow("no SQL of the member " + name)
	return cube.GeneratedSQL{}, ""
}

// membersOf returns the members of all entries, in the order of the snapshot. It fails if a
// member is in two entries, or in an entry and in the errors.
func membersOf(s *suite.Suite, snapshot *cube.Snapshot) []string {
	var names []string
	seen := map[string]bool{}
	for _, generated := range snapshot.GeneratedSQL {
		s.NotEmpty(generated.SQL)
		s.NotEmpty(generated.Members)
		for _, member := range generated.Members {
			s.False(seen[member.Member], member.Member)
			seen[member.Member] = true
			s.NotEmpty(member.Column, member.Member)
			names = append(names, member.Member)
		}
	}
	for _, member := range snapshot.MemberErrors {
		s.False(seen[member.Member], member.Member)
		seen[member.Member] = true
		s.NotEmpty(member.Error, member.Member)
	}
	return names
}

func (s *CollectSuite) TestSnapshotOfTheRecording() {
	s.server.Authorization = "the-token"
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	modelPath := "model"
	clockReads := 0

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{
		SecurityContextLabel: "full-read",
		CubeVersion:          "1.8.30",
		Git:                  &cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"},
		ModelPath:            &modelPath,
		Now: func() time.Time {
			// The time of the snapshot is the start of the collection: no request was sent.
			clockReads++
			s.Empty(s.server.Requests())
			return at
		},
	})
	s.Require().NoError(err)
	s.Equal(1, clockReads)

	s.Equal(14, report.Cubes)
	s.Equal(6, report.Views)
	s.Equal(254, report.Members)
	s.Equal(254, report.MembersWithSQL)
	s.Equal(batchesOfTheRecording, report.Statements)
	s.Equal(batchesOfTheRecording, report.SQLRequests)
	s.Zero(report.MemberErrors)
	s.Zero(report.GroupedMembers)
	s.False(report.ModelExport)
	s.Empty(report.Warnings)

	s.Equal(cubetest.MetaExtended(), snapshot.DataModel)
	s.Nil(snapshot.ModelExport)
	s.Nil(snapshot.MemberErrors)
	s.Equal(at, snapshot.CollectedAt)
	s.Equal("full-read", snapshot.SecurityContextLabel)
	s.Equal("1.8.30", snapshot.CubeVersion)
	s.Equal(&cube.GitContext{CloneURL: "https://github.com/example/analytics", Branch: "main", CommitSHA: "0123abc"}, snapshot.Git)
	s.Require().NotNil(snapshot.ModelPath)
	s.Equal("model", *snapshot.ModelPath)

	// Each member of the recording is in one entry, with its output column and its data source.
	recorded := cubetest.Members()
	names := membersOf(&s.Suite, snapshot)
	s.Require().Len(names, len(recorded))
	s.Require().Len(snapshot.GeneratedSQL, batchesOfTheRecording)
	for _, want := range recorded {
		generated, column := s.generated(snapshot, want.Member)
		s.Equal(cubetest.Alias(want.Member), column)
		s.Equal(want.DataSource, generated.DataSource, want.Member)
		s.True(generated.Ungrouped, want.Member)
		s.Contains(generated.SQL, want.SQL, want.Member)
		// An entry holds members of one cube or view, all measures or all dimensions.
		for _, other := range generated.Members {
			s.Equal(want.Cube, memberOwner(other.Member), want.Member)
		}
	}

	// The members keep the order of the data model: the measures of a cube, then its dimensions.
	s.Equal("pg_products.count", names[0])
	revenue, _ := s.generated(snapshot, "orders_overview.revenue")
	s.Equal("default", revenue.DataSource)
	s.Contains(revenue.SQL, "DWH_TESTING")

	// Only the two endpoints that return the data model. Never /v1/load.
	s.Equal(map[string]int{
		cubetest.BasePath + "/v1/meta": 1,
		cubetest.BasePath + "/v1/sql":  batchesOfTheRecording,
	}, s.server.Requests())
	// A query of many members is in the body of a POST request. A query of one member is a
	// GET request.
	for _, request := range s.server.SQLRequests() {
		s.True(request.Ungrouped)
		if len(request.Members) > 1 {
			s.Equal(http.MethodPost, request.Method)
		} else {
			s.Equal(http.MethodGet, request.Method)
		}
	}
}

func memberOwner(member string) string {
	for i := range member {
		if member[i] == '.' {
			return member[:i]
		}
	}
	return member
}

func (s *CollectSuite) TestBatchSizeOneSendsOneRequestForEachMember() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{BatchSize: 1})
	s.Require().NoError(err)

	s.Equal(254, report.MembersWithSQL)
	s.Equal(254, report.Statements)
	s.Equal(254, report.SQLRequests)
	recorded := cubetest.Members()
	s.Require().Len(snapshot.GeneratedSQL, len(recorded))
	byName := map[string]cubetest.Member{}
	for _, member := range recorded {
		byName[member.Member] = member
	}
	for _, generated := range snapshot.GeneratedSQL {
		s.Require().Len(generated.Members, 1)
		want, found := byName[generated.Members[0].Member]
		s.Require().True(found, generated.Members[0].Member)
		s.Equal(want.SQL, generated.SQL, want.Member)
		s.Equal(want.DataSource, generated.DataSource, want.Member)
		s.Equal(cubetest.Alias(want.Member), generated.Members[0].Column)
		s.True(generated.Ungrouped, want.Member)
	}
	s.Equal("pg_products.count", snapshot.GeneratedSQL[0].Members[0].Member)
	s.Equal(254, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
	for _, request := range s.server.SQLRequests() {
		s.Equal(http.MethodGet, request.Method)
		s.Len(request.Members, 1)
	}
}

func (s *CollectSuite) TestBatchSizeSplitsALongList() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{BatchSize: 10})
	s.Require().NoError(err)

	s.Equal(254, report.MembersWithSQL)
	s.Len(membersOf(&s.Suite, snapshot), 254)
	for _, request := range s.server.SQLRequests() {
		s.LessOrEqual(len(request.Members), 10)
	}
	// orders has 37 measures and 11 dimensions: lists of 10, 10, 10 and 7 measures, then lists
	// of 10 and 1 dimensions, in the order of the data model.
	var sizes []int
	for _, generated := range snapshot.GeneratedSQL {
		if memberOwner(generated.Members[0].Member) == "orders" {
			sizes = append(sizes, len(generated.Members))
		}
	}
	s.Equal([]int{10, 10, 10, 7, 10, 1}, sizes)
}

// One member for which Cube generates no SQL breaks the query of its list. The collector splits
// the list, so only that member has an error.
func (s *CollectSuite) TestOneBadMemberDoesNotLoseTheOthers() {
	s.server.ErrorMembers = map[string]string{"orders.revenue": "Can't find join path to join 'orders', 'customers'"}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)

	s.Equal(254, report.Members)
	s.Equal(253, report.MembersWithSQL)
	s.Equal(1, report.MemberErrors)
	s.Zero(report.GroupedMembers)
	s.Equal([]cube.MemberError{{Member: "orders.revenue", Error: "Can't find join path to join 'orders', 'customers'"}}, snapshot.MemberErrors)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "orders.revenue: Cube generated no SQL: Can't find join path")
	names := membersOf(&s.Suite, snapshot)
	s.Len(names, 253)
	s.NotContains(names, "orders.revenue")

	// The member that is alone gets two requests, as in a collection of one member for each
	// request: an ungrouped query, then a query without `ungrouped`.
	var alone []cubetest.SQLRequest
	withTheMember := 0
	for _, request := range s.server.SQLRequests() {
		for _, member := range request.Members {
			if member != "orders.revenue" {
				continue
			}
			withTheMember++
			if len(request.Members) == 1 {
				alone = append(alone, request)
			}
		}
	}
	s.Require().Len(alone, 2)
	s.True(alone[0].Ungrouped)
	s.False(alone[1].Ungrouped)
	// 37 measures: five or six lists hold the member, each half the size of the list before
	// it. Then the member is alone.
	s.GreaterOrEqual(withTheMember, 5+2)
	s.LessOrEqual(withTheMember, 6+2)
	// The split costs few requests: far fewer than one for each member.
	s.Equal(report.SQLRequests, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
	s.Less(report.SQLRequests, batchesOfTheRecording+15)
	// The entries of orders keep the order of the data model.
	s.Equal("pg_products.count", names[0])
	position := map[string]int{}
	for i, name := range names {
		position[name] = i
	}
	s.Less(position["orders.count"], position["orders.customer_count"])
	s.Less(position["orders.customer_count"], position["orders.id"])
}

// A member that needs a query without `ungrouped` gets one alone. The other members of its list
// keep the ungrouped query.
func (s *CollectSuite) TestAGroupedOnlyMemberIsCollectedAlone() {
	s.server.GroupedOnlyMembers = map[string]bool{"orders.status_revenue_rank": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)

	s.Zero(report.MemberErrors)
	s.Equal(254, report.MembersWithSQL)
	s.Equal(1, report.GroupedMembers)
	grouped, column := s.generated(snapshot, "orders.status_revenue_rank")
	s.False(grouped.Ungrouped)
	s.Len(grouped.Members, 1)
	s.Equal("orders__status_revenue_rank", column)
	other, _ := s.generated(snapshot, "orders.count")
	s.True(other.Ungrouped)
	s.Greater(len(other.Members), 1)
	// No query of many members is a query without `ungrouped`.
	for _, request := range s.server.SQLRequests() {
		if len(request.Members) > 1 {
			s.True(request.Ungrouped)
		}
	}
}

func (s *CollectSuite) TestAMemberWithNoColumnInTheResponseIsCollectedAlone() {
	s.server.NoAliasMembers = map[string]bool{"orders.revenue": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)

	s.Zero(report.MemberErrors)
	s.Equal(254, report.MembersWithSQL)
	s.Equal(batchesOfTheRecording+1, report.Statements)
	s.Equal(batchesOfTheRecording+1, report.SQLRequests)
	alone, column := s.generated(snapshot, "orders.revenue")
	s.Len(alone.Members, 1)
	// The column is the key of the response of the member alone.
	s.Equal("orders__revenue", column)
	s.True(alone.Ungrouped)
	others, _ := s.generated(snapshot, "orders.count")
	s.Len(others.Members, 36)
	s.Len(membersOf(&s.Suite, snapshot), 254)
}

// A Cube that names no output column gives statements that nothing can read: nothing says
// which output column is which member. Each member then has an error, and the collector builds
// no column name from the name of the member.
func (s *CollectSuite) TestACubeThatNamesNoColumnGivesAnErrorForEachMember() {
	s.server.NoAliases = true

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Concurrency: 1})
	s.Require().NoError(err)

	s.Zero(report.MembersWithSQL)
	s.Equal(254, report.MemberErrors)
	s.Nil(snapshot.GeneratedSQL)
	s.Require().Len(snapshot.MemberErrors, 254)
	for _, member := range snapshot.MemberErrors {
		s.Contains(member.Error, "aliasNameToMember", member.Member)
	}
	s.Equal("pg_products.count", snapshot.MemberErrors[0].Member)
	// One query of many members, then an ungrouped query and a query without `ungrouped` for
	// each member.
	s.Equal(1+2*254, report.SQLRequests)
}

// A Cube that does not serve POST /v1/sql gets the query of many members in a GET request. A
// URL that is too long is an error of the list, so the collector splits the list.
func (s *CollectSuite) TestACubeWithNoPostGetsTheQueryInTheURL() {
	s.server.NoPostSQL = true
	s.server.MaxURLLength = 600

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Concurrency: 1})
	s.Require().NoError(err)

	s.Zero(report.MemberErrors)
	s.Equal(254, report.MembersWithSQL)
	s.Len(membersOf(&s.Suite, snapshot), 254)
	s.Less(report.Statements, 254)
	// The fake answered each query in a GET request. The count of the report has the POST
	// request and the requests with a URL that was too long too.
	s.Equal(report.SQLRequests, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
	s.Greater(report.SQLRequests, len(s.server.SQLRequests()))
	for _, request := range s.server.SQLRequests() {
		s.Equal(http.MethodGet, request.Method)
	}
}

func (s *CollectSuite) TestUnsetOptionalFields() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Empty(snapshot.CubeVersion)
	s.Empty(snapshot.SecurityContextLabel)
	s.Nil(snapshot.Git)
	s.Nil(snapshot.ModelPath)
	s.WithinDuration(time.Now(), snapshot.CollectedAt, time.Minute)
}

func (s *CollectSuite) TestGroupedFallbackErrorsAndRetries() {
	s.server.GroupedOnlyMembers = map[string]bool{"orders.status_revenue_rank": true}
	s.server.ErrorMembers = map[string]string{"customers.lifetime_revenue": "Can't find join path to join 'customers', 'orders'"}
	s.server.ContinueWaitMembers = map[string]bool{"orders.count": true}
	s.server.UnavailableMembers = map[string]bool{"orders.revenue": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{BatchSize: 1})
	s.Require().NoError(err)

	s.Equal(254, report.Members)
	s.Equal(253, report.MembersWithSQL)
	s.Equal(1, report.MemberErrors)
	s.Equal(1, report.GroupedMembers)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "customers.lifetime_revenue")
	s.Len(snapshot.GeneratedSQL, 253)

	grouped, _ := s.generated(snapshot, "orders.status_revenue_rank")
	s.False(grouped.Ungrouped)
	s.NotEmpty(grouped.SQL)

	s.Equal([]cube.MemberError{{Member: "customers.lifetime_revenue", Error: "Can't find join path to join 'customers', 'orders'"}}, snapshot.MemberErrors)

	// "Continue wait" and status 503 are sent again, and the ungrouped query then passes.
	for _, name := range []string{"orders.count", "orders.revenue"} {
		generated, _ := s.generated(snapshot, name)
		s.True(generated.Ungrouped, name)
		s.NotEmpty(generated.SQL, name)
	}
	// 254 members, one more request for each of: the grouped fallback, the second request of
	// the broken member, "Continue wait" and status 503.
	s.Equal(254+1+1+1+1, s.server.Requests()[cubetest.BasePath+"/v1/sql"])
	s.Equal(254+1+1+1+1, report.SQLRequests)
}

// A request of many members that Cube still works on, or that gets status 503, is sent again
// and is not split.
func (s *CollectSuite) TestAListIsSentAgainAfterContinueWaitAndStatus503() {
	s.server.ContinueWaitMembers = map[string]bool{"orders.count": true}
	s.server.UnavailableMembers = map[string]bool{"customers.count": true}

	_, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Equal(254, report.MembersWithSQL)
	s.Equal(batchesOfTheRecording, report.Statements)
	s.Equal(batchesOfTheRecording+2, report.SQLRequests)
}

func (s *CollectSuite) TestContinueWaitDoesNotUseTheRetries() {
	s.server.Meta = []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"}]}]}`)
	s.server.ContinueWaitMembers = map[string]bool{"orders.count": true}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(cube.WithRetries(0)), cube.Options{})
	s.Require().NoError(err)
	s.Zero(report.MemberErrors)
	s.True(snapshot.GeneratedSQL[0].Ungrouped)
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
	// The fake checks the token on /v1/meta too, so use a meta that passes and a model whose
	// SQL requests fail.
	s.server.Meta = []byte(`{"cubes":[{"name":"orders","type":"cube","measures":[{"name":"orders.count"},{"name":"orders.revenue"}],"dimensions":[]}]}`)
	s.server.ErrorMembers = map[string]string{"orders.count": "API scope is missing: sql", "orders.revenue": "API scope is missing: sql"}

	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Equal(2, report.MemberErrors)
	s.Zero(report.MembersWithSQL)
	s.Nil(snapshot.GeneratedSQL)
	s.Equal([]cube.MemberError{
		{Member: "orders.count", Error: "API scope is missing: sql"},
		{Member: "orders.revenue", Error: "API scope is missing: sql"},
	}, snapshot.MemberErrors)
}

func (s *CollectSuite) TestCancelledContext() {
	s.server.SQLDelay = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(s.T().Context(), 30*time.Millisecond)
	defer cancel()

	snapshot, _, err := cube.Collect(ctx, s.client(), cube.Options{Concurrency: 1})
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Nil(snapshot)
	s.Less(s.server.Requests()[cubetest.BasePath+"/v1/sql"], batchesOfTheRecording)
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
	s.NotEmpty(snapshot.MemberErrors[0].Error)
}
