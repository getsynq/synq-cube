package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/getsynq/synq-cube/cube/cubemeta"
	"github.com/getsynq/synq-cube/cube/cubetest"
)

// writeSnapshotFile collects from the fake Cube and writes the snapshot file. It then removes
// the Cube settings, so a later run cannot read Cube.
func (s *UploadSuite) writeSnapshotFile() string {
	s.T().Setenv("CUBE_API_URL", s.cube.APIURL())
	s.T().Setenv("CUBE_API_SECRET", apiSecret)
	s.T().Setenv("CUBE_VERSION", "1.8.30")
	file := filepath.Join(s.T().TempDir(), "snapshot.json")
	s.Require().NoError(s.run("upload", "--dry-run", "--output", file, "--security-context-label", "full-read"))
	s.T().Setenv("CUBE_API_URL", "")
	s.T().Setenv("CUBE_API_SECRET", "")
	s.T().Setenv("CUBE_VERSION", "")
	s.stdout.Reset()
	s.stderr.Reset()
	return file
}

func (s *UploadSuite) readSnapshotFile(file string) *ingestcubev1.IngestSnapshotRequest {
	data, err := os.ReadFile(file)
	s.Require().NoError(err)
	request := &ingestcubev1.IngestSnapshotRequest{}
	s.Require().NoError(protojson.Unmarshal(data, request))
	return request
}

func (s *UploadSuite) writeRequest(request *ingestcubev1.IngestSnapshotRequest) string {
	data, err := protojson.Marshal(request)
	s.Require().NoError(err)
	file := filepath.Join(s.T().TempDir(), "edited.json")
	s.Require().NoError(os.WriteFile(file, data, 0o600))
	return file
}

func (s *UploadSuite) names(request *ingestcubev1.IngestSnapshotRequest) []string {
	meta, err := cubemeta.Parse(request.GetDataModel())
	s.Require().NoError(err)
	names := make([]string, 0, len(meta.Cubes))
	for _, c := range meta.Cubes {
		names = append(names, c.Name)
	}
	return names
}

func (s *UploadSuite) TestInputSendsTheSnapshotOfTheFile() {
	file := s.writeSnapshotFile()
	written := s.readSnapshotFile(file)
	// The uploader fields of a file are not sent: the command sends its own.
	otherVersion, otherTime := "9.9.9", "1999-01-01T00:00:00Z"
	written.UploaderVersion, written.UploaderBuildTime = &otherVersion, &otherTime
	file = s.writeRequest(written)
	requestsToCube := s.cube.Requests()
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	s.Require().NoError(s.run("upload", "--input", file))

	// No request to Cube, and no Cube setting was needed.
	s.Equal(requestsToCube, s.cube.Requests())
	s.Require().Len(s.service.requests, 1)
	sent := s.service.requests[0]
	s.NotEqual("9.9.9", sent.GetUploaderVersion())
	s.NotEmpty(sent.GetUploaderVersion())
	s.NotEqual("1999-01-01T00:00:00Z", sent.GetUploaderBuildTime())

	// But for the uploader fields, the request is the file, byte for byte.
	want := proto.Clone(written).(*ingestcubev1.IngestSnapshotRequest)
	want.UploaderVersion, want.UploaderBuildTime = sent.UploaderVersion, sent.UploaderBuildTime
	wantBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(want)
	s.Require().NoError(err)
	sentBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(sent)
	s.Require().NoError(err)
	s.True(bytes.Equal(wantBytes, sentBytes))
	s.Equal(cubetest.MetaExtended(), sent.GetDataModel())
	s.Equal("1.8.30", sent.GetCubeVersion())
	s.Equal("full-read", sent.GetSecurityContextLabel())

	// The same summary as after a collection, counted from the file.
	out := s.stdout.String()
	s.Contains(out, "Reading the snapshot from "+file)
	s.Contains(out, "Cubes: 14\n")
	s.Contains(out, "Views: 6\n")
	s.Contains(out, "Members: 254\n")
	s.Contains(out, "Members with an error: 0\n")
	s.Contains(out, "Sent the snapshot to Coalesce Quality")
	s.NotContains(out, "Left out")
}

