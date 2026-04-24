package internal

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareTrack(t *testing.T) {
	testCases := []struct {
		desc          string
		filePath      string
		contentType   string
		language      string
		timeScale     int
		duration      int
		sampleDur     int
		nrSamples     int
		gopLength     int
		sampleBitrate int
	}{
		{
			desc:          "video_400kbps_avc",
			filePath:      "../assets/test10s/video_400kbps_avc.mp4",
			contentType:   "video",
			timeScale:     12800,
			duration:      128000,
			sampleDur:     512,
			nrSamples:     250,
			gopLength:     25,
			sampleBitrate: 373200,
		},
		{
			desc:          "audio_128kbps_aac",
			filePath:      "../assets/test10s/audio_monotonic_128kbps_aac.mp4",
			contentType:   "audio",
			timeScale:     48000,
			duration:      469 * 1024,
			sampleDur:     1024,
			nrSamples:     469,
			gopLength:     1,
			sampleBitrate: 128001,
			language:      "und",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fh, err := os.Open(tc.filePath)
			require.NoError(t, err)
			ct, err := InitContentTrack(fh, tc.desc, 1, 1)
			require.NoError(t, err)
			require.Equal(t, tc.contentType, ct.ContentType, "contentType")
			require.Equal(t, tc.timeScale, int(ct.TimeScale), "timeScale")
			require.Equal(t, tc.duration, int(ct.Duration), "duration")
			require.Equal(t, tc.sampleDur, int(ct.SampleDur), "sampleDur")
			require.Equal(t, tc.nrSamples, int(ct.NrSamples), "nrSamples")
			require.Equal(t, tc.gopLength, int(ct.GopLength), "gopLength")
			require.Equal(t, tc.sampleBitrate, int(ct.SampleBitrate), "sampleBitrate")
		})
	}
}

func TestLoadAsset(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	require.NotNil(t, asset)

	// Check asset name
	require.Equal(t, "test10s", asset.Name)

	// Collect all tracks by contentType
	trackCounts := map[string]int{}
	for _, group := range asset.Groups {
		for _, track := range group.Tracks {
			trackCounts[track.ContentType]++
		}
	}
	// Expect 6 audio and 6 video tracks
	require.Equal(t, 6, trackCounts["audio"], "should have 6 audio tracks")
	require.Equal(t, 6, trackCounts["video"], "should have 6 video tracks")

	// Check that track names match the files
	var expectedNames = map[string]bool{
		"audio_monotonic_128kbps_aac":  true,
		"audio_monotonic_128kbps_opus": true,
		"audio_monotonic_192kbps_ac3":  true,
		"audio_scale_128kbps_aac":      true,
		"audio_scale_128kbps_opus":     true,
		"audio_scale_192kbps_ac3":      true,
		"video_400kbps_avc":            true,
		"video_600kbps_avc":            true,
		"video_900kbps_avc":            true,
		"video_400kbps_hevc":           true,
		"video_600kbps_hevc":           true,
		"video_900kbps_hevc":           true,
	}
	for _, group := range asset.Groups {
		for _, track := range group.Tracks {
			_, ok := expectedNames[track.Name]
			require.True(t, ok, "unexpected track name: %s", track.Name)
		}
	}

	// Check that video tracks are sorted by codec (avc before hvc) then by bitrate ascending
	var videoCodecs []string
	var videoBitrates []int
	var videoNames []string
	for _, group := range asset.Groups {
		if len(group.Tracks) > 0 && group.Tracks[0].ContentType == "video" {
			for _, track := range group.Tracks {
				videoCodecs = append(videoCodecs, track.SpecData.Codec())
				videoBitrates = append(videoBitrates, int(track.SampleBitrate))
				videoNames = append(videoNames, track.Name)
			}
		}
	}
	for i := 1; i < len(videoBitrates); i++ {
		if videoCodecs[i-1] == videoCodecs[i] {
			require.LessOrEqual(t, videoBitrates[i-1], videoBitrates[i],
				"video tracks not in bitrate order within codec: got %v (%v)", videoBitrates, videoNames)
		} else {
			require.LessOrEqual(t, videoCodecs[i-1], videoCodecs[i],
				"video tracks not in codec order: got %v (%v)", videoCodecs, videoNames)
		}
	}

	// Check that video group has a lower altGroupID than audio group
	var videoGroupID, audioGroupID uint32
	for _, group := range asset.Groups {
		if len(group.Tracks) > 0 {
			switch group.Tracks[0].ContentType {
			case "video":
				videoGroupID = group.AltGroupID
			case "audio":
				audioGroupID = group.AltGroupID
			}
		}
	}
	if videoGroupID != 0 && audioGroupID != 0 {
		require.Less(t, videoGroupID, audioGroupID,
			"video group altGroupID should be less than audio group altGroupID")
	}
	require.Equal(t, 10000, int(asset.LoopDurMS), "loop duration should be 10000ms")
	for _, group := range asset.Groups {
		for _, track := range group.Tracks {
			require.Equal(t, int(10*track.TimeScale), int(track.LoopDur),
				"loop duration should be 10s in timescale")
		}
	}
	cat, err := asset.GenCMAFCatalogEntry("cmsf/clear", ProtectionNone, 1234567890000, "cmaf")
	require.NoError(t, err)
	require.NotNil(t, cat)
	require.Equal(t, 12, len(cat.Tracks))
	// Check that all tracks have the namespace set
	for _, track := range cat.Tracks {
		require.Equal(t, "cmsf/clear", track.Namespace)
	}
}

