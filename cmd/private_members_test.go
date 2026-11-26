package cmd_test

import (
	ingestcubev1 "buf.build/gen/go/getsynq/api/protocolbuffers/go/synq/ingest/cube/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsynq/synq-cube/cube/cubetest"
)

// productionSnapshotFile writes a snapshot file of a Cube Cloud deployment in production: the
// data model has only what is public, and the model export has each cube, view and member.
func (s *UploadSuite) productionSnapshotFile() string {
	return s.writeRequest(&ingestcubev1.IngestSnapshotRequest{
		DataModel:   cubetest.PublicMetaExtended(),
		ModelExport: cubetest.ModelExport(),
		CollectedAt: timestamppb.Now(),
	})
}

// The run summary counts the private cubes, views and members that only the model export has.
func (s *UploadSuite) TestTheSummaryCountsThePrivateCubesViewsAndMembers() {
	s.Require().NoError(s.run("upload", "--input", s.productionSnapshotFile(), "--dry-run"))

	out := s.stdout.String()
	s.Contains(out, "Cubes: 14\n")
	s.Contains(out, "Views: 6\n")
	s.Contains(out, "Members: 254\n")
	s.Contains(out, "Cube Cloud model export: yes\n")
	s.Contains(out, "Private, from the Cube Cloud model export only: cubes 1, views 1, members 33\n")
}

// With --skip-hidden the snapshot has no model export, as the model export has the private cubes,
// views and members and cannot be filtered.
func (s *UploadSuite) TestSkipHiddenLeavesOutTheModelExportWithThePrivateMembers() {
	s.Require().NoError(s.run("upload", "--input", s.productionSnapshotFile(), "--dry-run", "--skip-hidden"))

	out := s.stdout.String()
	s.Contains(out, "Cubes: 13\n")
	s.Contains(out, "Views: 5\n")
	s.Contains(out, "Members: 221\n")
	s.NotContains(out, "Cube Cloud model export: yes")
	s.NotContains(out, "Private, from the Cube Cloud model export only")
	s.Contains(out, "Left out: the Cube Cloud model export.")
}