func (s *UploadSuite) TestInputFromTheStandardInput() {
	file := s.writeSnapshotFile()
	data, err := os.ReadFile(file)
	s.Require().NoError(err)
	s.stdin = bytes.NewReader(data)
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	s.Require().NoError(s.run("upload", "--input", "-"))
	s.Require().Len(s.service.requests, 1)
	s.Len(s.sqlMembers(s.service.requests[0]), 254)
	s.Contains(s.stdout.String(), "Reading the snapshot from the standard input")
}

func (s *UploadSuite) TestInputWithDryRunOnlyChecksTheFile() {
	file := s.writeSnapshotFile()
	before, err := os.ReadFile(file)
	s.Require().NoError(err)

	// No token of Coalesce Quality and no --output.
	s.Require().NoError(s.run("upload", "--input", file, "--dry-run"))
	s.Empty(s.tokenExchanges)
	s.Empty(s.service.requests)
	s.Contains(s.stdout.String(), "Members: 254\n")
	s.Contains(s.stdout.String(), "Dry run: nothing was sent")
	after, err := os.ReadFile(file)
	s.Require().NoError(err)
	s.Equal(before, after)
}

func (s *UploadSuite) TestInputWithOutputWritesTheFileAgain() {
	file := s.writeSnapshotFile()
	written := s.readSnapshotFile(file)
	otherVersion := "9.9.9"
	written.UploaderVersion = &otherVersion
	// A file in one line, as a tool can write it.
	compact := s.writeRequest(written)

	s.Require().NoError(s.run("upload", "--input", compact, "--output", compact, "--dry-run"))

	data, err := os.ReadFile(compact)
	s.Require().NoError(err)
	s.Greater(strings.Count(string(data), "\n"), 254)
	again := s.readSnapshotFile(compact)
	s.NotEqual("9.9.9", again.GetUploaderVersion())
	again.UploaderVersion, again.UploaderBuildTime = written.UploaderVersion, written.UploaderBuildTime
	s.True(proto.Equal(written, again))
}

func (s *UploadSuite) TestInputWithAFilterCutsTheFileDown() {
	file := s.writeSnapshotFile()
	requestsToCube := s.cube.Requests()
	smaller := filepath.Join(s.T().TempDir(), "smaller.json")

	err := s.run("upload", "--input", file, "--exclude", "pg_*", "--exclude", "calendar", "--skip-hidden", "--dry-run", "--output", smaller)
	s.Require().NoError(err)

	s.Equal(requestsToCube, s.cube.Requests())
	out := s.stdout.String()
	s.Contains(out, "Cubes: 8\n")
	s.Contains(out, "Views: 4\n")
	s.Contains(out, "Left out by --include and --exclude: cubes 5, views 1, members 36\n")
	s.Contains(out, "Left out by --skip-hidden: cubes 1, views 1, members ")
	s.Contains(s.stderr.String(), "warning: the view orders_overview includes members of calendar")

	cut := s.readSnapshotFile(smaller)
	names := s.names(cut)
	s.Len(names, 12)
	for _, name := range names {
		s.False(strings.HasPrefix(name, "pg_") || name == "calendar" || name == "events_internal" || name == "product_snapshots", name)
	}
	for _, member := range s.sqlMembers(cut) {
		owner, _, _ := strings.Cut(member, ".")
		s.Contains(names, owner, member)
	}
	s.Less(len(s.sqlMembers(cut)), 254-36)
	// --skip-hidden removed members from a statement that stays. Its SQL text did not change.
	whole := s.readSnapshotFile(file)
	shorter := 0
	for _, generated := range cut.GetGeneratedSql() {
		for _, before := range whole.GetGeneratedSql() {
			if before.GetSql() == generated.GetSql() && len(generated.GetMembers()) < len(before.GetMembers()) {
				shorter++
			}
		}
	}
	s.Positive(shorter)
	// The file that was read is not changed.
	s.Len(s.sqlMembers(whole), 254)

	// The file that was cut down can be sent.
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	s.Require().NoError(s.run("upload", "--input", smaller, "--skip-member-sql"))
	s.Require().Len(s.service.requests, 1)
	s.Equal(names, s.names(s.service.requests[0]))
	s.Empty(s.service.requests[0].GetGeneratedSql())
	s.Contains(s.stdout.String(), "Left out by --skip-member-sql")
}

