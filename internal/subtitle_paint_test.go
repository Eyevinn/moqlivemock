package internal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Eyevinn/locmaf"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

// cadence25 is the object cadence of the 25 fps test content, one frame per object.
var cadence25 = SubtitleCadence{TimeScale: 12800, SampleDur: 512, SampleBatch: 1}

// subsGroupNr is a group whose cue starts at the group start and ends at 900 ms.
const subsGroupNr = 1_000_000

func TestSubtitleCadenceChunkBounds(t *testing.T) {
	t.Run("25 fps", func(t *testing.T) {
		bounds := cadence25.chunkBoundsMS(subsGroupNr, 1000)
		require.Len(t, bounds, 26)
		for i, b := range bounds {
			require.Equal(t, uint64(subsGroupNr*1000+i*40), b)
		}
	})
	t.Run("25 fps, 2 frames per object", func(t *testing.T) {
		c := cadence25
		c.SampleBatch = 2
		bounds := c.chunkBoundsMS(subsGroupNr, 1000)
		// 25 frames make 12 objects of 2 frames and a last one of 1 frame.
		require.Len(t, bounds, 14)
		require.Equal(t, uint64(subsGroupNr*1000+80), bounds[1])
		require.Equal(t, uint64(subsGroupNr*1000+960), bounds[12])
		require.Equal(t, uint64(subsGroupNr*1000+1000), bounds[13])
	})
	t.Run("29.97 fps tiles across groups", func(t *testing.T) {
		c := SubtitleCadence{TimeScale: 30000, SampleDur: 1001, SampleBatch: 1}
		prev := c.chunkBoundsMS(subsGroupNr, 1000)
		for g := uint64(subsGroupNr + 1); g < subsGroupNr+5; g++ {
			bounds := c.chunkBoundsMS(g, 1000)
			require.Equal(t, prev[len(prev)-1], bounds[0], "group %d must start where %d ended", g, g-1)
			for i := 1; i < len(bounds); i++ {
				d := bounds[i] - bounds[i-1]
				require.True(t, d == 33 || d == 34, "chunk duration %d", d)
			}
			prev = bounds
		}
	})
	t.Run("no video", func(t *testing.T) {
		bounds := SubtitleCadence{}.chunkBoundsMS(7, 1000)
		require.Equal(t, []uint64{7000, 8000}, bounds)
	})
}

// subsSample is one decoded sample of a generated subtitle object.
type subsSample struct {
	decodeTime uint64
	dur        uint32
	flags      uint32
	data       []byte
}

// decodeCMAFSubsObject decodes a CMAF subtitle object (one moof+mdat).
func decodeCMAFSubsObject(t *testing.T, obj []byte) []subsSample {
	t.Helper()
	f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(obj))
	require.NoError(t, err)
	require.Len(t, f.Segments, 1)
	require.Len(t, f.Segments[0].Fragments, 1)
	fss, err := f.Segments[0].Fragments[0].GetFullSamples(nil)
	require.NoError(t, err)
	samples := make([]subsSample, 0, len(fss))
	for _, fs := range fss {
		samples = append(samples, subsSample{fs.DecodeTime, fs.Dur, fs.Flags, fs.Data})
	}
	return samples
}

func genSubsTrack(t *testing.T, format SubtitleFormat, body bool) *SubtitleTrack {
	t.Helper()
	st, err := NewSubtitleTrack("subs_"+string(format)+"_en", format, "en")
	require.NoError(t, err)
	st.Cadence = cadence25
	st.Body = body
	return st
}

func genCMAFSubsGroup(t *testing.T, st *SubtitleTrack) [][]subsSample {
	t.Helper()
	mg, err := GenSubtitleGroup(st, subsGroupNr, MoqGroupDurMS, "cmaf")
	require.NoError(t, err)
	require.Len(t, mg.MoQObjects, 25)
	chunks := make([][]subsSample, 0, len(mg.MoQObjects))
	for i, obj := range mg.MoQObjects {
		samples := decodeCMAFSubsObject(t, obj)
		start := uint64(subsGroupNr*1000 + i*40)
		require.Equal(t, start, samples[0].decodeTime, "chunk %d start", i)
		var dur uint32
		for _, s := range samples {
			dur += s.dur
		}
		require.Equal(t, uint32(40), dur, "chunk %d must tile 40 ms", i)
		require.Equal(t, start+40, mg.objEndMS[i], "chunk %d is sent when it ends", i)
		chunks = append(chunks, samples)
	}
	return chunks
}

