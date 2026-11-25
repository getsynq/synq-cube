package cmd_test

import (
	"os"
	"path/filepath"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
)

// dataSources returns the data sources of a request as texts, for a short assertion.
func (s *UploadSuite) dataSources(request *ingestcubev1.IngestSnapshotRequest) []string {
	var out []string
	for _, source := range request.GetDataSources() {
		text := source.GetName() + "=" + source.GetType()
		if source.Database != nil {
			text += ",database=" + source.GetDatabase()
		}
		if source.Host != nil {
			text += ",host=" + source.GetHost()
		}
		if source.Project != nil {
			text += ",project=" + source.GetProject()
		}
		out = append(out, text)
	}
	return out
}

func (s *UploadSuite) TestNoDataSourceIsKnown() {
	s.setCollection()

	s.Require().NoError(s.run("upload"))

	s.Empty(s.service.requests[0].GetDataSources())
	s.Contains(s.stdout.String(), "Data sources that the snapshot does not describe: bigquery, clickhouse, default, postgres\n")
	s.NotContains(s.stdout.String(), "Data source default")
}

func (s *UploadSuite) TestDataSourcesFromTheEnvironmentOfCube() {
	s.setCollection()
	s.environ = []string{
		"CUBEJS_DB_TYPE=snowflake",
		"CUBEJS_DB_NAME=ANALYTICS",
		"CUBEJS_DB_SNOWFLAKE_ACCOUNT=ab12345.eu-west-1",
		"CUBEJS_DB_USER=the-user-of-cube",
		"CUBEJS_DB_PASS=the-password-of-cube",
		"CUBEJS_DATASOURCES=default,postgres,bigquery,warehouse_two",
		"CUBEJS_DS_POSTGRES_DB_TYPE=postgres",
		"CUBEJS_DS_POSTGRES_DB_HOST=db.internal.example.com",
		"CUBEJS_DS_POSTGRES_DB_PASS=the-password-of-postgres",
		"CUBEJS_DS_BIGQUERY_DB_TYPE=bigquery",
		"CUBEJS_DS_BIGQUERY_DB_BQ_PROJECT_ID=example-project",
		"CUBEJS_DS_BIGQUERY_DB_BQ_CREDENTIALS=the-credentials-of-bigquery",
		// No member uses this data source, so it is not in the snapshot.
		"CUBEJS_DS_WAREHOUSE_TWO_DB_TYPE=mysql",
	}
	output := filepath.Join(s.T().TempDir(), "snapshot.json")

	s.Require().NoError(s.run("upload", "--output", output))

	s.Equal([]string{
		"default=snowflake,database=ANALYTICS,host=ab12345.eu-west-1",
		"bigquery=bigquery,project=example-project",
		"postgres=postgres,host=db.internal.example.com",
	}, s.dataSources(s.service.requests[0]))

	out := s.stdout.String()
	s.Contains(out, "Data source default: snowflake, database ANALYTICS, host ab12345.eu-west-1 (from the environment of Cube)\n")
	s.Contains(out, "Data source bigquery: bigquery, project example-project (from the environment of Cube)\n")
	s.Contains(out, "Data source postgres: postgres, host db.internal.example.com (from the environment of Cube)\n")
	s.Contains(out, "Data sources that the snapshot does not describe: clickhouse\n")
	s.NotContains(out, "warehouse_two")

	// No credential of Cube is printed, written or sent.
	file, err := os.ReadFile(output)
	s.Require().NoError(err)
	sent := s.json(s.service.requests[0])
	for _, secret := range []string{"the-user-of-cube", "the-password-of-cube", "the-password-of-postgres", "the-credentials-of-bigquery"} {
		s.NotContains(out+s.stderr.String(), secret)
		s.NotContains(string(file), secret)
		s.NotContains(sent, secret)
	}
}

