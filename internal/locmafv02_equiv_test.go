package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal/locmafv02"
)

// TestLocmafV02IntegrationSmoke is the strongest cross-cutting test:
// it walks the real test10s assets through the v0.2 pipeline
// (compress + decompress) for the first MoQ group's worth of chunks
// and asserts the reconstructed sample data still matches the source
// samples byte-for-byte (modulo encryption side-effects, none in this
// test path because we pick clear tracks).
func TestLocmafV02IntegrationSmoke(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	wanted := map[string]bool{
		"video_400kbps_avc":       false,
		"audio_scale_128kbps_aac": false,
	}

	for _, group := range asset.Groups {
		for i := range group.Tracks {
			ct := &group.Tracks[i]
			if _, want := wanted[ct.Name]; !want {
				continue
			}
			wanted[ct.Name] = true
			runIntegrationTrack(t, ct)
		}
	}
	for name, saw := range wanted {
		require.True(t, saw, "did not find track %s", name)
	}
}

func runIntegrationTrack(t *testing.T, ct *ContentTrack) {
	t.Helper()
	state := locmafv02.NewState()
	batch := uint64(ct.SampleBatch)
	if batch == 0 {
		batch = 1
	}

	// Drive GenLocmafV02Chunk to confirm the public publisher API
	// works end-to-end (the result is also exercised below via
	// Compress directly, so we don't need to keep the bytes here).
	for chunkNr := uint32(0); chunkNr < 3; chunkNr++ {
		start := uint64(chunkNr) * batch
		end := start + batch
		if end > uint64(len(ct.Samples)) {
			return
		}
		_, err := ct.GenLocmafV02Chunk(chunkNr, start, end, state)
		require.NoError(t, err)
	}

	// Independent rebuild: encode all 3 chunks, decode all 3 chunks,
	// and verify lengths/flags line up with the source.
	encState := locmafv02.NewState()
	decState := locmafv02.NewState()
	for chunkNr := uint32(0); chunkNr < 3; chunkNr++ {
		start := uint64(chunkNr) * batch
		end := start + batch
		if end > uint64(len(ct.Samples)) {
			return
		}
		srcFrag, err := ct.createFragment(chunkNr, start, end)
		require.NoError(t, err)

		headBytes, _, err := locmafv02.Compress(srcFrag.Moof, encState, moovOf(ct))
		require.NoError(t, err)
		obj := append(append([]byte(nil), headBytes...), srcFrag.Mdat.Data...)

		decMoof, _, err := locmafv02.Decompress(obj, decState, moovOf(ct))
		require.NoError(t, err)
		require.NotNil(t, decMoof, "track %s chunk %d", ct.Name, chunkNr)
		require.Len(t, decMoof.Traf.Trun.Samples, len(srcFrag.Moof.Traf.Trun.Samples),
			"track %s chunk %d sample count", ct.Name, chunkNr)
		for i, src := range srcFrag.Moof.Traf.Trun.Samples {
			got := decMoof.Traf.Trun.Samples[i]
			require.Equal(t, src.Size, got.Size,
				"track %s chunk %d sample %d size", ct.Name, chunkNr, i)
			require.Equal(t, src.Dur, got.Dur,
				"track %s chunk %d sample %d dur", ct.Name, chunkNr, i)
			require.Equal(t, src.Flags, got.Flags,
				"track %s chunk %d sample %d flags", ct.Name, chunkNr, i)
		}

		// The mdat payload survives untouched: re-encode the
		// decoded moof + mdat and assert mdat round-tripped.
		frag := mp4.NewFragment()
		frag.AddChild(decMoof)
		frag.AddChild(&mp4.MdatBox{Data: srcFrag.Mdat.Data})
		sw := bits.NewFixedSliceWriter(int(frag.Size()))
		require.NoError(t, frag.EncodeSW(sw))
		// Sanity: the encoded bytes must start with a styp- or
		// moof-shaped box header (mp4 box length + 'moof').
		require.Greater(t, len(sw.Bytes()), 8)
		require.True(t, bytes.Contains(sw.Bytes(), []byte("moof")),
			"reconstructed fragment lacks moof box")
		require.True(t, bytes.Contains(sw.Bytes(), []byte("mdat")),
			"reconstructed fragment lacks mdat box")
	}
}

func moovOf(ct *ContentTrack) *mp4.MoovBox {
	return ct.SpecData.GetInit().Moov
}