// The cue of the group starts at 0 ms and ends at 900 ms, inside chunk 22
// [880, 920). Chunk 23 [920, 960) is the first with nothing on screen.
const (
	cueEndChunk    = 22
	firstIdleChunk = 23
)

func TestChunkedStppRestatesUnclippedDocuments(t *testing.T) {
	chunks := genCMAFSubsGroup(t, genSubsTrack(t, SubtitleFormatSTPP, false))
	for i, samples := range chunks {
		require.Len(t, samples, 1, "one document per chunk")
		doc := string(samples[0].data)
		require.True(t, strings.HasPrefix(doc, "<?xml"), "chunk %d is a full document", i)
		if i <= cueEndChunk {
			require.Contains(t, doc, `begin="277:46:40.000"`, "chunk %d keeps the true begin", i)
		}
		require.Equal(t, i == cueEndChunk, strings.Contains(doc, `end="277:46:40.900"`),
			"chunk %d: the end is written only in the chunk where the cue ends", i)
	}
	require.Equal(t, mp4.SyncSampleFlags, chunks[0][0].flags)
	require.Equal(t, redundantSampleFlags, chunks[1][0].flags, "an identical restatement is marked redundant")
	require.Equal(t, mp4.SyncSampleFlags, chunks[cueEndChunk][0].flags)
	require.Equal(t, mp4.SyncSampleFlags, chunks[firstIdleChunk][0].flags)
	require.Equal(t, redundantSampleFlags, chunks[24][0].flags)
}

func TestChunkedStpcSendsNoChangeAndBodyBoxes(t *testing.T) {
	stpp := genCMAFSubsGroup(t, genSubsTrack(t, SubtitleFormatSTPP, false))
	for _, body := range []bool{false, true} {
		chunks := genCMAFSubsGroup(t, genSubsTrack(t, SubtitleFormatSTPC, body))
		require.Equal(t, stpp[0][0].data, chunks[0][0].data, "the first chunk is the full stpp document")
		require.Equal(t, mp4.SyncSampleFlags, chunks[0][0].flags)
		for i := 1; i < len(chunks); i++ {
			s := chunks[i][0]
			switch i {
			case cueEndChunk, firstIdleChunk:
				if !body {
					require.Equal(t, stpp[i][0].data, s.data, "chunk %d is a full document", i)
					require.Equal(t, mp4.SyncSampleFlags, s.flags, "a full document is self-contained")
					continue
				}
				require.Equal(t, paintDependentFlags, s.flags, "chunk %d depends on the first", i)
				require.Equal(t, "ttmb", string(s.data[4:8]), "chunk %d is a body box", i)
				require.Equal(t, uint32(len(s.data)), bits.NewFixedSliceReader(s.data).ReadUint32())
				bodyText := string(s.data[8:])
				require.True(t, strings.HasPrefix(bodyText, "  <body"), "chunk %d body: %q", i, bodyText)
				require.NotContains(t, bodyText, "<head>")
				require.Contains(t, string(stpp[i][0].data), bodyText, "the body is the document's body")
			default:
				require.Equal(t, ttmnBox, s.data, "chunk %d is a no-change box", i)
				require.Equal(t, paintDependentFlags, s.flags, "chunk %d depends on the first", i)
			}
		}
	}
}

