package internal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

type ContentTrack struct {
	name          string
	contentType   string
	language      string
	sampleBitrate uint32
	timeScale     uint32
	duration      uint32
	gopLength     uint32
	sampleDur     uint32
	nrSamples     uint32
	init          *mp4.InitSegment
	samples       []mp4.FullSample
}

type Asset struct {
	name   string
	groups []TrackGroup
}

type TrackGroup struct {
	altGroupID uint32
	tracks     []ContentTrack
}

// InitContentTrack initializes a ContentTrack from an io.Reader (expects a fragmented MP4)
func InitContentTrack(r io.Reader) (*ContentTrack, error) {
	m, err := mp4.DecodeFile(r)
	if err != nil {
		return nil, fmt.Errorf("could not decode file: %w", err)
	}
	if !m.IsFragmented() {
		return nil, fmt.Errorf("file is not fragmented")
	}
	if len(m.Moov.Traks) != 1 {
		return nil, fmt.Errorf("file has not exactly one track")
	}
	init := m.Init
	trak := init.Moov.Trak
	mdia := trak.Mdia
	ct := ContentTrack{
		timeScale: mdia.Mdhd.Timescale,
		language:  mdia.Mdhd.GetLanguage(),
		init:      init,
	}
	hdlr := mdia.Hdlr.HandlerType
	switch hdlr {
	case "vide":
		ct.contentType = "video"
	case "soun":
		ct.contentType = "audio"
	default:
		return nil, fmt.Errorf("unknown media type: %s", hdlr)
	}
	ct.name = ct.contentType
	trex := init.Moov.Mvex.Trex
	for _, seg := range m.Segments {
		for _, frag := range seg.Fragments {
			fs, err := frag.GetFullSamples(trex)
			if err != nil {
				return nil, fmt.Errorf("could not get full samples: %w", err)
			}
			ct.samples = append(ct.samples, fs...)
		}
	}
	for i, s := range ct.samples {
		if ct.sampleDur == 0 {
			ct.sampleDur = s.Dur
		} else {
			// Last sample may have different duration, but all other should be same
			if s.Dur != ct.sampleDur && i != len(ct.samples)-1 {
				return nil, fmt.Errorf("sample duration is not consistent")
			}
		}
	}
	timeOffset := uint64(0)
	if ct.contentType == "audio" {
		// Check edit list and possibly shift away a sample for proper
		// alignment when looping witoout edit list
		if trak.Edts != nil {
			if len(trak.Edts.Elst) != 1 {
				return nil, fmt.Errorf("edit list has not exactly than one edit")
			}
			elst := trak.Edts.Elst[0]
			if len(elst.Entries) != 1 {
				return nil, fmt.Errorf("edts has not exactly than one elst")
			}
			timeOffset = uint64(elst.Entries[0].MediaTime)
		}
	}
	if timeOffset > 0 {
		// Shift all timestamps by timeOffset, and remove samples with too small time
		// This means we can loop without edit list, since that only applies to
		// first time the sample is played. The loop transition may not be perfect
		// from an audible perspective. but the timestamps will be correct.
		firsIdx := 0
		for _, s := range ct.samples {
			if s.DecodeTime < timeOffset {
				firsIdx++
			} else {
				break
			}
		}
		ct.samples = ct.samples[firsIdx:]
		for _, s := range ct.samples {
			s.DecodeTime -= timeOffset
		}
	}

	if ct.samples[0].IsSync() {
		lastSync := 0
		for i := 1; i < len(ct.samples); i++ {
			if ct.samples[i].IsSync() {
				gopLen := i - lastSync
				if ct.gopLength == 0 {
					ct.gopLength = uint32(gopLen)
				} else {
					if ct.gopLength != uint32(gopLen) {
						return nil, fmt.Errorf("gop length is not consistent")
					}
				}
				lastSync = i
			}
		}
	}
	ct.duration = uint32(len(ct.samples)) * ct.sampleDur
	ct.nrSamples = uint32(len(ct.samples))
	// Calculate sampleBitrate (bits per second)
	totalBytes := 0
	for _, s := range ct.samples {
		totalBytes += int(s.Size)
	}
	durationSeconds := float64(ct.duration) / float64(ct.timeScale)
	if durationSeconds > 0 {
		ct.sampleBitrate = uint32(float64(totalBytes*8) / durationSeconds)
	}
	return &ct, nil
}

// LoadAsset opens a directory, reads all *.mp4 files, creates ContentTrack from each, groups them by contentType, and returns a pointer to an Asset.
func LoadAsset(dirPath string) (*Asset, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("could not read directory: %w", err)
	}
	tracksByType := make(map[string][]ContentTrack)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".mp4" {
			continue
		}
		filePath := filepath.Join(dirPath, entry.Name())
		fh, err := os.Open(filePath)
		if err != nil {
			return nil, fmt.Errorf("could not open file %s: %w", filePath, err)
		}
		ct, err := InitContentTrack(fh)
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("could not create ContentTrack for %s: %w", filePath, err)
		}
		ct.name = entry.Name()
		tracksByType[ct.contentType] = append(tracksByType[ct.contentType], *ct)
	}
	var groups []TrackGroup
	groupID := uint32(1)
	// Add video group(s) first
	if videoTracks, ok := tracksByType["video"]; ok {
		sort.Slice(videoTracks, func(i, j int) bool {
			return videoTracks[i].sampleBitrate < videoTracks[j].sampleBitrate
		})
		groups = append(groups, TrackGroup{
			altGroupID: groupID,
			tracks:     videoTracks,
		})
		groupID++
	}
	// Then audio group(s)
	if audioTracks, ok := tracksByType["audio"]; ok {
		sort.Slice(audioTracks, func(i, j int) bool {
			return audioTracks[i].sampleBitrate < audioTracks[j].sampleBitrate
		})
		groups = append(groups, TrackGroup{
			altGroupID: groupID,
			tracks:     audioTracks,
		})
		groupID++
	}
	// Then any other types
	for contentType, tracks := range tracksByType {
		if contentType == "video" || contentType == "audio" {
			continue
		}
		sort.Slice(tracks, func(i, j int) bool {
			return tracks[i].sampleBitrate < tracks[j].sampleBitrate
		})
		groups = append(groups, TrackGroup{
			altGroupID: groupID,
			tracks:     tracks,
		})
		groupID++
	}
	asset := &Asset{
		name:   filepath.Base(dirPath),
		groups: groups,
	}
	return asset, nil
}
