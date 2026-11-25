// Package buildinfo is the single source of the build stamps of synq-cube.
//
// `--version` and the uploader fields of a snapshot read [Get]. No other package declares its
// own stamp variables.
//
// The release stamps it with:
//
//	-X github.com/getsynq/synq-cube/internal/buildinfo.BuildVersion=<version>
//	-X github.com/getsynq/synq-cube/internal/buildinfo.BuildCommit=<sha>
//	-X github.com/getsynq/synq-cube/internal/buildinfo.BuildTime=<rfc3339-utc>
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Stamped via -ldflags at release time. They are empty for `go build` and `go run` from a
// checkout, where [Get] reads the embedded VCS information.
var (
	BuildVersion string
	BuildCommit  string
	BuildTime    string
)

// DevVersion is what an unstamped build with no usable version reports.
const DevVersion = "dev"

// Info describes the build of the running binary. Fields other than Version can be empty: a
// stamped release fills all three, a `go build` from a clean checkout fills Version and Commit,
// and a build from an archive with no VCS data fills only Version.
type Info struct {
	// Version is a release version without the leading v ("0.4.4"), or "dev" if this is not a
	// release build.
	Version string
	// Commit is the full sha of the commit that the build came from.
	Commit string
	// Time is when the build was made, RFC 3339 in UTC.
	Time string
	// Dirty reports that the working tree had uncommitted changes. Only a local build sets it.
	Dirty bool
	// GoVersion is the toolchain that built the binary.
	GoVersion string
}

// Get resolves the build stamps. A value from link time wins over a value that the toolchain
// embedded.
//
//	Version: BuildVersion > module version > "dev"
//	Commit:  BuildCommit > vcs.revision
//	Time:    BuildTime > vcs.time
//
// The module version is the version of a binary from `go install <module>@<version>`.
// "(devel)" is not a version, so Get skips it.
func Get() Info {
	info := Info{
		// A release stamps a bare version, but a caller that passes a tag stamps "v0.3.0".
		Version: strings.TrimPrefix(BuildVersion, "v"),
		Commit:  BuildCommit,
		Time:    BuildTime,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion

		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = strings.TrimPrefix(bi.Main.Version, "v")
		}

		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Time == "" {
					info.Time = s.Value
				}
			case "vcs.modified":
				info.Dirty = dirtyFromVCS(BuildVersion != "", s.Value)
			}
		}
	}

	if info.Version == "" {
		info.Version = DevVersion
	}

	return info
}

// dirtyFromVCS decides if the build reports a dirty working tree.
//
// A stamped build never reports it. The release writes files into the tree before it compiles
// (the shell completions), so Go can record vcs.modified=true for a release. The version, the
// commit and the time of a release come from the tag.
//
// An unstamped build is a working copy, where the flag says that the binary does not match its
// commit.
func dirtyFromVCS(stamped bool, vcsModified string) bool {
	return !stamped && vcsModified == "true"
}

// String renders the version line for `synq-cube --version`. Cobra puts "synq-cube version "
// before it.
//
//	0.4.4 (commit 6da2aaa4a763b39ef8490adb5bf1501cb807e5d2, built 2026-07-29T15:54:09Z)
//
// An empty field is left out, so a local build reads "dev (commit abc123, dirty)".
func (i Info) String() string {
	var details []string
	if i.Commit != "" {
		details = append(details, "commit "+i.Commit)
	}
	if i.Time != "" {
		details = append(details, "built "+i.Time)
	}
	if i.Dirty {
		details = append(details, "dirty")
	}

	if len(details) == 0 {
		return i.Version
	}
	return fmt.Sprintf("%s (%s)", i.Version, strings.Join(details, ", "))
}