func (s *UploadSuite) TestNoDataSourcesFromEnv() {
	s.setCollection()
	s.environ = []string{"CUBEJS_DB_TYPE=snowflake"}

	s.Require().NoError(s.run("upload", "--no-data-sources-from-env"))
	s.Empty(s.service.requests[0].GetDataSources())

	s.T().Setenv("CUBE_NO_DATA_SOURCES_FROM_ENV", "true")
	s.Require().NoError(s.run("upload"))
	s.Empty(s.service.requests[1].GetDataSources())

	// The flag wins over the variable.
	s.Require().NoError(s.run("upload", "--no-data-sources-from-env=false"))
	s.Equal([]string{"default=snowflake"}, s.dataSources(s.service.requests[2]))

	s.T().Setenv("CUBE_NO_DATA_SOURCES_FROM_ENV", "maybe")
	err := s.run("upload")
	s.Require().Error(err)
	s.Contains(err.Error(), "CUBE_NO_DATA_SOURCES_FROM_ENV")
}

func (s *UploadSuite) TestTheDataSourceSettingWins() {
	s.setCollection()
	s.environ = []string{"CUBEJS_DB_TYPE=postgres", "CUBEJS_DB_NAME=from_env", "CUBEJS_DS_POSTGRES_DB_TYPE=postgres"}

	err := s.run("upload",
		"--data-source", "default=snowflake,database=ANALYTICS,host=ab12345.eu-west-1",
		"--data-source", "bigquery=bigquery,project=example-project")
	s.Require().NoError(err)

	s.Equal([]string{
		"default=snowflake,database=ANALYTICS,host=ab12345.eu-west-1",
		"bigquery=bigquery,project=example-project",
		"postgres=postgres",
	}, s.dataSources(s.service.requests[0]))
	s.Contains(s.stdout.String(), "Data source default: snowflake, database ANALYTICS, host ab12345.eu-west-1 (from --data-source)\n")
	s.Contains(s.stdout.String(), "Data source postgres: postgres (from the environment of Cube)\n")

	// The variable, with semicolons. The flag wins over it.
	s.T().Setenv("CUBE_DATA_SOURCES", "default=redshift,host=dwh.example.com ; clickhouse=clickhouse;")
	s.Require().NoError(s.run("upload"))
	s.Equal([]string{"default=redshift,host=dwh.example.com", "clickhouse=clickhouse", "postgres=postgres"}, s.dataSources(s.service.requests[1]))
	s.Require().NoError(s.run("upload", "--data-source", "default=snowflake"))
	s.Equal([]string{"default=snowflake", "postgres=postgres"}, s.dataSources(s.service.requests[2]))
}

func (s *UploadSuite) TestAnInvalidDataSourceFailsAtStartup() {
	s.setCollection()

	for _, text := range []string{"default", "default=snowflake,password=x", "=snowflake"} {
		err := s.run("upload", "--data-source", text)
		s.Require().Error(err, text)
		s.Contains(err.Error(), "data source")
	}
	err := s.run("upload", "--data-source", "default=snowflake", "--data-source", "default=postgres")
	s.Require().Error(err)
	s.Contains(err.Error(), "two times")

	s.Empty(s.cube.Requests())
	s.Empty(s.tokenExchanges)
}

func (s *UploadSuite) TestTheSnapshotFileKeepsTheDataSources() {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	file := filepath.Join(s.T().TempDir(), "snapshot.json")
	s.Require().NoError(s.run("upload", "--dry-run", "--output", file,
		"--data-source", "default=snowflake,database=ANALYTICS", "--data-source", "postgres=postgres,host=db.example.com"))
	s.T().Setenv("CUBE_API_URL", "")
	s.T().Setenv("CUBE_API_SECRET", "")
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	s.stdout.Reset()
	// The environment of Cube is not read for a file.
	s.environ = []string{"CUBEJS_DB_TYPE=mysql", "CUBEJS_DS_CLICKHOUSE_DB_TYPE=clickhouse"}

	// A filter keeps the list as it is.
	s.Require().NoError(s.run("upload", "--input", file, "--exclude", "pg_*"))

	want := []string{"default=snowflake,database=ANALYTICS", "postgres=postgres,host=db.example.com"}
	s.Equal(want, s.dataSources(s.readSnapshotFile(file)))
	s.Equal(want, s.dataSources(s.service.requests[0]))
	s.Contains(s.stdout.String(), "Data source default: snowflake, database ANALYTICS (from the snapshot file)\n")

	for _, args := range [][]string{{"--data-source", "default=postgres"}, {"--no-data-sources-from-env"}} {
		err := s.run(append([]string{"upload", "--input", file}, args...)...)
		s.Require().Error(err)
		s.Contains(err.Error(), "does not read Cube")
	}
}
