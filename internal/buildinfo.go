package internal

import (
	"fmt"
	"runtime/debug"
	"time"
)

// Version returns the version of the running binary and the time of its
// commit, such as "v0.15.1, date: 2026-09-21", from the build information the
// Go toolchain embeds.
//
// Since Go 1.24, building a package in a git checkout in module mode embeds
// the version from the tag: v0.15.1 at the tag, a pseudo-version such as
// v0.15.2-0.20260929143058-141ad30347be after it, and +dirty with uncommitted
// changes, untracked files included. go install of a released version embeds
// that version. A build in workspace mode (go.work) embeds neither version nor
// commit, and gives "(devel)"; so does a build without the .git directory.
func Version() string {
	info, _ := debug.ReadBuildInfo()
	return versionString(info)
}

// versionString formats the version from the build information, which may be nil.
func versionString(info *debug.BuildInfo) string {
	if info == nil {
		return "(devel)"
	}
	version := info.Main.Version
	if version == "" {
		version = "(devel)"
	}
	for _, s := range info.Settings {
		if s.Key != "vcs.time" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
			return fmt.Sprintf("%s, date: %s", version, t.UTC().Format(time.DateOnly))
		}
	}
	return version
}
