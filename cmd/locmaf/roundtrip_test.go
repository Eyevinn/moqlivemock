package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

const (
	bundledAVCAsset  = "../../assets/test10s/video_400kbps_avc.mp4"
	bundledHEVCAsset = "../../assets/test10s/video_900kbps_hevc.mp4"
	bundledAACAsset  = "../../assets/test10s/audio_monotonic_128kbps_aac.mp4"
	bundledOpusAsset = "../../assets/test10s/audio_monotonic_128kbps_opus.mp4"
)

// End-to-end coverage of runRoundtrip on every bundled codec.
//
// Each bundled asset is a fragmented MP4 containing one media track. The
// test runs the full pipeline (parse, synthesise track metadata, compress
// moov, decompress moov, compress every moof, decompress every moof, and
// verify per-sample fidelity) and only asserts on the error result —
// runRoundtrip itself reports per-fragment failures via err.

func TestRunRoundtrip_AVC(t *testing.T) {
	require.NoError(t, runRoundtrip(bundledAVCAsset, "", nil, "", false))
}

func TestRunRoundtrip_HEVC(t *testing.T) {
	require.NoError(t, runRoundtrip(bundledHEVCAsset, "", nil, "", false))
}

func TestRunRoundtrip_AAC(t *testing.T) {
	require.NoError(t, runRoundtrip(bundledAACAsset, "", nil, "", false))
}

func TestRunRoundtrip_Opus(t *testing.T) {
	require.NoError(t, runRoundtrip(bundledOpusAsset, "", nil, "", false))
}

func TestRunRoundtrip_Verbose(t *testing.T) {
	// Verbose mode prints a per-fragment table; only verify the pipeline
	// still succeeds with verbose enabled.
	require.NoError(t, runRoundtrip(bundledAVCAsset, "", nil, "", true))
}

func TestRunRoundtrip_WithTrackInfoJSON(t *testing.T) {
	// Side-channel JSON path: write a minimal CMSF-shaped Track,
	// then run roundtrip with -track-info pointing at it.
	timescale := 12800
	width := 384
	height := 216
	tr := internal.Track{
		Role:      "video",
		Codec:     "avc1",
		Timescale: &timescale,
		Width:     &width,
		Height:    &height,
	}
	data, err := json.Marshal(tr)
	require.NoError(t, err)
	jsonPath := filepath.Join(t.TempDir(), "track.json")
	require.NoError(t, os.WriteFile(jsonPath, data, 0o644))

	require.NoError(t, runRoundtrip(bundledAVCAsset, "", nil, jsonPath, false))
}

func TestRunRoundtrip_NonexistentFile(t *testing.T) {
	err := runRoundtrip("/no/such/file.mp4", "", nil, "", false)
	require.Error(t, err)
}

// synthesizeTrackFromMoov unit tests — verify the catalog-side Track
// shape inferred from a parsed moov for each media type.

func TestSynthesizeTrackFromMoov_Video(t *testing.T) {
	moov := mustLoadMoov(t, bundledAVCAsset)
	tr, err := synthesizeTrackFromMoov(moov)
	require.NoError(t, err)
	require.Equal(t, "video", tr.Role)
	require.Equal(t, "avc1", tr.Codec)
	require.NotNil(t, tr.Timescale)
	require.Positive(t, *tr.Timescale)
	require.NotNil(t, tr.Width)
	require.NotNil(t, tr.Height)
	require.Positive(t, *tr.Width)
	require.Positive(t, *tr.Height)
	require.Nil(t, tr.SampleRate)
}

func TestSynthesizeTrackFromMoov_HEVC(t *testing.T) {
	moov := mustLoadMoov(t, bundledHEVCAsset)
	tr, err := synthesizeTrackFromMoov(moov)
	require.NoError(t, err)
	require.Equal(t, "video", tr.Role)
	require.Equal(t, "hvc1", tr.Codec)
}

func TestSynthesizeTrackFromMoov_Audio(t *testing.T) {
	moov := mustLoadMoov(t, bundledAACAsset)
	tr, err := synthesizeTrackFromMoov(moov)
	require.NoError(t, err)
	require.Equal(t, "audio", tr.Role)
	require.Equal(t, "mp4a", tr.Codec)
	require.NotNil(t, tr.Timescale)
	require.NotNil(t, tr.SampleRate)
	require.Equal(t, 48000, *tr.SampleRate)
	require.Nil(t, tr.Width)
	require.Nil(t, tr.Height)
}

