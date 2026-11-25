package quality

import (
	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsynq/synq-cube/cube"
)

// NewRequest builds the message of the upload from a snapshot of the collector.
// uploaderVersion and uploaderBuildTime name the build of the tool that sends it.
//
// The package cube imports no generated proto package, so this file is the one place that maps
// a snapshot to the message and back. A new field of cube.Snapshot needs a line in NewRequest
// and in NewSnapshot. TestNewRequestMapsEachField fails until it has them.
func NewRequest(snapshot *cube.Snapshot, uploaderVersion string, uploaderBuildTime string) *ingestcubev1.IngestSnapshotRequest {
	var members []*ingestcubev1.MemberSql
	if len(snapshot.MemberSQL) > 0 {
		members = make([]*ingestcubev1.MemberSql, 0, len(snapshot.MemberSQL))
	}
	for _, member := range snapshot.MemberSQL {
		members = append(members, newMemberSQL(member))
	}
	request := &ingestcubev1.IngestSnapshotRequest{
		Meta:              snapshot.Meta,
		MemberSql:         members,
		ModelExport:       snapshot.ModelExport,
		UploaderVersion:   uploaderVersion,
		UploaderBuildTime: uploaderBuildTime,
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

// newMemberSQL maps one member. A member has an error or it has SQL, never the two. The SQL of
// a member with no error is set even if it is empty.
func newMemberSQL(member cube.MemberSQL) *ingestcubev1.MemberSql {
	message := &ingestcubev1.MemberSql{Member: member.Member, Ungrouped: member.Ungrouped}
	if member.Error != "" {
		message.Error = &member.Error
		return message
	}
	message.Sql = &member.SQL
	if member.DataSource != "" {
		message.DataSource = &member.DataSource
	}
	return message
}

// NewSnapshot builds the snapshot of the collector from the message of the upload, for a
// message that was read from a file. It is the reverse of NewRequest. The fields that name the
// uploader are not part of a snapshot.
//
// A message from NewRequest gives the snapshot that it was built from. A message that a person
// edited can hold a member with an error and with SQL: the snapshot keeps the error only, as
// NewRequest sends a member with an error with no SQL.
func NewSnapshot(request *ingestcubev1.IngestSnapshotRequest) *cube.Snapshot {
	snapshot := &cube.Snapshot{
		Meta:                 request.GetMeta(),
		ModelExport:          request.ModelExport,
		CubeVersion:          request.GetCubeVersion(),
		SecurityContextLabel: request.GetSecurityContextLabel(),
	}
	if request.GetCollectedAt() != nil {
		snapshot.CollectedAt = request.GetCollectedAt().AsTime()
	}
	for _, message := range request.GetMemberSql() {
		member := cube.MemberSQL{Member: message.GetMember(), Ungrouped: message.GetUngrouped()}
		if message.GetError() != "" {
			member.Error = message.GetError()
		} else {
			member.SQL = message.GetSql()
			member.DataSource = message.GetDataSource()
		}
		snapshot.MemberSQL = append(snapshot.MemberSQL, member)
	}
	return snapshot
}
