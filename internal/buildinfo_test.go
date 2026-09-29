package internal

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionString(t *testing.T) {
	built := func(version, vcsTime string) *debug.BuildInfo {
		info := &debug.BuildInfo{Main: debug.Module{Path: "github.com/Eyevinn/moqlivemock", Version: version}}
		if vcsTime != "" {
			info.Settings = []debug.BuildSetting{
				{Key: "vcs.revision", Value: "141ad30347bee5ea0f0a7fdfeb55408c75195e7e"},
				{Key: "vcs.time", Value: vcsTime},
			}
		}
		return info
	}
	cases := []struct {
		desc string
		info *debug.BuildInfo
		want string
	}{
		{"tag build", built("v0.15.1", "2026-09-21T10:00:00Z"), "v0.15.1, date: 2026-09-21"},
		{"build after the tag, with local changes",
			built("v0.15.2-0.20260929143058-141ad30347be+dirty", "2026-09-29T14:30:58Z"),
			"v0.15.2-0.20260929143058-141ad30347be+dirty, date: 2026-09-29"},
		{"go install of a release has no vcs time", built("v0.15.1", ""), "v0.15.1"},
		{"the date is in UTC", built("v0.15.1", "2026-09-21T23:30:00-02:00"), "v0.15.1, date: 2026-09-22"},
		{"workspace build", built("(devel)", ""), "(devel)"},
		{"build of files, not a package", built("", ""), "(devel)"},
		{"no build information", nil, "(devel)"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			require.Equal(t, c.want, versionString(c.info))
		})
	}
}

// TestVersion checks that a test binary reports what the toolchain embedded
// in it, without asserting a value: that depends on how the test was built.
func TestVersion(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	require.True(t, ok)
	require.Equal(t, versionString(info), Version())
}
