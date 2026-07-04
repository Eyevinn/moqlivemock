package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/locmaf"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

// TestLocmafIntegrationSmoke is the strongest cross-cutting test: it
// walks the real test10s assets through the LOCMAF pipeline (canonical
// encode + decode + canonical reconstruction) for the first chunks of
// clear tracks and asserts the decoded effective values match the
// source samples, and that the canonical chunk parses as CMAF with the
// mdat payload intact.
func TestLocmafIntegrationSmoke(t *testing.T) {
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
	batch := uint64(ct.SampleBatch)
	if batch == 0 {
		batch = 1
	}

	// Drive GenLocmafChunk to confirm the public publisher API works
	// end-to-end.
	state := locmaf.NewState()
	for chunkNr := uint32(0); chunkNr < 3; chunkNr++ {
		start := uint64(chunkNr) * batch
		end := start + batch
		if end > uint64(len(ct.Samples)) {
			return
		}
		_, err := ct.GenLocmafChunk(chunkNr, start, end, state)
		require.NoError(t, err)
	}

	// Independent rebuild: encode all 3 chunks, decode all 3 chunks,
	// and verify the effective values line up with the source.
	encState := locmaf.NewState()
	decState := locmaf.NewState()
	for chunkNr := uint32(0); chunkNr < 3; chunkNr++ {
		start := uint64(chunkNr) * batch
		end := start + batch
		if end > uint64(len(ct.Samples)) {
			return
		}
		srcFrag, err := ct.createFragment(chunkNr, start, end)
		require.NoError(t, err)

		obj, err := locmaf.EncodeCanonical(nil, srcFrag.Moof, srcFrag.Mdat.Data, encState, moovOf(ct))
		require.NoError(t, err)

		eff, raw, err := locmaf.Decode(obj, decState, moovOf(ct))
		require.NoError(t, err)
		require.Nil(t, raw)
		srcSamples := srcFrag.Moof.Traf.Trun.Samples
		require.Equal(t, len(srcSamples), eff.SampleCount,
			"track %s chunk %d sample count", ct.Name, chunkNr)
		for i, src := range srcSamples {
			require.Equal(t, src.Size, eff.Sizes[i],
				"track %s chunk %d sample %d size", ct.Name, chunkNr, i)
			require.Equal(t, src.Dur, eff.Durations[i],
				"track %s chunk %d sample %d dur", ct.Name, chunkNr, i)
			require.Equal(t, src.Flags, eff.Flags[i],
				"track %s chunk %d sample %d flags", ct.Name, chunkNr, i)
		}

		// The canonical reconstruction is a parseable CMAF chunk and
		// carries the mdat payload untouched.
		chunk, err := locmaf.ReconstructCanonical(moovOf(ct), eff)
		require.NoError(t, err)
		box, err := mp4.DecodeBox(0, bytes.NewReader(chunk))
		require.NoError(t, err)
		_, ok := box.(*mp4.MoofBox)
		require.True(t, ok, "canonical chunk starts with a moof")
		require.True(t, bytes.HasSuffix(chunk, srcFrag.Mdat.Data),
			"mdat payload survives untouched")
	}
}

func moovOf(ct *ContentTrack) *mp4.MoovBox {
	return ct.SpecData.GetInit().Moov
}

// TestLocmafEncryptedSencRoundTrip exercises the DRM/CENC path, which
// the clear-track tests above never touch. For both cbcs and cenc it
// encrypts real fragments, runs them through the codec, and asserts the
// decoded effective CENC values match the source senc box: per-sample
// IV size, the IV bytes, and the subsample (clear/protected) layout.
func TestLocmafEncryptedSencRoundTrip(t *testing.T) {
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

			encState := locmaf.NewState()
			decState := locmaf.NewState()
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
				// GenLocmafChunk does internally (createFragment +
				// encryptFragment).
				srcFrag, err := ct.createFragment(chunkNr, start, end)
				require.NoError(t, err)
				encFrag, err := encryptViaTrack(ct, srcFrag)
				require.NoError(t, err)

				srcSenc := parsedSenc(t, encFrag.Moof, moov)
				require.NotNil(t, srcSenc, "source chunk %d must carry a senc box", chunkNr)
				sawProtected = true

				// Capture source senc values before encoding, which may
				// mutate parsed box state on the traf.
				srcIVSize := srcSenc.PerSampleIVSize()
				var srcIVs []byte
				for _, iv := range srcSenc.IVs {
					srcIVs = append(srcIVs, iv...)
				}
				srcSubs := append([][]mp4.SubSamplePattern(nil), srcSenc.SubSamples...)

				obj, err := locmaf.EncodeCanonical(nil, encFrag.Moof, encFrag.Mdat.Data, encState, moov)
				require.NoError(t, err)

				eff, raw, err := locmaf.Decode(obj, decState, moov)
				require.NoError(t, err)
				require.Nil(t, raw)

				require.Equal(t, srcIVSize, eff.PerSampleIVSize,
					"chunk %d per-sample IV size", chunkNr)
				require.Equal(t, srcIVs, eff.IVs, "chunk %d IVs", chunkNr)
				if len(srcSubs) > 0 {
					require.True(t, eff.HasSubsamples, "chunk %d subsample map", chunkNr)
					subIdx := 0
					for i, subs := range srcSubs {
						require.Equal(t, len(subs), int(eff.SubsampleCounts[i]),
							"chunk %d sample %d subsample count", chunkNr, i)
						for _, ss := range subs {
							require.Equal(t, ss.BytesOfClearData, eff.ClearBytes[subIdx],
								"chunk %d subsample %d clear bytes", chunkNr, subIdx)
							require.Equal(t, ss.BytesOfProtectedData, eff.ProtectedBytes[subIdx],
								"chunk %d subsample %d protected bytes", chunkNr, subIdx)
							subIdx++
						}
					}
				}

				// The canonical reconstruction must carry the senc
				// metadata in parseable form.
				chunk, err := locmaf.ReconstructCanonical(moov, eff)
				require.NoError(t, err)
				box, err := mp4.DecodeBox(0, bytes.NewReader(chunk))
				require.NoError(t, err)
				decMoof, ok := box.(*mp4.MoofBox)
				require.True(t, ok)
				decSenc := parsedSenc(t, decMoof, moov)
				require.NotNil(t, decSenc, "reconstructed chunk %d lost its senc box", chunkNr)
				require.Equal(t, srcIVSize, decSenc.PerSampleIVSize(),
					"chunk %d reconstructed per-sample IV size", chunkNr)
			}
			require.True(t, sawProtected, "no encrypted chunks were exercised")
		})
	}
}

// encryptViaTrack serialises the fragment and encrypts it the way
// GenLocmafChunk does.
func encryptViaTrack(ct *ContentTrack, frag *mp4.Fragment) (*mp4.Fragment, error) {
	size := frag.Size()
	buf := make([]byte, 0, size)
	w := bytes.NewBuffer(buf)
	if err := frag.Encode(w); err != nil {
		return nil, err
	}
	return ct.encryptFragment(w.Bytes())
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

// getTrackSinf finds the sinf box of the given track without panicking
// on a moov with no stsd children.
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
