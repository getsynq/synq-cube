package buildinfo

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type BuildInfoSuite struct {
	suite.Suite
}

func TestBuildInfoSuite(t *testing.T) {
	suite.Run(t, new(BuildInfoSuite))
}

// stamp sets the link-time variables for one test and restores them after, so a test that fakes
// a release does not change the fallback tests.
func (s *BuildInfoSuite) stamp(version, commit, buildTime string) {
	s.T().Helper()
	prevV, prevC, prevT := BuildVersion, BuildCommit, BuildTime
	BuildVersion, BuildCommit, BuildTime = version, commit, buildTime
	s.T().Cleanup(func() {
		BuildVersion, BuildCommit, BuildTime = prevV, prevC, prevT
	})
}

func (s *BuildInfoSuite) TestStampsWin() {
	s.stamp("0.4.4", "6da2aaa4a763b39ef8490adb5bf1501cb807e5d2", "2026-07-29T15:54:09Z")

	info := Get()
	s.Equal("0.4.4", info.Version)
	s.Equal("6da2aaa4a763b39ef8490adb5bf1501cb807e5d2", info.Commit)
	s.Equal("2026-07-29T15:54:09Z", info.Time)
}

// A workflow that passes its tag with no change stamps "v0.3.0".
func (s *BuildInfoSuite) TestVersionPrefixStripped() {
	s.stamp("v0.3.0", "abc", "2026-07-29T18:06:34Z")
	s.Equal("0.3.0", Get().Version)
}

// An unstamped build must not claim to be a release. The commit comes from the embedded VCS
// data, which is there when tests run from a checkout, so the test asserts only the version.
func (s *BuildInfoSuite) TestUnstampedReportsDev() {
	s.stamp("", "", "")

	s.Equal(DevVersion, Get().Version)
}

func (s *BuildInfoSuite) TestStampedBuildNeverReportsDirty() {
	s.False(dirtyFromVCS(true, "true"), "a stamped release must not report the state of the tree")
	s.False(dirtyFromVCS(true, "false"))
}

func (s *BuildInfoSuite) TestUnstampedBuildReportsDirty() {
	s.True(dirtyFromVCS(false, "true"))
	s.False(dirtyFromVCS(false, "false"))
	s.False(dirtyFromVCS(false, ""), "absent VCS data is not a dirty tree")
}

func (s *BuildInfoSuite) TestStringRendersReleaseLine() {
	info := Info{
		Version: "0.4.4",
		Commit:  "6da2aaa4a763b39ef8490adb5bf1501cb807e5d2",
		Time:    "2026-07-29T15:54:09Z",
	}
	s.Equal(
		"0.4.4 (commit 6da2aaa4a763b39ef8490adb5bf1501cb807e5d2, built 2026-07-29T15:54:09Z)",
		info.String(),
	)
}

// An empty field is left out, so a local build does not report a build time of "unknown".
func (s *BuildInfoSuite) TestStringOmitsEmptyFields() {
	s.Equal("dev", Info{Version: DevVersion}.String())
	s.Equal("dev (commit abc123)", Info{Version: DevVersion, Commit: "abc123"}.String())
	s.Equal("dev (commit abc123, dirty)", Info{Version: DevVersion, Commit: "abc123", Dirty: true}.String())
}