func TestCreateProtectedTracksDoesNotMutateOriginalTrackInit(t *testing.T) {
	tracksByType, err := parseTracks("../assets/test10s", 1, 1)
	require.NoError(t, err)

	origVideo := tracksByType["video"][0]
	videoInitBefore, err := origVideo.SpecData.GenCMAFInitData()
	require.NoError(t, err)

	kidStr := "39112233445566778899aabbccddeeff"
	keyStr := "40112233445566778899aabbccddeeff"
	ivStr := "41112233445566778899aabbccddeeff"
	drm, err := ParseCENCflags("cenc", kidStr, keyStr, ivStr, "http://localhost:8081/clearkey")
	require.NoError(t, err)

	err = createProtectedTracks(tracksByType, drm, "_drm", ProtectionDRM)
	require.NoError(t, err)

	origVideoAfter := tracksByType["video"][0]
	videoInitAfter, err := origVideoAfter.SpecData.GenCMAFInitData()
	require.NoError(t, err)
	require.Equal(t, videoInitBefore, videoInitAfter, "original video init data should be unchanged")

	protectedVideo := tracksByType["video"][len(tracksByType["video"])-1]
	require.NotNil(t, protectedVideo.cenc)
	require.NotEmpty(t, protectedVideo.contentProtectionRefIDs)
	require.NotNil(t, protectedVideo.ipd)
	protectedVideoInit, err := protectedVideo.SpecData.GenCMAFInitData()
	require.NoError(t, err)
	require.NotEqual(t, videoInitBefore, protectedVideoInit, "protected track should have modified init data")
}

func TestLocmafCatalogUsesLocmafInitData(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	cat, err := asset.GenCMAFCatalogEntry("locmaf/clear", ProtectionNone, 1234567890000, "locmaf")
	require.NoError(t, err)
	require.NotEmpty(t, cat.Tracks)

	var videoTrack *Track
	for i := range cat.Tracks {
		if cat.Tracks[i].Role == "video" {
			videoTrack = &cat.Tracks[i]
			break
		}
	}
	require.NotNil(t, videoTrack)
	require.Equal(t, "locmaf", videoTrack.Packaging)
	require.NotEmpty(t, videoTrack.InitData)

	locmafInit, err := base64.StdEncoding.DecodeString(videoTrack.InitData)
	require.NoError(t, err)

	headerID, n := binary.Varint(locmafInit)
	require.Greater(t, n, 0)
	require.Equal(t, int64(MoovHeader), headerID)

	pos := 0
	_, n = binary.Varint(locmafInit[pos:])
	pos += n
	locPayloadLength, n := binary.Varint(locmafInit[pos:])
	pos += n
	require.Greater(t, n, 0)
	require.LessOrEqual(t, pos+int(locPayloadLength), len(locmafInit))
	locPayload := locmafInit[pos : pos+int(locPayloadLength)]

	timescale := *videoTrack.Timescale
	width := *videoTrack.Width
	height := *videoTrack.Height
	decompressedInit, err := DecompressInit(locPayload, Track{
		Timescale: &timescale,
		Width:     &width,
		Height:    &height,
	})
	require.NoError(t, err)
	require.NotNil(t, decompressedInit)
	require.NotNil(t, decompressedInit.Moov)
}