func TestChunkedWvttAndWvtc(t *testing.T) {
	wvtt := genCMAFSubsGroup(t, genSubsTrack(t, SubtitleFormatWVTT, false))
	require.Len(t, wvtt[0], 1)
	require.Equal(t, "vttc", string(wvtt[0][0].data[4:8]))
	require.Equal(t, redundantSampleFlags, wvtt[1][0].flags)
	// The cue ends 20 ms into chunk 22, which is split into cue and empty samples.
	require.Len(t, wvtt[cueEndChunk], 2)
	require.Equal(t, uint32(20), wvtt[cueEndChunk][0].dur)
	require.Equal(t, vtteBox, wvtt[cueEndChunk][1].data)
	require.Equal(t, vtteBox, wvtt[firstIdleChunk][0].data)

	wvtc := genCMAFSubsGroup(t, genSubsTrack(t, SubtitleFormatWVTC, false))
	require.Equal(t, wvtt[0][0].data, wvtc[0][0].data)
	require.Equal(t, mp4.SyncSampleFlags, wvtc[0][0].flags)
	for i := 1; i < cueEndChunk; i++ {
		require.Equal(t, vttnBox, wvtc[i][0].data, "chunk %d continues the cue", i)
		require.Equal(t, paintDependentFlags, wvtc[i][0].flags)
	}
	// Chunk 22: the cue still shows for 20 ms (no change), then clears.
	require.Equal(t, vttnBox, wvtc[cueEndChunk][0].data)
	require.Equal(t, vtteBox, wvtc[cueEndChunk][1].data)
	require.Equal(t, mp4.SyncSampleFlags, wvtc[cueEndChunk][1].flags)
	require.Equal(t, vttnBox, wvtc[firstIdleChunk][0].data, "nothing on screen is a restatement too")
}

// TestSubtitleLocmafRoundTrip decodes every LOCMAF subtitle object and checks
// it carries exactly the samples of the CMAF object, and that the paint-model
// variants shrink the group in both packagings.
func TestSubtitleLocmafRoundTrip(t *testing.T) {
	sizes := map[string]int{}
	for _, format := range []SubtitleFormat{SubtitleFormatSTPP, SubtitleFormatSTPC, SubtitleFormatWVTT,
		SubtitleFormatWVTC} {
		st := genSubsTrack(t, format, true)
		cmaf, err := GenSubtitleGroup(st, subsGroupNr, MoqGroupDurMS, "cmaf")
		require.NoError(t, err)
		lm, err := GenSubtitleGroup(st, subsGroupNr, MoqGroupDurMS, "locmaf")
		require.NoError(t, err)
		require.Len(t, lm.MoQObjects, len(cmaf.MoQObjects))
		init, err := st.SpecData.initSegment()
		require.NoError(t, err)
		state := locmaf.NewState()
		for i := range lm.MoQObjects {
			want := decodeCMAFSubsObject(t, cmaf.MoQObjects[i])
			eff, raw, err := locmaf.Decode(lm.MoQObjects[i], state, init.Moov)
			require.NoError(t, err, "%s object %d", format, i)
			require.Nil(t, raw)
			require.Equal(t, len(want), eff.SampleCount)
			require.Equal(t, want[0].decodeTime, eff.BMDT)
			offset := 0
			for j, w := range want {
				require.Equal(t, w.dur, eff.Durations[j])
				require.Equal(t, w.flags, eff.Flags[j], "%s object %d sample %d flags", format, i, j)
				size := int(eff.Sizes[j])
				require.True(t, bytes.Equal(w.data, eff.MdatPayload[offset:offset+size]))
				offset += size
			}
			sizes[string(format)+"/cmaf"] += len(cmaf.MoQObjects[i])
			sizes[string(format)+"/locmaf"] += len(lm.MoQObjects[i])
		}
	}
	t.Logf("bytes per 1 s group: %v", sizes)
	for _, pack := range []string{"cmaf", "locmaf"} {
		require.Less(t, sizes["stpc/"+pack]*5, sizes["stpp/"+pack], "stpc is a fraction of stpp in %s", pack)
		require.Less(t, sizes["wvtc/"+pack], sizes["wvtt/"+pack], "wvtc is smaller than wvtt in %s", pack)
	}
	for _, format := range []string{"stpp", "stpc", "wvtt", "wvtc"} {
		require.Less(t, sizes[format+"/locmaf"], sizes[format+"/cmaf"], "%s: LOCMAF is smaller", format)
	}
}