// resolveTrack covers both arms: explicit JSON wins; absent JSON falls
// back to the moov-synthesised track.

func TestResolveTrack_FromJSON(t *testing.T) {
	moov := mustLoadMoov(t, bundledAVCAsset)

	timescale := 90000
	width := 1280
	height := 720
	tr := internal.Track{
		Role:      "video",
		Codec:     "avc1.640028",
		Timescale: &timescale,
		Width:     &width,
		Height:    &height,
	}
	data, err := json.Marshal(tr)
	require.NoError(t, err)
	jsonPath := filepath.Join(t.TempDir(), "track.json")
	require.NoError(t, os.WriteFile(jsonPath, data, 0o644))

	resolved, source, err := resolveTrack(moov, jsonPath)
	require.NoError(t, err)
	require.Contains(t, source, "track.json")
	require.Equal(t, "avc1.640028", resolved.Codec)
	require.Equal(t, 90000, *resolved.Timescale)
	require.Equal(t, 1280, *resolved.Width)
}

func TestResolveTrack_Synthesised(t *testing.T) {
	moov := mustLoadMoov(t, bundledAVCAsset)
	tr, source, err := resolveTrack(moov, "")
	require.NoError(t, err)
	require.Equal(t, "synthesised from moov", source)
	require.Equal(t, "video", tr.Role)
}