func (s *UploadSuite) TestInputWithAFilterThatLeavesNothing() {
	file := s.writeSnapshotFile()
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	err := s.run("upload", "--input", file, "--include", "no_such_*")
	s.Require().Error(err)
	s.Contains(err.Error(), "leave no cube and no view")
	s.Empty(s.service.requests)
}

func (s *UploadSuite) TestInputRefusesTheSettingsOfACollection() {
	file := s.writeSnapshotFile()
	s.T().Setenv("QUALITY_TOKEN", integrationToken)

	for _, args := range [][]string{
		{"--cube-api-url", s.cube.APIURL()},
		{"--concurrency", "2"},
		{"--security-context-label", "other"},
		{"--cube-version", "2.0.0"},
		{"--request-timeout", "5s"},
	} {
		err := s.run(append([]string{"upload", "--input", file}, args...)...)
		s.Require().Error(err, args[0])
		s.Contains(err.Error(), args[0])
		s.Contains(err.Error(), "does not read Cube")
	}
	s.Empty(s.service.requests)
	s.Empty(s.tokenExchanges)
}

func (s *UploadSuite) TestInputRefusesABrokenFile() {
	valid := s.readSnapshotFile(s.writeSnapshotFile())
	s.T().Setenv("QUALITY_TOKEN", integrationToken)
	dir := s.T().TempDir()

	noMeta := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	noMeta.DataModel = nil
	noCubes := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	noCubes.DataModel = []byte(`{"cubes": "none"}`)
	notJSON := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	notJSON.DataModel = []byte(`not JSON`)
	noTime := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	noTime.CollectedAt = nil
	noColumn := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	noColumn.GetGeneratedSql()[0].GetMembers()[0].Column = ""
	noMembers := proto.Clone(valid).(*ingestcubev1.IngestSnapshotRequest)
	noMembers.GetGeneratedSql()[0].Members = nil

	for name, test := range map[string]struct {
		content string
		message string
	}{
		"cut off":          {content: `{"dataModel": "e30=", "generatedSql": [`, message: "is not a snapshot file"},
		"not a snapshot":   {content: `[1, 2, 3]`, message: "is not a snapshot file"},
		"empty":            {content: ``, message: "is not a snapshot file"},
		"unknown field":    {content: `{"dataModel": "eyJjdWJlcyI6W119", "collectedAt": "2026-03-04T05:06:07Z", "generatedSQL": []}`, message: "generatedSQL"},
		"older format":     {content: `{"meta": "eyJjdWJlcyI6W119", "collectedAt": "2026-03-04T05:06:07Z", "memberSql": []}`, message: "is not a snapshot file"},
		"no meta":          {content: s.json(noMeta), message: `it has no "dataModel"`},
		"no column":        {content: s.json(noColumn), message: `no "member" or no "column"`},
		"no members":       {content: s.json(noMembers), message: `no "sql" or no "members"`},
		"no cubes list":    {content: s.json(noCubes), message: `"cubes" list`},
		"meta is not JSON": {content: s.json(notJSON), message: `"cubes" list`},
		"no collected at":  {content: s.json(noTime), message: `it has no "collectedAt"`},
	} {
		file := filepath.Join(dir, "broken.json")
		s.Require().NoError(os.WriteFile(file, []byte(test.content), 0o600))
		err := s.run("upload", "--input", file)
		s.Require().Error(err, name)
		s.Contains(err.Error(), test.message, name)
	}
	err := s.run("upload", "--input", filepath.Join(dir, "no-such-file.json"))
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot read the snapshot")

	// A broken file fails before the connection to Coalesce Quality.
	s.Empty(s.tokenExchanges)
	s.Empty(s.service.requests)
}

func (s *UploadSuite) json(request *ingestcubev1.IngestSnapshotRequest) string {
	data, err := protojson.Marshal(request)
	s.Require().NoError(err)
	return string(data)
}
