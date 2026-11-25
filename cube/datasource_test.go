package cube_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/getsynq/synq-cube/cube"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// The members of the recording use four data sources: default, postgres, clickhouse and
// bigquery.
type DataSourceSuite struct {
	suite.Suite
	server *cubetest.Server
}

func TestDataSourceSuite(t *testing.T) {
	suite.Run(t, new(DataSourceSuite))
}

func (s *DataSourceSuite) SetupTest() {
	s.server = cubetest.NewServer()
	s.T().Cleanup(s.server.Close)
}

func (s *DataSourceSuite) client() *cube.Client {
	tokens, err := cube.NewStaticTokenSource("the-token")
	s.Require().NoError(err)
	client, err := cube.NewClient(s.server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)
	return client
}

func (s *DataSourceSuite) cloudClient() *cube.Client {
	cloud := newFakeCloud(s.server.APIURL() + "/v1")
	s.T().Cleanup(cloud.Close)
	s.server.Authorization = "Bearer " + cloud.token
	tokens, err := cube.NewCloudTokenSource(cube.CloudConfig{URL: cloud.URL, APIKey: "platform-key", DeploymentID: 7})
	s.Require().NoError(err)
	client, err := cube.NewClient(s.server.APIURL(), tokens, cube.WithRetryBackoff(time.Millisecond))
	s.Require().NoError(err)
	return client
}

func (s *DataSourceSuite) usedInTheRecording() map[string]bool {
	used := map[string]bool{}
	for _, member := range cubetest.Members() {
		used[member.DataSource] = true
	}
	s.Require().True(used["default"])
	s.Require().True(used["postgres"])
	s.Require().Greater(len(used), 2)
	return used
}

