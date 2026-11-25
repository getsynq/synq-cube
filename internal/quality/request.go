package quality

import (
	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsynq/synq-cube/cube"
)

// NewRequest builds the message of the upload from a snapshot of the collector.
// uploaderVersion and uploaderBuildTime name the build of the tool that sends it.
//
// The package cube imports no generated proto package, so this function is the one place that
// maps a snapshot to the message. A new field of cube.Snapshot needs a line here.
// TestNewRequestMapsEachField fails until it has one.
func NewRequest(snapshot *cube.Snapshot, uploaderVersion string, uploaderBuildTime string) *ingestcubev1.IngestSnapshotRequest {
	members := make([]*ingestcubev1.MemberSql, 0, len(snapshot.MemberSQL))
	for _, member := range snapshot.MemberSQL {
		members = append(members, newMemberSQL(member))
	}
	request := &ingestcubev1.IngestSnapshotRequest{
		Meta:              snapshot.Meta,
		MemberSql:         members,
		ModelExport:       snapshot.ModelExport,
		CollectedAt:       timestamppb.New(snapshot.CollectedAt),
		UploaderVersion:   uploaderVersion,
		UploaderBuildTime: uploaderBuildTime,
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