func TestResolveTrack_MissingRequiredFields(t *testing.T) {
	moov := mustLoadMoov(t, bundledAVCAsset)
	// Track JSON with no role / no timescale — should be rejected.
	jsonPath := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{"codec":"avc1"}`), 0o644))

	_, _, err := resolveTrack(moov, jsonPath)
	require.Error(t, err)
}

func TestResolveTrack_BadJSONPath(t *testing.T) {
	moov := mustLoadMoov(t, bundledAVCAsset)
	_, _, err := resolveTrack(moov, "/no/such/track.json")
	require.Error(t, err)
}

// gzipSize is a small helper but exercised heavily in roundtrip stats;
// pin its behaviour with two cases.

func TestGzipSize_Compresses(t *testing.T) {
	data := bytes.Repeat([]byte("hello world "), 100) // 1200 B, highly redundant
	size, err := gzipSize(data)
	require.NoError(t, err)
	require.Less(t, size, len(data))
}

func TestGzipSize_Empty(t *testing.T) {
	size, err := gzipSize(nil)
	require.NoError(t, err)
	// gzip header + trailer for empty input is small but non-zero.
	require.Positive(t, size)
}

func TestGzipSize_Random(t *testing.T) {
	// Random/incompressible data should not blow up; gzip output may be
	// slightly larger than input, but the call must succeed.
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i * 17) // not random, but not very compressible
	}
	_, err := gzipSize(data)
	require.NoError(t, err)
}

// Flag-parsing surface of runRoundtripCommand: covers the mutual-
// exclusion checks and the default-when-nothing-given branch.

func TestRunRoundtripCommand_DefaultsToBundledInput(t *testing.T) {
	// No flags → defaults to the bundled AVC asset (defaultRoundtripInput).
	require.NoError(t, runRoundtripCommand(nil))
}

func TestRunRoundtripCommand_InputAndInitMutuallyExclusive(t *testing.T) {
	err := runRoundtripCommand([]string{
		"-input", bundledAVCAsset,
		"-init", bundledAVCAsset,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")
}

func TestRunRoundtripCommand_InitRequiresSegment(t *testing.T) {
	err := runRoundtripCommand([]string{"-init", bundledAVCAsset})
	require.Error(t, err)
	require.Contains(t, err.Error(), "-segment")
}

func TestRunRoundtripCommand_UnexpectedPositional(t *testing.T) {
	err := runRoundtripCommand([]string{bundledAVCAsset})
	require.Error(t, err)
	require.Contains(t, err.Error(), "positional")
}

// The separate-input mode of runRoundtrip: split a bundled fMP4 into an
// init segment file (ftyp + moov) and one media segment file
// (moof + mdat for the first fragment), then run with -init / -segment.

func TestRunRoundtrip_InitPlusSegmentMode(t *testing.T) {
	initPath, segPath := splitBundledAVCToInitAndFirstSegment(t)
	require.NoError(t, runRoundtrip("", initPath, []string{segPath}, "", false))
}

func TestRunRoundtrip_InitPlusMultipleSegments(t *testing.T) {
	initPath, segs := splitBundledAVCToInitAndSegments(t, 3)
	require.NoError(t, runRoundtrip("", initPath, segs, "", false))
}

// stringSlice (the segmentPaths flag.Value) is exercised by the flag
// parser when the test above feeds -segment multiple times via the
// runRoundtripCommand path. Cover the type directly here too.

func TestSegmentPaths_AppendsAndJoins(t *testing.T) {
	var s segmentPaths
	require.Empty(t, s.String())
	require.NoError(t, s.Set("a.m4s"))
	require.NoError(t, s.Set("b.m4s"))
	require.Equal(t, []string{"a.m4s", "b.m4s"}, []string(s))
	require.Equal(t, "a.m4s,b.m4s", s.String())
}

// Smoke-test the testgen subcommand end-to-end: it should write the six
// expected artifact files for the bundled AVC asset.

func TestRunTestFileGeneratorCommand(t *testing.T) {
	out := t.TempDir()
	args := []string{"-input", bundledAVCAsset, "-out", out}
	require.NoError(t, runTestFileGeneratorCommand(args))

	for _, name := range []string{
		cmafInitReferenceFile,
		locmafInitEncodingFile,
		locmafFullMoofObjectFile + "-1",
		locmafFullMoofObjectFile + "-2",
		locmafDeltaMoofObjectFile + "-1",
		locmafDeltaMoofObjectFile + "-2",
	} {
		path := filepath.Join(out, name)
		info, err := os.Stat(path)
		require.NoError(t, err, "expected output file %s to exist", name)
		require.Positive(t, info.Size(), "expected output file %s to be non-empty", name)
	}
}

func TestRunTestFileGeneratorCommand_UnexpectedPositional(t *testing.T) {
	err := runTestFileGeneratorCommand([]string{"extra-positional"})
	require.Error(t, err)
}

// Helpers.

func mustLoadMoov(t *testing.T, path string) *mp4.MoovBox {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	file, err := mp4.DecodeFile(f)
	require.NoError(t, err)
	require.NotNil(t, file.Moov)
	return file.Moov
}

// splitBundledAVCToInitAndFirstSegment decodes the bundled AVC asset and
// writes (a) ftyp+moov to an init.mp4 and (b) the first fragment's
// moof+mdat to a 1.m4s file in a temp dir. Returns their paths.
func splitBundledAVCToInitAndFirstSegment(t *testing.T) (initPath, segPath string) {
	t.Helper()
	initPath, segs := splitBundledAVCToInitAndSegments(t, 1)
	return initPath, segs[0]
}

func splitBundledAVCToInitAndSegments(t *testing.T, nSegments int) (initPath string, segPaths []string) {
	t.Helper()
	f, err := os.Open(bundledAVCAsset)
	require.NoError(t, err)
	defer f.Close()
	file, err := mp4.DecodeFile(f)
	require.NoError(t, err)
	require.NotNil(t, file.Ftyp)
	require.NotNil(t, file.Moov)
	require.NotEmpty(t, file.Segments)

	dir := t.TempDir()

	var initBuf bytes.Buffer
	require.NoError(t, file.Ftyp.Encode(&initBuf))
	require.NoError(t, file.Moov.Encode(&initBuf))
	initPath = filepath.Join(dir, "init.mp4")
	require.NoError(t, os.WriteFile(initPath, initBuf.Bytes(), 0o644))

	// Each .m4s carries one fragment's moof+mdat. Walk the fragments
	// in stream order and emit nSegments files.
	emitted := 0
outer:
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if emitted >= nSegments {
				break outer
			}
			var sbuf bytes.Buffer
			require.NoError(t, frag.Moof.Encode(&sbuf))
			require.NoError(t, frag.Mdat.Encode(&sbuf))
			path := filepath.Join(dir, segName(emitted+1))
			require.NoError(t, os.WriteFile(path, sbuf.Bytes(), 0o644))
			segPaths = append(segPaths, path)
			emitted++
		}
	}
	require.Equal(t, nSegments, emitted, "bundled asset did not yield enough fragments")
	return initPath, segPaths
}

func segName(n int) string {
	switch n {
	case 1:
		return "1.m4s"
	case 2:
		return "2.m4s"
	case 3:
		return "3.m4s"
	default:
		return "seg.m4s"
	}
}
