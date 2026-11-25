package quality

import (
	"time"

	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	ingestgitv1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/git/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsynq/synq-cube/cube"
)

// NewRequest builds the message of the upload from a snapshot of the collector.
// uploaderVersion and uploaderBuildTime name the build of the tool that sends it.
//
// The package cube imports no generated proto package, so this file is the one place that maps
// a snapshot to the message and back. A new field of cube.Snapshot needs a line in NewRequest
// and in NewSnapshot. TestNewRequestMapsEachField fails until it has them.
//
// The message follows the rules of the proto file: an optional field with an empty value is not
// set, the error text of a member is never empty, and the build time is in RFC 3339 format or
// not set. TestACollectedSnapshotIsAValidMessage checks a message against the rules.
func NewRequest(snapshot *cube.Snapshot, uploaderVersion string, uploaderBuildTime string) *ingestcubev1.IngestSnapshotRequest {
	request := &ingestcubev1.IngestSnapshotRequest{DataModel: snapshot.DataModel}
	if len(snapshot.ModelExport) > 0 {
		request.ModelExport = snapshot.ModelExport
	}
	if uploaderVersion != "" {
		request.UploaderVersion = &uploaderVersion
	}
	if buildTime := rfc3339(uploaderBuildTime); buildTime != "" {
		request.UploaderBuildTime = &buildTime
	}
	if len(snapshot.GeneratedSQL) > 0 {
		request.GeneratedSql = make([]*ingestcubev1.GeneratedSql, 0, len(snapshot.GeneratedSQL))
	}
	for _, generated := range snapshot.GeneratedSQL {
		// The value of `ungrouped` is always set: a statement with no value reads as a grouped one.
		ungrouped := generated.Ungrouped
		message := &ingestcubev1.GeneratedSql{
			Sql:       generated.SQL,
			Members:   make([]*ingestcubev1.GeneratedSqlMember, 0, len(generated.Members)),
			Ungrouped: &ungrouped,
		}
		for _, member := range generated.Members {
			message.Members = append(message.Members, &ingestcubev1.GeneratedSqlMember{Member: member.Member, Column: member.Column})
		}
		if generated.DataSource != "" {
			message.DataSource = &generated.DataSource
		}
		request.GeneratedSql = append(request.GeneratedSql, message)
	}
	for _, member := range snapshot.MemberErrors {
		text := member.Error
		if text == "" {
			text = noErrorText
		}
		request.MemberErrors = append(request.MemberErrors, &ingestcubev1.MemberError{Member: member.Member, Error: text})
	}
	for _, source := range snapshot.DataSources {
		message := &ingestcubev1.DataSource{Name: source.Name}
		if source.Type != "" {
			message.Type = &source.Type
		}
		if source.Database != "" {
			message.Database = &source.Database
		}
		if source.Host != "" {
			message.Host = &source.Host
		}
		if source.Project != "" {
			message.Project = &source.Project
		}
		request.DataSources = append(request.DataSources, message)
	}
	if snapshot.Git != nil && snapshot.Git.CloneURL != "" {
		request.GitContext = &ingestgitv1.GitContext{
			CloneUrl:  snapshot.Git.CloneURL,
			Branch:    snapshot.Git.Branch,
			CommitSha: snapshot.Git.CommitSHA,
		}
	}
	if snapshot.ModelPath != nil {
		modelPath := *snapshot.ModelPath
		request.ModelPath = &modelPath
	}
	if !snapshot.CollectedAt.IsZero() {
		request.CollectedAt = timestamppb.New(snapshot.CollectedAt)
	}
	if snapshot.CubeVersion != "" {
		request.CubeVersion = &snapshot.CubeVersion
	}
	if snapshot.SecurityContextLabel != "" {
		request.SecurityContextLabel = &snapshot.SecurityContextLabel
	}
	return request
}

// noErrorText is the error text of a member that has no SQL and no error text.
const noErrorText = "the uploader has no SQL and no error text for the member"

// rfc3339 returns a time text in RFC 3339 format, in UTC. It returns an empty text for a text
// that is not a time in that format, so the message never has a build time that the API refuses.
func rfc3339(text string) string {
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

// NewSnapshot builds the snapshot of the collector from the message of the upload, for a
// message that was read from a file. It is the reverse of NewRequest. The fields that name the
// uploader are not part of a snapshot.
//
// A message from NewRequest gives the snapshot that it was built from. A statement with no
// `ungrouped` value is a grouped one.
func NewSnapshot(request *ingestcubev1.IngestSnapshotRequest) *cube.Snapshot {
	snapshot := &cube.Snapshot{
		DataModel:            request.GetDataModel(),
		ModelExport:          request.ModelExport,
		CubeVersion:          request.GetCubeVersion(),
		SecurityContextLabel: request.GetSecurityContextLabel(),
	}
	if request.ModelPath != nil {
		modelPath := request.GetModelPath()
		snapshot.ModelPath = &modelPath
	}
	if request.GetCollectedAt() != nil {
		snapshot.CollectedAt = request.GetCollectedAt().AsTime()
	}
	for _, message := range request.GetGeneratedSql() {
		generated := cube.GeneratedSQL{
			SQL:        message.GetSql(),
			DataSource: message.GetDataSource(),
			Ungrouped:  message.GetUngrouped(),
		}
		for _, member := range message.GetMembers() {
			generated.Members = append(generated.Members, cube.GeneratedSQLMember{Member: member.GetMember(), Column: member.GetColumn()})
		}
		snapshot.GeneratedSQL = append(snapshot.GeneratedSQL, generated)
	}
	for _, message := range request.GetMemberErrors() {
		snapshot.MemberErrors = append(snapshot.MemberErrors, cube.MemberError{Member: message.GetMember(), Error: message.GetError()})
	}
	for _, message := range request.GetDataSources() {
		snapshot.DataSources = append(snapshot.DataSources, cube.DataSource{
			Name:     message.GetName(),
			Type:     message.GetType(),
			Database: message.GetDatabase(),
			Host:     message.GetHost(),
			Project:  message.GetProject(),
		})
	}
	if git := request.GetGitContext(); git != nil {
		snapshot.Git = &cube.GitContext{CloneURL: git.GetCloneUrl(), Branch: git.GetBranch(), CommitSHA: git.GetCommitSha()}
	}
	return snapshot
}
