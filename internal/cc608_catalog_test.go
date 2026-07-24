package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// requireCC608Accessibility asserts that tr advertises exactly one CTA-608
// accessibility descriptor: the SCTE DASH CEA-608 scheme with the CC1/English
// value. It is the wire-visible contract of the -cc608 feature at the catalog
// level (draft-ietf-moq-msf-01 Section 5.2.44).
func requireCC608Accessibility(t *testing.T, tr *Track) {
	t.Helper()
	require.Lenf(t, tr.Accessibility, 1, "track %q accessibility count", tr.Name)
	assert.Equalf(t, "urn:scte:dash:cc:cea-608:2015", tr.Accessibility[0].Scheme,
		"track %q accessibility scheme", tr.Name)
	assert.Equalf(t, "CC1=eng", tr.Accessibility[0].Value,
		"track %q accessibility value", tr.Name)
}

// requireNoAccessibility asserts that no track in the catalog advertises any
// accessibility descriptor (the omitempty field must be entirely absent).
func requireNoAccessibility(t *testing.T, tracks []Track) {
	t.Helper()
	for i := range tracks {
		assert.Emptyf(t, tracks[i].Accessibility,
			"track %q must not advertise accessibility", tracks[i].Name)
	}
}

// TestCatalogCC608Accessibility_CMSF verifies issue #113 for the unified
// MSF/CMSF catalog: with an enabled generator installed, every video track
// (both its CMAF and LOCMAF variant) carries the CTA-608 accessibility
// descriptor while audio and subtitle tracks carry none; and without an
// enabled generator the field is absent everywhere (the gating).
func TestCatalogCC608Accessibility_CMSF(t *testing.T) {
	// Captions on: video tracks (both packagings) advertise CTA-608, others don't.
	on, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	on.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	catOn, err := on.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 1234567890000)
	require.NoError(t, err)

	sawCMAFVideo, sawLOCMAFVideo := false, false
	for i := range catOn.Tracks {
		tr := &catOn.Tracks[i]
		if tr.Role == "video" {
			requireCC608Accessibility(t, tr)
			switch tr.Packaging {
			case "cmaf":
				sawCMAFVideo = true
			case "locmaf":
				sawLOCMAFVideo = true
			}
			continue
		}
		assert.Emptyf(t, tr.Accessibility,
			"non-video track %q must not advertise captions", tr.Name)
	}
	assert.True(t, sawCMAFVideo, "expected at least one CMAF video track")
	assert.True(t, sawLOCMAFVideo, "expected at least one LOCMAF video track")

	// Captions off (no generator installed): the descriptor is absent everywhere.
	off, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	catOff, err := off.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 1234567890000)
	require.NoError(t, err)
	requireNoAccessibility(t, catOff.Tracks)

	// A disabled generator is equivalent to no generator: still absent.
	dis, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	dis.SetCC608Generator(cc608.New(cc608.Config{Enabled: false}))
	catDis, err := dis.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 1234567890000)
	require.NoError(t, err)
	requireNoAccessibility(t, catDis.Tracks)
}

// TestCatalogCC608Accessibility_LOC verifies issue #113 for the MSF/LOC catalog:
// with an enabled generator every LOC video track advertises the CTA-608
// descriptor and audio tracks carry none; without one the field is absent.
func TestCatalogCC608Accessibility_LOC(t *testing.T) {
	on, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	on.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	catOn, err := on.GenLOCCatalogEntry(1700000000000)
	require.NoError(t, err)

	sawVideo := false
	for i := range catOn.Tracks {
		tr := &catOn.Tracks[i]
		if tr.Role == "video" {
			requireCC608Accessibility(t, tr)
			sawVideo = true
			continue
		}
		assert.Emptyf(t, tr.Accessibility,
			"non-video track %q must not advertise captions", tr.Name)
	}
	assert.True(t, sawVideo, "expected at least one LOC video track")

	// Captions off: absent everywhere.
	off, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	catOff, err := off.GenLOCCatalogEntry(1700000000000)
	require.NoError(t, err)
	requireNoAccessibility(t, catOff.Tracks)
}
