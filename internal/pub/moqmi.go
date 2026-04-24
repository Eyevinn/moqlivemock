package pub

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/moqmi"
)

// MoqMITrackMap maps moqmi-convention track names (video0, audio0, ...) to
// names of tracks in the asset. moqmi has no catalog, so the server advertises
// these fixed names by convention; this map provides the binding to real assets.
type MoqMITrackMap map[string]string

// PublishMoqMITrack publishes a single track using the moq-mi wire format
// (draft-cenzano-moq-media-interop-03). It attaches moqmi extension headers
// to every object and uses the codec-specific grouping rules:
//
//   - Video (H.264 AVCC): one group per GOP (IDR-bounded), object 0 of each
//     group carries the AVCDecoderConfigurationRecord in extension 0x0D.
//   - Audio (AAC-LC, Opus): one group per audio frame, single object per group.
//
// Payloads are the codec bitstream as defined by moqmi (AVCC length-prefixed
// NALUs for H.264, raw Opus packets, AAC raw_data_block).
func PublishMoqMITrack(ctx context.Context, publisher moqtransport.Publisher,
	asset *internal.Asset, assetTrackName, moqmiTrackName string) {
	ct := asset.GetTrackByName(assetTrackName)
	if ct == nil {
		slog.Error("moqmi: asset track not found", "track", assetTrackName)
		return
	}
	switch sd := ct.SpecData.(type) {
	case *internal.AVCData:
		publishMoqMIVideo(ctx, publisher, ct, sd, moqmiTrackName)
	case *internal.AACData:
		publishMoqMIAudio(ctx, publisher, ct, moqmi.MediaTypeAudioAACLC, moqmiTrackName)
	case *internal.OpusData:
		publishMoqMIAudio(ctx, publisher, ct, moqmi.MediaTypeAudioOpus, moqmiTrackName)
	default:
		slog.Error("moqmi: unsupported codec for moq-mi", "track", assetTrackName,
			"codec", ct.SpecData.Codec())
	}
}

func publishMoqMIVideo(ctx context.Context, publisher moqtransport.Publisher,
	ct *internal.ContentTrack, avcData *internal.AVCData, moqmiTrackName string) {
	extradata, err := avcData.GenAVCDecoderConfigurationRecord()
	if err != nil {
		slog.Error("moqmi: failed to build AVCDecoderConfigurationRecord",
			"track", moqmiTrackName, "error", err)
		return
	}
	gopLen := uint64(ct.GopLength)
	if gopLen == 0 {
		slog.Error("moqmi: unknown GOP length for video track", "track", moqmiTrackName)
		return
	}
	timebase := uint64(ct.TimeScale)
	sampleDur := uint64(ct.SampleDur)

	// Align start with the next GOP boundary in wallclock time so multiple
	// subscribers joining separately land on the same grouping.
	now := time.Now().UnixMilli()
	gopDurMS := gopLen * sampleDur * 1000 / timebase
	if gopDurMS == 0 {
		gopDurMS = 1000
	}
	currGopNr := uint64(now) / gopDurMS
	groupNr := currGopNr + 1

	slog.Info("moqmi: publishing video track",
		"track", moqmiTrackName, "startGroup", groupNr, "gopLen", gopLen)

	seqID := groupNr * gopLen
	for {
		if ctx.Err() != nil {
			return
		}
		sg, err := publisher.OpenSubgroup(groupNr, 0, MediaPriority)
		if err != nil {
			slog.Error("moqmi: failed to open subgroup", "error", err)
			return
		}
		startSample := groupNr * gopLen
		endSample := startSample + gopLen
		for objectID, sampleNr := uint64(0), startSample; sampleNr < endSample; objectID, sampleNr = objectID+1, sampleNr+1 {
			if ctx.Err() != nil {
				return
			}
			_, origNr := ct.CalcSample(sampleNr)
			sample := ct.Samples[origNr]
			pts := sampleNr * sampleDur
			ptsMS := int64(pts * 1000 / timebase)
			waitMS := ptsMS - time.Now().UnixMilli()
			if waitMS > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(waitMS) * time.Millisecond):
				}
			}
			meta := moqmi.VideoMetadata{
				SeqID:       seqID,
				PTS:         pts,
				DTS:         pts,
				Timebase:    timebase,
				Duration:    sampleDur,
				WallclockMS: uint64(time.Now().UnixMilli()),
			}
			var headers moqtransport.KVPList
			if objectID == 0 {
				headers = moqmi.VideoHeaders(meta, extradata)
			} else {
				headers = moqmi.VideoHeaders(meta, nil)
			}
			if _, err := sg.WriteObjectWithHeaders(objectID, headers, sample.Data); err != nil {
				slog.Error("moqmi: failed to write video object",
					"group", groupNr, "object", objectID, "error", err)
				return
			}
			seqID++
		}
		if err := sg.Close(); err != nil {
			slog.Error("moqmi: failed to close video subgroup", "error", err)
			return
		}
		slog.Debug("moqmi: published video group", "track", moqmiTrackName,
			"group", groupNr, "objects", gopLen)
		groupNr++
	}
}

