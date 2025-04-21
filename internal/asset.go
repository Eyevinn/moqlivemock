package internal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	trackID = 1
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
	loopDur       uint32 // Loop duration in local timescale
	samples       []mp4.FullSample
	specData      CodecSpecificData
}

type Asset struct {
	name      string
	groups    []TrackGroup
	loopDurMS uint32
}

type CodecSpecificData interface {
	GenCMAFInitData() ([]byte, error)
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
	}
	ct.name = ct.contentType
	sampleDesc, err := mdia.Minf.Stbl.Stsd.GetSampleDescription(0)
	if err != nil {
		return nil, fmt.Errorf("could not get sample description: %w", err)
	}
	switch sampleDesc.Type() {
	case "avc1", "avc3":
		ct.contentType = "video"
	case "mp4a":
		ct.contentType = "audio"
	default:
		return nil, fmt.Errorf("unsupported sample description type: %s", sampleDesc.Type())
	}
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
		for i := range ct.samples {
			ct.samples[i].DecodeTime -= timeOffset
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

	switch sampleDesc.Type() {
	case "avc1", "avc3":
		ct.specData, err = initAVCData(init, ct.samples)
		if err != nil {
			return nil, fmt.Errorf("could not initialize AVC data: %w", err)
		}
	case "mp4a":
		ct.specData, err = initAACData(init)
		if err != nil {
			return nil, fmt.Errorf("could not initialize AAC data: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown sample description type: %s", sampleDesc.Type())
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
		for i := 0; i < len(videoTracks); i++ {
			if videoTracks[i].duration != videoTracks[0].duration {
				return nil, fmt.Errorf("video tracks have different durations")
			}
		}
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
	asset := &Asset{
		name:   filepath.Base(dirPath),
		groups: groups,
	}
	if err := asset.setLoopDuration(); err != nil {
		return nil, fmt.Errorf("could not set loop duration: %w", err)
	}
	return asset, nil
}

// setLoopDuration set a loop duration for all tracks in the asset
// based on the first track in the first group.
// All the tracks in the first group must have durations that
// are equal to the loopDuration in their timeScale.
func (a *Asset) setLoopDuration() error {
	if len(a.groups) == 0 {
		return fmt.Errorf("no tracks found")
	}
	loopDurMS := a.groups[0].tracks[0].duration * 1000 / a.groups[0].tracks[0].timeScale
	for gNr, group := range a.groups {
		for tNr, track := range group.tracks {
			switch {
			case gNr == 0:
				if track.duration*1000 != loopDurMS*track.timeScale {
					return fmt.Errorf("group %d track %s not compatible with loop duration", gNr, track.name)
				}
				group.tracks[tNr].loopDur = track.duration
			case gNr > 0 && track.contentType == "audio":
				if track.duration*1000 < loopDurMS*track.timeScale {
					return fmt.Errorf("group %d audio track %s not compatible with loop duration", gNr, track.name)
				}
				group.tracks[tNr].loopDur = loopDurMS * track.timeScale / 1000
			default:
				if track.duration*1000 != loopDurMS*track.timeScale {
					return fmt.Errorf("group %d track %s not compatible with loop duration", gNr, track.name)
				}
				group.tracks[tNr].loopDur = track.duration
			}
		}
	}
	a.loopDurMS = loopDurMS
	return nil
}

// GetCMAFChunk returns a raw CMAF chunk consisting of one sample.
// The number is 0-based relative to the UNIX epoch.
// Therefore nr is translated into data for the time interval
// [nr*d.sampleDur, (nr+1)*d.sampleDur].
// This is calculated based on wrap-around given the loopDuration
// of the asset.
func (t *ContentTrack) GetCMAFChunk(nr uint64) ([]byte, error) {
	startTime := nr * uint64(t.sampleDur)
	nrWraps := startTime / uint64(t.loopDur)
	wrapTime := nrWraps * uint64(t.loopDur)
	offset := uint64(0)
	if lacking := wrapTime % uint64(t.sampleDur); lacking > 0 {
		offset = uint64(t.sampleDur) - lacking
	}
	startTime += offset
	origNr := startTime / uint64(t.sampleDur) % uint64(t.nrSamples)
	data, err := t.genCMAFChunk(startTime, nr, origNr)
	if err != nil {
		return nil, fmt.Errorf("could not generate CMAF chunk: %w", err)
	}
	return data, nil
}

func (t *ContentTrack) genCMAFChunk(startTime uint64, nr uint64, origNr uint64) ([]byte, error) {
	f, err := mp4.CreateFragment(uint32(nr+1), trackID)
	if err != nil {
		return nil, err
	}
	orig := t.samples[origNr]
	fs := mp4.FullSample{
		Sample: mp4.Sample{
			Flags: orig.Flags,
			Dur:   uint32(t.sampleDur),
			Size:  uint32(len(orig.Data)),
		},
		DecodeTime: startTime,
		Data:       orig.Data,
	}
	f.AddFullSample(fs)
	f.Moof.Traf.OptimizeTfhdTrun()
	f.SetTrunDataOffsets()
	size := f.Size()
	sw := bits.NewFixedSliceWriter(int(size))
	err = f.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}