func TestPaintSubtitleInitSegments(t *testing.T) {
	for _, tc := range []struct {
		format SubtitleFormat
		entry  string
	}{
		{SubtitleFormatSTPP, "stpp"},
		{SubtitleFormatSTPC, "stpc"},
		{SubtitleFormatWVTT, "wvtt"},
		{SubtitleFormatWVTC, "wvtc"},
	} {
		st := genSubsTrack(t, tc.format, false)
		data, err := st.SpecData.GenCMAFInitData()
		require.NoError(t, err)
		f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(data))
		require.NoError(t, err)
		stsd := f.Moov.Trak.Mdia.Minf.Stbl.Stsd
		require.Len(t, stsd.Children, 1)
		require.Equal(t, tc.entry, stsd.Children[0].Type())
	}
}

func TestAddPaintSubtitleTracks(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	require.NoError(t, asset.AddSubtitleTracks([]string{"sv"}, []string{"en"}))
	require.NoError(t, asset.AddPaintSubtitleTracks([]string{"sv"}, []string{"en"}, true))
	names := make([]string, 0, len(asset.SubtitleTracks))
	for _, st := range asset.SubtitleTracks {
		names = append(names, st.Name)
		require.Equal(t, SubsTimeTimescale*uint32(1), st.TimeScale)
		require.NotZero(t, st.Cadence.SampleDur, "%s follows the video cadence", st.Name)
		bounds := st.Cadence.chunkBoundsMS(subsGroupNr, MoqGroupDurMS)
		require.Len(t, bounds, 26, "%s: one object per 25 fps video frame", st.Name)
		require.Equal(t, st.Format == SubtitleFormatSTPC, st.Body)
	}
	require.Equal(t, []string{"subs_wvtt_sv", "subs_stpp_en", "subs_wvtc_sv", "subs_stpc_en"}, names)

	st, packaging := asset.ResolveSubtitleTrack("subs_stpc_en")
	require.Equal(t, "subs_stpc_en", st.Name)
	require.Equal(t, "cmaf", packaging)
	st, packaging = asset.ResolveSubtitleTrack("subs_stpc_en" + LocmafTrackSuffix)
	require.Equal(t, "subs_stpc_en", st.Name)
	require.Equal(t, "locmaf", packaging)
	st, _ = asset.ResolveSubtitleTrack("subs_stpc_fr_locmaf")
	require.Nil(t, st)

	cat, err := asset.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 0)
	require.NoError(t, err)
	type subsEntry struct {
		codec, packaging string
		altGroup         int
		bitrate          int
	}
	got := map[string]subsEntry{}
	for _, tr := range cat.Tracks {
		if tr.Role != "subtitle" {
			continue
		}
		require.NotEmpty(t, tr.InitRef)
		got[tr.Name] = subsEntry{tr.Codec, tr.Packaging, *tr.AltGroup, *tr.Bitrate}
		if tr.Packaging == "locmaf" {
			require.Equal(t, locmaf.Version, tr.LocmafVersion)
			require.Equal(t, "init-"+strings.TrimSuffix(tr.Name, LocmafTrackSuffix), tr.InitRef,
				"the LOCMAF variant shares the CMAF init data")
		}
	}
	require.Len(t, got, 8)
	require.Equal(t, "stpc", got["subs_stpc_en"].codec)
	require.Equal(t, "wvtc", got["subs_wvtc_sv_locmaf"].codec)
	require.Equal(t, "stpp.ttml.im1t", got["subs_stpp_en_locmaf"].codec)
	require.Equal(t, got["subs_stpc_en"].altGroup, got["subs_stpc_en_locmaf"].altGroup)
	require.NotEqual(t, got["subs_stpc_en"].altGroup, got["subs_stpp_en"].altGroup)
	require.Less(t, got["subs_stpc_en"].bitrate*5, got["subs_stpp_en"].bitrate)
	require.Less(t, got["subs_wvtc_sv_locmaf"].bitrate, got["subs_wvtc_sv"].bitrate)
}