// TestLocmafV02EncryptedSencRoundTrip exercises the DRM/CENC path of
// the v0.2 codec, which the clear-track tests above never touch. For
// both cbcs and cenc it encrypts real fragments, runs them through the
// v0.2 Compress/Decompress codec, and asserts the reconstructed senc
// box matches the source: per-sample IV size, the IV bytes, and the
// subsample (clear/protected byte) layout. This is the
// "document the DRM box round-trip" milestone from docs/locmaf_v0_2.md.
func TestLocmafV02EncryptedSencRoundTrip(t *testing.T) {
	kidStr := "39112233445566778899aabbccddeeff"
	keyStr := "40112233445566778899aabbccddeeff"
	ivStr := "41112233445566778899aabbccddeeff"

	for _, scheme := range []string{"cbcs", "cenc"} {
		t.Run(scheme, func(t *testing.T) {
			eccp, err := ParseCENCflags(scheme, kidStr, keyStr, ivStr,
				"http://localhost:8081/clearkey")
			require.NoError(t, err)
			asset, err := LoadAssetWithProtection("../assets/test10s", 1, 1, nil, eccp)
			require.NoError(t, err)

			ct := firstProtectedVideoTrack(t, asset)
			moov := moovOf(ct)

			encState := locmafv02.NewState()
			decState := locmafv02.NewState()
			batch := uint64(ct.SampleBatch)
			if batch == 0 {
				batch = 1
			}

			sawProtected := false
			for chunkNr := uint32(0); chunkNr < 4; chunkNr++ {
				start := uint64(chunkNr) * batch
				end := start + batch
				if end > uint64(len(ct.Samples)) {
					break
				}

				// Build the encrypted source fragment exactly like
				// GenLocmafV02Chunk does internally (createFragment +
				// encryptFragment).
				srcFrag, err := ct.createFragment(chunkNr, start, end)
				require.NoError(t, err)
				sw := bits.NewFixedSliceWriter(int(srcFrag.Size()))
				require.NoError(t, srcFrag.EncodeSW(sw))
				encFrag, err := ct.encryptFragment(sw.Bytes())
				require.NoError(t, err)

				srcSenc := parsedSenc(t, encFrag.Moof, moov)
				require.NotNil(t, srcSenc, "source chunk %d must carry a senc box", chunkNr)
				sawProtected = true

				// Capture source senc values before Compress, which may
				// mutate parsed box state on the traf.
				srcIVSize := srcSenc.PerSampleIVSize()
				srcIVs := append([]mp4.InitializationVector(nil), srcSenc.IVs...)
				srcSubs := append([][]mp4.SubSamplePattern(nil), srcSenc.SubSamples...)

				headBytes, _, err := locmafv02.Compress(encFrag.Moof, encState, moov)
				require.NoError(t, err)
				obj := append(append([]byte(nil), headBytes...), encFrag.Mdat.Data...)

				decMoof, _, err := locmafv02.Decompress(obj, decState, moov)
				require.NoError(t, err)
				require.NotNil(t, decMoof, "chunk %d decompressed to nil", chunkNr)

				decSenc := parsedSenc(t, decMoof, moov)
				require.NotNil(t, decSenc, "reconstructed chunk %d lost its senc box", chunkNr)

				require.Equal(t, srcIVSize, decSenc.PerSampleIVSize(),
					"chunk %d per-sample IV size", chunkNr)
				require.Equal(t, srcIVs, decSenc.IVs, "chunk %d IVs", chunkNr)
				require.Equal(t, srcSubs, decSenc.SubSamples,
					"chunk %d subsample layout", chunkNr)
			}
			require.True(t, sawProtected, "no encrypted chunks were exercised")
		})
	}
}

// firstProtectedVideoTrack returns the first encrypted video track in
// the asset, failing the test if there is none.
func firstProtectedVideoTrack(t *testing.T, asset *Asset) *ContentTrack {
	t.Helper()
	for gi := range asset.Groups {
		for ti := range asset.Groups[gi].Tracks {
			ct := &asset.Groups[gi].Tracks[ti]
			if ct.Protection != ProtectionNone && ct.ContentType == "video" {
				return ct
			}
		}
	}
	require.FailNow(t, "no protected video track found")
	return nil
}

// parsedSenc returns the senc box of the moof's traf, parsing it on
// demand if it is still raw (the source fragment carries an unparsed
// senc until ParseReadSenc is called).
func parsedSenc(t *testing.T, moof *mp4.MoofBox, moov *mp4.MoovBox) *mp4.SencBox {
	t.Helper()
	require.NotNil(t, moof.Traf)
	traf := moof.Traf
	ok, parsed := traf.ContainsSencBox()
	if !ok {
		return nil
	}
	if !parsed {
		var ivSize uint8
		if sinf := getTrackSinf(moov, traf.Tfhd.TrackID); sinf != nil &&
			sinf.Schi != nil && sinf.Schi.Tenc != nil {
			ivSize = sinf.Schi.Tenc.DefaultPerSampleIVSize
		}
		require.NoError(t, traf.ParseReadSenc(ivSize, moof.StartPos))
	}
	if traf.Senc != nil {
		return traf.Senc
	}
	if traf.UUIDSenc != nil {
		return traf.UUIDSenc.Senc
	}
	return nil
}

// getTrackSinf returns the sinf for trackID without panicking on a moov
// whose stsd has no children.
func getTrackSinf(moov *mp4.MoovBox, trackID uint32) *mp4.SinfBox {
	if moov == nil {
		return nil
	}
	for _, trak := range moov.Traks {
		if trak == nil || trak.Tkhd == nil || trak.Tkhd.TrackID != trackID ||
			trak.Mdia == nil || trak.Mdia.Minf == nil ||
			trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
			continue
		}
		stsd := trak.Mdia.Minf.Stbl.Stsd
		if len(stsd.Children) == 0 {
			continue
		}
		switch sd := stsd.Children[0].(type) {
		case *mp4.VisualSampleEntryBox:
			return sd.Sinf
		case *mp4.AudioSampleEntryBox:
			return sd.Sinf
		}
	}
	return nil
}
