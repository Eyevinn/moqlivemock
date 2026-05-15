package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBitrateExposesPackagingDifferences asserts that the catalog Bitrate
// field reflects the wire footprint of each packaging variant: encrypted
// CMAF reports more than clear CMAF, and LOC always exceeds the raw sample
// bitrate. This guards against regressions where a code path forgets to
// account for container or transport overhead.
func TestBitrateExposesPackagingDifferences(t *testing.T) {
	const kid = "abcdef0123456789abcdef0123456789"
	const iv = "fedcba9876543210"
	const laURL = "http://localhost:8081/clearkey"

	cencEccp, err := ParseCENCflags("cenc", kid, kid, iv, laURL)
	require.NoError(t, err)

	asset, err := LoadAssetWithProtection("../assets/test10s", 2, 1, nil, cencEccp)
	require.NoError(t, err)

	clearCat, err := asset.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 0, "cmaf")
	require.NoError(t, err)
	encCat, err := asset.GenCMAFCatalogEntry("cmsf/eccp-cenc", ProtectionECCP, 0, "cmaf")
	require.NoError(t, err)
	locCat, err := asset.GenLOCCatalogEntry(0)
	require.NoError(t, err)

	// Index each catalog by base track name (encrypted tracks have an "_eccp"
	// suffix that we strip for the comparison).
	clearByName := indexTracks(clearCat.Tracks, "")
	encByName := indexTracks(encCat.Tracks, "_eccp")
	locByName := indexTracks(locCat.Tracks, "")

	require.NotEmpty(t, clearByName)
	require.NotEmpty(t, encByName)
	require.NotEmpty(t, locByName)

	for name, clearTrack := range clearByName {
		ct := asset.GetTrackByName(name)
		require.NotNil(t, ct, "lookup track %s", name)
		require.NotNil(t, clearTrack.Bitrate, "clear bitrate for %s", name)

		// CMAF clear must exceed the raw sample bitrate.
		require.Greater(t, *clearTrack.Bitrate, int(ct.SampleBitrate),
			"%s: clear CMAF bitrate %d <= sample bitrate %d",
			name, *clearTrack.Bitrate, ct.SampleBitrate)

		// CMAF cenc must report more than CMAF clear (saio + saiz + senc + IVs).
		if encTrack, ok := encByName[name]; ok {
			require.NotNil(t, encTrack.Bitrate)
			require.Greater(t, *encTrack.Bitrate, *clearTrack.Bitrate,
				"%s: cenc bitrate %d should exceed clear CMAF bitrate %d",
				name, *encTrack.Bitrate, *clearTrack.Bitrate)
		}

		// LOC must exceed the raw sample bitrate (per-object headers, plus
		// per-IRAP parameter sets for video).
		if locTrack, ok := locByName[name]; ok {
			require.NotNil(t, locTrack.Bitrate)
			require.Greater(t, *locTrack.Bitrate, int(ct.SampleBitrate),
				"%s: LOC bitrate %d should exceed sample bitrate %d",
				name, *locTrack.Bitrate, ct.SampleBitrate)
		}
	}
}

// TestLOCBitrateHEVCExceedsAVC asserts that for video tracks of comparable
// size the HEVC LOC bitrate carries more parameter-set overhead per IRAP
// than AVC: HEVC has VPS+SPS+PPS while AVC has only SPS+PPS, and HEVC
// parameter sets are typically larger byte-for-byte. Without a per-codec
// keyframe term in calcLOCBitrate the two would tie at the raw bitrate.
func TestLOCBitrateHEVCExceedsParameterSetOverheadAVC(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 2, 1)
	require.NoError(t, err)
	cat, err := asset.GenLOCCatalogEntry(0)
	require.NoError(t, err)
	byName := indexTracks(cat.Tracks, "")

	for _, bitrate := range []string{"400kbps", "600kbps", "900kbps"} {
		avc, ok := byName["video_"+bitrate+"_avc"]
		require.True(t, ok, "expected AVC LOC track at %s", bitrate)
		hevc, ok := byName["video_"+bitrate+"_hevc"]
		require.True(t, ok, "expected HEVC LOC track at %s", bitrate)
		require.NotNil(t, avc.Bitrate)
		require.NotNil(t, hevc.Bitrate)

		ctAvc := asset.GetTrackByName(avc.Name)
		ctHevc := asset.GetTrackByName(hevc.Name)
		avcExtra := *avc.Bitrate - int(ctAvc.SampleBitrate)
		hevcExtra := *hevc.Bitrate - int(ctHevc.SampleBitrate)
		// HEVC must carry strictly more LOC overhead than AVC at the same
		// bitrate target — VPS adds bytes that AVC doesn't have.
		require.Greater(t, hevcExtra, avcExtra,
			"%s: HEVC LOC overhead %d should exceed AVC LOC overhead %d",
			bitrate, hevcExtra, avcExtra)
	}
}

// TestLocmafBitrateIsLowerThanCmaf asserts that for the same source track
// the LOCMAF catalog bitrate is strictly lower than the CMAF catalog
// bitrate. This guards against regressions where the locmaf-packaged
// Track ends up reporting the CMAF wire bitrate (which was the case
// before calcLocmafBitrate was introduced).
func TestLocmafBitrateIsLowerThanCmaf(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	cmafCat, err := asset.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 0, "cmaf")
	require.NoError(t, err)
	locmafCat, err := asset.GenCMAFCatalogEntry("locmaf/clear", ProtectionNone, 0, "locmaf")
	require.NoError(t, err)

	cmafByName := indexTracks(cmafCat.Tracks, "")
	locmafByName := indexTracks(locmafCat.Tracks, "")
	require.NotEmpty(t, cmafByName)
	require.NotEmpty(t, locmafByName)

	for name, cmafTrack := range cmafByName {
		locmafTrack, ok := locmafByName[name]
		require.True(t, ok, "track %s missing from LOCMAF catalog", name)
		require.NotNil(t, cmafTrack.Bitrate)
		require.NotNil(t, locmafTrack.Bitrate)

		// LOCMAF moofs are dramatically smaller than CMAF moofs at
		// sample-level fragmentation; the per-track bitrate must reflect
		// that.
		require.Less(t, *locmafTrack.Bitrate, *cmafTrack.Bitrate,
			"%s: LOCMAF bitrate %d should be lower than CMAF bitrate %d",
			name, *locmafTrack.Bitrate, *cmafTrack.Bitrate)

		// LOCMAF should still exceed the raw sample bitrate — there is
		// always some per-object framing and the per-group full moof.
		ct := asset.GetTrackByName(name)
		require.NotNil(t, ct)
		require.Greater(t, *locmafTrack.Bitrate, int(ct.SampleBitrate),
			"%s: LOCMAF bitrate %d should exceed raw sample bitrate %d",
			name, *locmafTrack.Bitrate, ct.SampleBitrate)
	}
}

func indexTracks(tracks []Track, suffix string) map[string]Track {
	out := make(map[string]Track, len(tracks))
	for _, tr := range tracks {
		name := tr.Name
		if suffix != "" && len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			name = name[:len(name)-len(suffix)]
		}
		out[name] = tr
	}
	return out
}