func TestGen20sCMAFStreams(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	require.NotNil(t, asset)

	tmpDir := t.TempDir()
	cases := []struct {
		name     string
		groupIdx int
		trackNr  int
	}{
		{"video_400kbps_avc", 0, 0},
		{"video_600kbps_avc", 0, 1},
		{"video_900kbps_avc", 0, 2},
		{"audio_128kbps", 1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := asset.Groups[tc.groupIdx].Tracks[tc.trackNr]
			outFile := filepath.Join(tmpDir, tc.name+".mp4")
			ofh, err := os.Create(outFile)
			require.NoError(t, err)
			spc := tr.SpecData
			data, err := spc.GenCMAFInitData()
			require.NoError(t, err)
			_, err = ofh.Write(data)
			require.NoError(t, err)
			nrSamples := int(20 * tr.TimeScale / tr.SampleDur)
			groupNr := uint32(0)
			for nr := 0; nr < nrSamples; nr++ {
				chunk, err := tr.GenCMAFChunk(groupNr, uint64(nr), uint64(nr+1))
				require.NoError(t, err)
				_, err = ofh.Write(chunk)
				require.NoError(t, err)
			}
			ofh.Close()
			fh, err := os.Open(outFile)
			require.NoError(t, err)
			defer fh.Close()
			mp4f, err := mp4.DecodeFile(fh)
			require.NoError(t, err)
			require.Equal(t, 1, len(mp4f.Segments))
			require.Equal(t, nrSamples, len(mp4f.Segments[0].Fragments))
			for _, frag := range mp4f.Segments[0].Fragments {
				// Tfdt version will be 0 at start, but 1 as needed
				// for big enough timestamps (64-bit need)
				require.Equal(t, 0, int(frag.Moof.Traf.Tfdt.Version))
				// Size of fragment should be 100 bytes for tfdt version 0
				// and exactly one sample without compositionTimeOffset.
				require.Equal(t, 100, int(frag.Moof.Size()))
			}
		})
	}
}

func TestClearKeyDecryptionMatchExcatly(t *testing.T) {
	kidStr := "39112233445566778899aabbccddeeff"
	keyStr := "40112233445566778899aabbccddeeff"
	ivStr := "41112233445566778899aabbccddeeff"
	schemes := []string{"cbcs", "cenc"}
	for _, scheme := range schemes {
		eccp, err := ParseCENCflags(scheme, kidStr, keyStr, ivStr, "http://localhost:8081/clearkey")
		require.NoError(t, err)
		checkDecryptedTracksMatchExactly(t, eccp, "_eccp")
	}
}