func (s *DataSourceSuite) TestFromEnv() {
	sources := cube.DataSourcesFromEnv([]string{
		"PATH=/usr/bin",
		"CUBEJS_DB_TYPE=snowflake",
		"CUBEJS_DB_NAME=ANALYTICS",
		"CUBEJS_DB_HOST=ignored.example.com",
		"CUBEJS_DB_SNOWFLAKE_ACCOUNT=ab12345.eu-west-1",
		"CUBEJS_DATASOURCES=default,Postgres,bq_events",
		"CUBEJS_DS_POSTGRES_DB_TYPE=postgres",
		"CUBEJS_DS_POSTGRES_DB_NAME=shop",
		"CUBEJS_DS_POSTGRES_DB_HOST=db.internal.example.com",
		"CUBEJS_DS_BQ_EVENTS_DB_TYPE=bigquery",
		"CUBEJS_DS_BQ_EVENTS_DB_BQ_PROJECT_ID=example-project",
		"CUBEJS_DS_NOT_LISTED_DB_TYPE= clickhouse ",
		"CUBEJS_DS_EMPTY_DB_TYPE=",
		"CUBEJS_DS__DB_TYPE=nameless",
		"CUBEJS_DS_DB_TYPE=nameless",
		"NOT_CUBEJS_DB_TYPE=mysql",
	})

	s.Equal([]cube.DataSource{
		{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1"},
		// The spelling of CUBEJS_DATASOURCES.
		{Name: "Postgres", Type: "postgres", Database: "shop", Host: "db.internal.example.com"},
		{Name: "bq_events", Type: "bigquery", Project: "example-project"},
		// Not in CUBEJS_DATASOURCES: lower case.
		{Name: "not_listed", Type: "clickhouse"},
	}, sources)

	s.Nil(cube.DataSourcesFromEnv(nil))
	s.Nil(cube.DataSourcesFromEnv([]string{"HOME=/root", "CUBEJS_API_SECRET=secret"}))
}

// The function reads no credential. Each value here is a marker, and no marker is in the result.
func (s *DataSourceSuite) TestFromEnvReadsNoCredential() {
	var environ []string
	var markers []string
	add := func(name string) {
		marker := fmt.Sprintf("CREDENTIAL-%d", len(markers))
		markers = append(markers, marker)
		environ = append(environ, name+"="+marker)
	}
	for _, prefix := range []string{"CUBEJS_", "CUBEJS_DS_POSTGRES_", "CUBEJS_DS_DB_TYPE_"} {
		for _, name := range []string{
			"DB_USER", "DB_PASS", "DB_PASSWORD", "DB_URL", "DB_SSL_KEY", "DB_SSL_CERT", "DB_SSL_CA", "DB_SSL_PASSPHRASE",
			"DB_BQ_CREDENTIALS", "DB_BQ_KEY_FILE", "DB_SNOWFLAKE_PRIVATE_KEY", "DB_SNOWFLAKE_PRIVATE_KEY_PASS",
			"DB_SNOWFLAKE_PRIVATE_KEY_PATH", "DB_SNOWFLAKE_OAUTH_TOKEN", "DB_SNOWFLAKE_AUTHENTICATOR", "DB_SNOWFLAKE_ROLE",
			"DB_SNOWFLAKE_WAREHOUSE", "DB_DATABRICKS_TOKEN", "DB_DATABRICKS_URL", "DB_EXPORT_BUCKET_AWS_KEY",
			"DB_EXPORT_BUCKET_AWS_SECRET", "DB_TOKEN", "DB_SECRET", "DB_KEY", "DB_PORT", "DB_SCHEMA",
		} {
			add(prefix + name)
		}
	}
	for _, name := range []string{"CUBEJS_API_SECRET", "CUBEJS_JWT_KEY", "CUBEJS_CUBESTORE_HOST", "CUBEJS_REDIS_PASSWORD", "DB_TYPE", "DB_NAME"} {
		add(name)
	}
	// Two variables that the function reads, so the result is not empty.
	environ = append(environ, "CUBEJS_DB_TYPE=postgres", "CUBEJS_DS_POSTGRES_DB_NAME=shop")

	sources := cube.DataSourcesFromEnv(environ)

	s.Equal([]cube.DataSource{
		{Name: "default", Type: "postgres"},
		{Name: "postgres", Database: "shop"},
	}, sources)
	text := fmt.Sprintf("%+v", sources)
	for _, marker := range markers {
		s.NotContains(text, marker)
	}
}

func (s *DataSourceSuite) TestParseDataSource() {
	source, err := cube.ParseDataSource("default=snowflake, database=ANALYTICS ,host=ab12345.eu-west-1")
	s.Require().NoError(err)
	s.Equal(cube.DataSource{Name: "default", Type: "snowflake", Database: "ANALYTICS", Host: "ab12345.eu-west-1"}, source)

	source, err = cube.ParseDataSource("bq=bigquery,project=example-project")
	s.Require().NoError(err)
	s.Equal(cube.DataSource{Name: "bq", Type: "bigquery", Project: "example-project"}, source)

	for _, text := range []string{"", "default", "=snowflake", "default=", "default=snowflake,password=x", "default=snowflake,database", "default=snowflake,host="} {
		_, err := cube.ParseDataSource(text)
		s.Error(err, text)
	}
}

func (s *DataSourceSuite) TestCollectWithNoDataSources() {
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{})
	s.Require().NoError(err)
	s.Nil(snapshot.DataSources)
	s.Nil(report.DataSourceOrigins)
	s.Empty(report.Warnings)
	// A deployment that is not on Cube Cloud gets no request for the data sources.
	s.Zero(s.server.Requests()[cubetest.BasePath+"/v1/data-sources"])
}

func (s *DataSourceSuite) TestCollectKeepsTheDataSourcesThatTheMembersUse() {
	s.usedInTheRecording()
	snapshot, report, err := cube.Collect(s.T().Context(), s.client(), cube.Options{
		EnvironmentDataSources: []cube.DataSource{
			{Name: "default", Type: "snowflake", Database: "FROM_ENV"},
			{Name: "postgres", Type: "postgres", Host: "db.example.com"},
			{Name: "not_used", Type: "mysql"},
		},
		DataSources: []cube.DataSource{
			{Name: "default", Type: "snowflake", Database: "FROM_SETTING"},
			{Name: "also_not_used", Type: "mysql"},
		},
	})
	s.Require().NoError(err)

	s.Equal([]cube.DataSource{
		// The setting wins over the environment, as a whole entry.
		{Name: "default", Type: "snowflake", Database: "FROM_SETTING"},
		{Name: "postgres", Type: "postgres", Host: "db.example.com"},
	}, snapshot.DataSources)
	s.Equal(map[string]cube.DataSourceOrigin{
		"default":  cube.OriginSetting,
		"postgres": cube.OriginEnvironment,
	}, report.DataSourceOrigins)
}