func publishMoqMIAudio(ctx context.Context, publisher moqtransport.Publisher,
	ct *internal.ContentTrack, mediaType uint64, moqmiTrackName string) {
	timebase := uint64(ct.TimeScale)
	sampleDur := uint64(ct.SampleDur)
	if sampleDur == 0 {
		slog.Error("moqmi: zero sample duration", "track", moqmiTrackName)
		return
	}

	// Start on the next audio-frame boundary at or after now.
	now := time.Now().UnixMilli()
	framePTSUnits := uint64(now) * timebase / 1000
	frameNr := framePTSUnits / sampleDur
	if framePTSUnits%sampleDur != 0 {
		frameNr++
	}

	slog.Info("moqmi: publishing audio track",
		"track", moqmiTrackName, "startFrame", frameNr,
		"timebase", timebase, "frameDurUnits", sampleDur,
		"mediaType", mediaType)

	channels := audioChannels(ct.SpecData)

	seqID := frameNr
	for {
		if ctx.Err() != nil {
			return
		}
		pts := frameNr * sampleDur
		ptsMS := int64(pts * 1000 / timebase)
		waitMS := ptsMS - time.Now().UnixMilli()
		if waitMS > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(waitMS) * time.Millisecond):
			}
		}
		_, origNr := ct.CalcSample(frameNr)
		sample := ct.Samples[origNr]

		sg, err := publisher.OpenSubgroup(frameNr, 0, MediaPriority)
		if err != nil {
			slog.Error("moqmi: failed to open audio subgroup", "error", err)
			return
		}
		meta := moqmi.AudioMetadata{
			SeqID:       seqID,
			PTS:         pts,
			Timebase:    timebase,
			SampleFreq:  timebase,
			NumChannels: channels,
			Duration:    sampleDur,
			WallclockMS: uint64(time.Now().UnixMilli()),
		}
		var headers moqtransport.KVPList
		switch mediaType {
		case moqmi.MediaTypeAudioAACLC:
			headers = moqmi.AudioAACHeaders(meta)
		case moqmi.MediaTypeAudioOpus:
			headers = moqmi.AudioOpusHeaders(meta)
		default:
			slog.Error("moqmi: unsupported audio media type", "type", mediaType)
			_ = sg.Close()
			return
		}
		if _, err := sg.WriteObjectWithHeaders(0, headers, sample.Data); err != nil {
			slog.Error("moqmi: failed to write audio object",
				"frame", frameNr, "error", err)
			_ = sg.Close()
			return
		}
		if err := sg.Close(); err != nil {
			slog.Error("moqmi: failed to close audio subgroup", "error", err)
			return
		}
		seqID++
		frameNr++
	}
}

// audioChannels returns the numeric channel count for an audio SpecData.
// Falls back to 0 if the channel config cannot be parsed.
func audioChannels(sd internal.CodecSpecificData) uint64 {
	var cfg string
	switch s := sd.(type) {
	case *internal.AACData:
		cfg = s.ChannelConfig()
	case *internal.OpusData:
		cfg = s.ChannelConfig()
	default:
		return 0
	}
	n, err := strconv.ParseUint(cfg, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ResolveMoqMITrack maps a moqmi-convention track name to an asset track name.
// Returns "" if the moqmi track is not mapped.
func ResolveMoqMITrack(m MoqMITrackMap, moqmiName string) string {
	if m == nil {
		return ""
	}
	return m[moqmiName]
}

// BuildMoqMITrackMap inspects an asset and chooses default mappings for
// video0 (first AVC track) and audio0 (first AAC-LC track, else first Opus track).
func BuildMoqMITrackMap(asset *internal.Asset) (MoqMITrackMap, error) {
	m := MoqMITrackMap{}
	for _, group := range asset.Groups {
		for _, ct := range group.Tracks {
			if ct.Protection != internal.ProtectionNone {
				continue
			}
			switch ct.SpecData.(type) {
			case *internal.AVCData:
				if _, ok := m["video0"]; !ok {
					m["video0"] = ct.Name
				}
			case *internal.AACData:
				if _, ok := m["audio0"]; !ok {
					m["audio0"] = ct.Name
				}
			}
		}
	}
	// Fall back to Opus if no AAC was found.
	if _, ok := m["audio0"]; !ok {
		for _, group := range asset.Groups {
			for _, ct := range group.Tracks {
				if ct.Protection != internal.ProtectionNone {
					continue
				}
				if _, isOpus := ct.SpecData.(*internal.OpusData); isOpus {
					m["audio0"] = ct.Name
					break
				}
			}
			if _, ok := m["audio0"]; ok {
				break
			}
		}
	}
	if _, ok := m["video0"]; !ok {
		return nil, fmt.Errorf("no clear AVC video track available for moq-mi video0")
	}
	return m, nil
}