func TestCompressDecompressProtectedInitPreservesProtectionFields(t *testing.T) {
	kidStr := "39112233445566778899aabbccddeeff"
	keyStr := "40112233445566778899aabbccddeeff"
	ivStr := "41112233445566778899aabbccddeeff"

	for _, scheme := range []string{"cenc", "cbcs"} {
		t.Run(scheme, func(t *testing.T) {
			eccp, err := ParseCENCflags(scheme, kidStr, keyStr, ivStr, "http://localhost:8081/clearkey")
			require.NoError(t, err)

			asset, err := LoadAssetWithProtection("../assets/test10s", 1, 1, nil, eccp)
			require.NoError(t, err)

			var protectedTrack *ContentTrack
			for groupIdx := range asset.Groups {
				for trackIdx := range asset.Groups[groupIdx].Tracks {
					track := &asset.Groups[groupIdx].Tracks[trackIdx]
					if track.Protection != ProtectionNone && track.ContentType == "video" {
						protectedTrack = track
						break
					}
				}
				if protectedTrack != nil {
					break
				}
			}
			require.NotNil(t, protectedTrack)

			initData, err := protectedTrack.SpecData.GenCMAFInitData()
			require.NoError(t, err)
			originalFile, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(initData))
			require.NoError(t, err)
			require.NotNil(t, originalFile.Init)

			require.NotNil(t, originalFile.Init.Moov.Trak)
			require.NotEmpty(t, originalFile.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children)
			originalSampleEntry := originalFile.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0]
			originalSinf := getSampleEntrySinf(originalSampleEntry)
			originalTenc := getSampleEntryTenc(originalSampleEntry)
			require.NotNil(t, originalSinf)
			require.NotNil(t, originalSinf.Schm)
			require.NotNil(t, originalTenc)
			require.Equal(t, byte(1), originalTenc.DefaultIsProtected)

			compressed, err := CompressMoov(originalFile.Init.Moov)
			require.NoError(t, err)

			timescale := int(protectedTrack.TimeScale)
			width := int(originalFile.Init.Moov.Trak.Tkhd.Width >> 16)
			height := int(originalFile.Init.Moov.Trak.Tkhd.Height >> 16)
			trackInfo := Track{Timescale: &timescale, Width: &width, Height: &height}
			decompressedInit, err := DecompressInit(compressed, trackInfo)
			require.NoError(t, err)

			require.NotNil(t, decompressedInit.Moov.Trak)
			require.NotEmpty(t, decompressedInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children)
			rebuiltSampleEntry := decompressedInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0]
			rebuiltSinf := getSampleEntrySinf(rebuiltSampleEntry)
			rebuiltTenc := getSampleEntryTenc(rebuiltSampleEntry)
			require.NotNil(t, rebuiltSinf)
			require.NotNil(t, rebuiltSinf.Schm)
			require.NotNil(t, rebuiltTenc)

			require.Equal(t, originalFile.Init.Moov.Trak.Tkhd.Flags, decompressedInit.Moov.Trak.Tkhd.Flags)
			require.Equal(t, originalSinf.Schm.SchemeType, rebuiltSinf.Schm.SchemeType)
			require.Equal(t, originalTenc.Version, rebuiltTenc.Version)
			require.Equal(t, originalTenc.DefaultIsProtected, rebuiltTenc.DefaultIsProtected)
			require.Equal(t, originalTenc.DefaultCryptByteBlock, rebuiltTenc.DefaultCryptByteBlock)
			require.Equal(t, originalTenc.DefaultSkipByteBlock, rebuiltTenc.DefaultSkipByteBlock)
			require.Equal(t, originalTenc.DefaultPerSampleIVSize, rebuiltTenc.DefaultPerSampleIVSize)
			require.Equal(t, originalTenc.DefaultKID, rebuiltTenc.DefaultKID)
			require.Equal(t, originalTenc.DefaultConstantIV, rebuiltTenc.DefaultConstantIV)
		})
	}
}

func TestCommercialDRMDecryptionMatchExactly(t *testing.T) {
	drm, err := ConfigureDRMFromFile("../assets/testdrm/drm_config_test.json")
	require.NoError(t, err)
	checkDecryptedTracksMatchExactly(t, drm, "_drm")
}

func TestGetSubtitleTrackByName(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	err = asset.AddSubtitleTracks([]string{"en", "sv"}, []string{"en"})
	require.NoError(t, err)

	t.Run("found wvtt", func(t *testing.T) {
		st := asset.GetSubtitleTrackByName("subs_wvtt_en")
		require.NotNil(t, st)
		assert.Equal(t, "subs_wvtt_en", st.Name)
		assert.Equal(t, SubtitleFormatWVTT, st.Format)
		assert.Equal(t, "en", st.Language)
	})

	t.Run("found stpp", func(t *testing.T) {
		st := asset.GetSubtitleTrackByName("subs_stpp_en")
		require.NotNil(t, st)
		assert.Equal(t, SubtitleFormatSTPP, st.Format)
	})

	t.Run("not found", func(t *testing.T) {
		assert.Nil(t, asset.GetSubtitleTrackByName("nonexistent"))
	})
}

func TestAddSubtitleTracks(t *testing.T) {
	asset := &Asset{}

	err := asset.AddSubtitleTracks([]string{"en", "sv"}, []string{"fr"})
	require.NoError(t, err)
	assert.Equal(t, 3, len(asset.SubtitleTracks))
	assert.Equal(t, "subs_wvtt_en", asset.SubtitleTracks[0].Name)
	assert.Equal(t, "subs_wvtt_sv", asset.SubtitleTracks[1].Name)
	assert.Equal(t, "subs_stpp_fr", asset.SubtitleTracks[2].Name)
}