func (s *DataSourceSuite) TestDefaultStaysAlsoIfNoMemberUsesIt() {
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{
		Filter:      cube.Filter{Include: []string{"pg_*"}},
		DataSources: []cube.DataSource{{Name: "default", Type: "snowflake"}, {Name: "postgres", Type: "postgres"}, {Name: "clickhouse", Type: "clickhouse"}},
	})
	s.Require().NoError(err)
	s.Equal([]cube.DataSource{{Name: "default", Type: "snowflake"}, {Name: "postgres", Type: "postgres"}}, snapshot.DataSources)
}

func (s *DataSourceSuite) TestEachDataSourceStaysIfTheMembersDoNotSayWhatIsInUse() {
	sources := []cube.DataSource{{Name: "default", Type: "snowflake"}, {Name: "other", Type: "postgres"}}
	snapshot, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{Filter: cube.Filter{SkipMemberSQL: true}, DataSources: sources})
	s.Require().NoError(err)
	s.Equal(sources, snapshot.DataSources)
}

func (s *DataSourceSuite) TestCubeCloudDataSources() {
	// The shape of the response of the Metadata API.
	s.server.DataSources = []byte(`{"data":{"data_sources":[
		{"name":"default","type":"snowflake","database":"DWH_TESTING"},
		{"name":"bigquery","type":"bigquery","properties":{"project_id":"example-project"}},
		{"name":"not_used","type":"mysql"},
		{"type":"nameless"}
	]}}`)
	s.server.ModelExport = []byte(`{"data":[],"pagination":{"total":0}}`)
	s.Require().True(s.usedInTheRecording()["bigquery"])

	snapshot, report, err := cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{
		EnvironmentDataSources: []cube.DataSource{
			{Name: "default", Type: "postgres", Database: "from_env"},
			{Name: "postgres", Type: "postgres", Host: "db.example.com"},
		},
		DataSources: []cube.DataSource{{Name: "bigquery", Type: "bigquery", Project: "other-project"}},
	})
	s.Require().NoError(err)

	s.Equal([]cube.DataSource{
		{Name: "default", Type: "snowflake", Database: "DWH_TESTING"},
		{Name: "bigquery", Type: "bigquery", Project: "other-project"},
		{Name: "postgres", Type: "postgres", Host: "db.example.com"},
	}, snapshot.DataSources)
	s.Equal(map[string]cube.DataSourceOrigin{
		"default":  cube.OriginCubeCloud,
		"bigquery": cube.OriginSetting,
		"postgres": cube.OriginEnvironment,
	}, report.DataSourceOrigins)
	s.Empty(report.Warnings)
	s.Equal(1, s.server.Requests()[cubetest.BasePath+"/v1/data-sources"])
}

func (s *DataSourceSuite) TestAFailedReadOfCubeCloudIsAWarning() {
	s.server.ModelExport = []byte(`{"data":[],"pagination":{"total":0}}`)

	// The fake answers /v1/data-sources with status 404.
	snapshot, report, err := cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{
		EnvironmentDataSources: []cube.DataSource{{Name: "default", Type: "snowflake"}},
	})
	s.Require().NoError(err)
	s.Require().Len(report.Warnings, 1)
	s.Contains(report.Warnings[0], "the data sources of Cube Cloud are not in the snapshot")
	// The other origins still apply.
	s.Equal([]cube.DataSource{{Name: "default", Type: "snowflake"}}, snapshot.DataSources)

	s.server.DataSources = []byte(`{"unexpected": true}`)
	snapshot, report, err = cube.Collect(s.T().Context(), s.cloudClient(), cube.Options{})
	s.Require().NoError(err)
	s.Nil(snapshot.DataSources)
	s.Require().Len(report.Warnings, 1)
}

func (s *DataSourceSuite) TestApplyKeepsTheDataSources() {
	sources := []cube.DataSource{{Name: "default", Type: "snowflake"}, {Name: "postgres", Type: "postgres"}}
	whole, _, err := cube.Collect(s.T().Context(), s.client(), cube.Options{DataSources: sources})
	s.Require().NoError(err)

	applied, _, err := cube.Filter{Exclude: []string{"pg_*"}, SkipMemberSQL: true}.Apply(whole)
	s.Require().NoError(err)
	s.Equal(sources, applied.DataSources)
}