func TestAddSubtitleTracksEmpty(t *testing.T) {
	asset := &Asset{}

	err := asset.AddSubtitleTracks([]string{}, []string{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(asset.SubtitleTracks))
}

func checkDecryptedTracksMatchExactly(t *testing.T, drm *DRMInfo, suffix string) {
	var drmAsset *Asset
	var err error
	if suffix == "_eccp" {
		drmAsset, err = LoadAssetWithProtection("../assets/test10s", 1, 1, nil, drm)
	} else {
		drmAsset, err = LoadAssetWithProtection("../assets/test10s", 1, 1, drm, nil)
	}
	require.NoError(t, err)
	require.NotNil(t, drmAsset)

	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	require.NotNil(t, asset)

	tmpDir := t.TempDir()
	cases := []struct {
		name     string
		groupIdx int
		trackNr  int
	}{
		{"video_400kbps_avc", 0, 0},
		{"video_400kbps_hevc", 0, 1},
		{"audio_128kbps", 1, 0},
		{"audio_monotonic_128kbps_opus", 1, 1},
	}

	encryptionStatuses := []string{"original", "encrypted"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var files []*mp4.File
			originalTrack := asset.Groups[tc.groupIdx].Tracks[tc.trackNr]
			for _, encryptionStatus := range encryptionStatuses {
				var tr ContentTrack
				switch encryptionStatus {
				case "original":
					tr = originalTrack
				case "encrypted":
					protectedName := originalTrack.Name + suffix
					found := false
					for _, cand := range drmAsset.Groups[tc.groupIdx].Tracks {
						if cand.Name == protectedName {
							tr = cand
							found = true
							break
						}
					}
					require.True(t, found, "could not find protected track %s", protectedName)
				}
				outFile := filepath.Join(tmpDir, tc.name+encryptionStatus+".mp4")
				ofh, err := os.Create(outFile)
				require.NoError(t, err)
				spc := tr.SpecData
				initData, err := spc.GenCMAFInitData()
				require.NoError(t, err)
				_, err = ofh.Write(initData)
				require.NoError(t, err)
				nrSamples := int(3 * tr.TimeScale / tr.SampleDur)
				groupNr := uint32(0)
				for nr := 0; nr < nrSamples; nr++ {
					chunk, err := tr.GenCMAFChunk(groupNr, uint64(nr), uint64(nr+1))
					if err != nil {
						t.Fatalf("chunk generation failed for track=%s codec=%s scheme=%s encStatus=%s chunkNr=%d: %v",
							tr.Name, tr.SpecData.Codec(), drm.ContentProtections[0].Scheme, encryptionStatus, nr, err)
					}
					_, err = ofh.Write(chunk)
					require.NoError(t, err)
				}
				ofh.Close()
				fh, err := os.Open(outFile)
				require.NoError(t, err)
				defer fh.Close()
				mp4f, err := mp4.DecodeFile(fh)
				require.NoError(t, err)

				//Decrypt CENC encrypted file and remove protection information
				if encryptionStatus == "encrypted" {
					sw := bits.NewFixedSliceWriter(int(mp4f.Init.Size()))
					err = mp4f.Init.EncodeSW(sw)
					require.NoError(t, err)
					decryptedInit, _, ipd, err := DecryptInit(sw.Bytes())
					require.NoError(t, err)

					// Replace encrypted init with decrypted one
					initDecoded, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(decryptedInit))
					require.NoError(t, err)
					mp4f.Init = initDecoded.Init

					for _, seg := range mp4f.Segments {
						for i, frag := range seg.Fragments {
							sw = bits.NewFixedSliceWriter(int(frag.Size()))
							err = frag.EncodeSW(sw)
							require.NoError(t, err)
							decPayload, err := DecryptFragment(sw.Bytes(), ipd, drm.cenc.key)
							require.NoError(t, err)

							fsr := bits.NewFixedSliceReader(decPayload)
							fDec, err := mp4.DecodeFileSR(fsr)
							require.NoError(t, err)
							//Replace encrypted fragment with unencrypted fragment
							seg.Fragments[i] = fDec.Segments[0].Fragments[0]
						}
					}
				}
				files = append(files, mp4f)
			}
			// Encode original to bytes
			sw0 := bits.NewFixedSliceWriter(int(files[0].Size()))
			err = files[0].EncodeSW(sw0)
			require.NoError(t, err)

			// Encode decrypted to bytes
			sw1 := bits.NewFixedSliceWriter(int(files[1].Size()))
			err = files[1].EncodeSW(sw1)
			require.NoError(t, err)

			// Check that the encoded bytes match exactly
			require.Equal(t, sw0.Bytes(), sw1.Bytes(), "Encoded files should be identical")
		})
	}
}
