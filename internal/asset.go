package internal

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	trackID           = 1
	cmafOverheadBytes = 112 // moof + mdat header size for one sample
)

type ContentTrack struct {
	Name          string
	ContentType   string
	Language      string
	SampleBitrate uint32
	TimeScale     uint32
	Duration      uint32
	GopLength     uint32
	SampleDur     uint32
	NrSamples     uint32
	LoopDur       uint32 // Loop duration in local timescale
	SampleBatch   int
	Samples       []mp4.FullSample
	SpecData      CodecSpecificData
	cenc          *CENCInfo
	ipd           *mp4.InitProtectData
}

type CENCInfo struct {
	scheme    string
	kid       mp4.UUID
	key       []byte
	iv        []byte
	psshBoxes []*mp4.PsshBox
}

type Asset struct {
	Name           string
	Groups         []TrackGroup
	LoopDurMS      uint32
	SubtitleTracks []*SubtitleTrack
}

type CodecSpecificData interface {
	GenCMAFInitData() ([]byte, error)
	Codec() string
	GetInit() *mp4.InitSegment
}

type TrackGroup struct {
	AltGroupID uint32
	Tracks     []ContentTrack
}

// GetTrackByName returns a pointer to a ContentTrack with the given name, or nil if not found.
func (a *Asset) GetTrackByName(name string) *ContentTrack {
	for _, group := range a.Groups {
		for _, ct := range group.Tracks {
			if ct.Name == name {
				return &ct
			}
		}
	}
	return nil
}

// GetSubtitleTrackByName returns a pointer to a SubtitleTrack with the given name, or nil if not found.
func (a *Asset) GetSubtitleTrackByName(name string) *SubtitleTrack {
	for _, st := range a.SubtitleTracks {
		if st.Name == name {
			return st
		}
	}
	return nil
}

// AddSubtitleTracks adds WVTT and STPP subtitle tracks for the given languages.
// wvttLangs and stppLangs are lists of language codes (e.g., "en", "sv").
// Track names are formatted as "subs_wvtt_{lang}" and "subs_stpp_{lang}".
func (a *Asset) AddSubtitleTracks(wvttLangs, stppLangs []string) error {
	// Create WVTT tracks
	for _, lang := range wvttLangs {
		name := fmt.Sprintf("subs_wvtt_%s", lang)
		track, err := NewSubtitleTrack(name, SubtitleFormatWVTT, lang)
		if err != nil {
			return fmt.Errorf("failed to create WVTT subtitle track for %s: %w", lang, err)
		}
		a.SubtitleTracks = append(a.SubtitleTracks, track)
	}

	// Create STPP tracks
	for _, lang := range stppLangs {
		name := fmt.Sprintf("subs_stpp_%s", lang)
		track, err := NewSubtitleTrack(name, SubtitleFormatSTPP, lang)
		if err != nil {
			return fmt.Errorf("failed to create STPP subtitle track for %s: %w", lang, err)
		}
		a.SubtitleTracks = append(a.SubtitleTracks, track)
	}

	return nil
}

// InitContentTrack initializes a ContentTrack from an io.Reader (expects a fragmented MP4).
// The name is stripped of any extension.
func InitContentTrack(r io.Reader, name string, audioSampleBatch, videoSampleBatch int, cenc *CENCInfo) (*ContentTrack, error) {
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
	if ext := filepath.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	ct := ContentTrack{
		TimeScale: mdia.Mdhd.Timescale,
		Language:  mdia.Mdhd.GetLanguage(),
		Name:      name,
		cenc:      cenc,
	}
	sampleDesc, err := mdia.Minf.Stbl.Stsd.GetSampleDescription(0)
	if err != nil {
		return nil, fmt.Errorf("could not get sample description: %w", err)
	}
	switch sampleDesc.Type() {
	case "avc1", "avc3", "hvc1", "hev1":
		ct.ContentType = "video"
		ct.SampleBatch = videoSampleBatch
	case "mp4a", "Opus", "ac-3", "ec-3":
		ct.ContentType = "audio"
		ct.SampleBatch = audioSampleBatch
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
			ct.Samples = append(ct.Samples, fs...)
		}
	}
	for i, s := range ct.Samples {
		if ct.SampleDur == 0 {
			ct.SampleDur = s.Dur
		} else {
			// Last sample may have different duration, but all other should be same
			if s.Dur != ct.SampleDur && i != len(ct.Samples)-1 {
				return nil, fmt.Errorf("sample duration is not consistent")
			}
		}
	}
	timeOffset := uint64(0)
	if ct.ContentType == "audio" {
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
		for _, s := range ct.Samples {
			if s.DecodeTime < timeOffset {
				firsIdx++
			} else {
				break
			}
		}
		ct.Samples = ct.Samples[firsIdx:]
		for i := range ct.Samples {
			ct.Samples[i].DecodeTime -= timeOffset
		}
	}

	if ct.Samples[0].IsSync() {
		lastSync := 0
		for i := 1; i < len(ct.Samples); i++ {
			if ct.Samples[i].IsSync() {
				gopLen := i - lastSync
				if ct.GopLength == 0 {
					ct.GopLength = uint32(gopLen)
				} else {
					if ct.GopLength != uint32(gopLen) {
						return nil, fmt.Errorf("gop length is not consistent")
					}
				}
				lastSync = i
			}
		}
	}

	switch sampleDesc.Type() {
	case "avc1", "avc3":
		ct.SpecData, err = initAVCData(init, ct.Samples)
		if err != nil {
			return nil, fmt.Errorf("could not initialize AVC data: %w", err)
		}
	case "hvc1", "hev1":
		ct.SpecData, err = initHEVCData(init, ct.Samples)
		if err != nil {
			return nil, fmt.Errorf("could not initialize HEVC data: %w", err)
		}
	case "mp4a":
		ct.SpecData, err = initAACData(init)
		if err != nil {
			return nil, fmt.Errorf("could not initialize AAC data: %w", err)
		}
	case "Opus":
		ct.SpecData, err = initOpusData(init)
		if err != nil {
			return nil, fmt.Errorf("could not initialize Opus data: %w", err)
		}
	case "ac-3":
		ct.SpecData, err = initAC3Data(init)
		if err != nil {
			return nil, fmt.Errorf("could not initialize AC-3 data: %w", err)
		}
	case "ec-3":
		ct.SpecData, err = initEC3Data(init)
		if err != nil {
			return nil, fmt.Errorf("could not initialize EC-3 data: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown sample description type: %s", sampleDesc.Type())
	}
	if ct.cenc != nil {
		ipd, err := mp4.InitProtect(ct.SpecData.GetInit(), cenc.key, cenc.iv, cenc.scheme, cenc.kid, cenc.psshBoxes)
		if err != nil {
			return nil, fmt.Errorf("unable to add protection data to Init: %w", err)
		}
		ct.ipd = ipd
	}
	ct.Duration = uint32(len(ct.Samples)) * ct.SampleDur
	ct.NrSamples = uint32(len(ct.Samples))
	// Calculate sampleBitrate (bits per second)
	totalBytes := 0
	for _, s := range ct.Samples {
		totalBytes += int(s.Size)
	}
	durationSeconds := float64(ct.Duration) / float64(ct.TimeScale)
	if durationSeconds > 0 {
		ct.SampleBitrate = uint32(float64(totalBytes*8) / durationSeconds)
	}

	return &ct, nil
}

// LoadAsset opens a directory, reads all *.mp4 files, creates ContentTrack from each,
// groups them by contentType, and returns a pointer to an Asset.
func LoadAsset(dirPath string, audioSampleBatch, videoSampleBatch int) (*Asset, error) {
	return LoadAssetWithCENCInfo(dirPath, audioSampleBatch, videoSampleBatch, nil)
}

// LoadAssetWithCENCInfo opens a directory, reads all *.mp4 files, creates ContentTrack from each,
// groups them by contentType, and returns a pointer to an Asset. If cenc is not nil, it also encrypts every ContentTrack during CMAF chunk generation.
func LoadAssetWithCENCInfo(dirPath string, audioSampleBatch, videoSampleBatch int, cenc *CENCInfo) (*Asset, error) {
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
		ct, err := InitContentTrack(fh, entry.Name(), audioSampleBatch, videoSampleBatch, cenc)
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("could not create ContentTrack for %s: %w", filePath, err)
		}
		tracksByType[ct.ContentType] = append(tracksByType[ct.ContentType], *ct)
	}
	var groups []TrackGroup
	groupID := uint32(1)
	// Add video group(s) first
	if videoTracks, ok := tracksByType["video"]; ok {
		sort.Slice(videoTracks, func(i, j int) bool {
			return videoTracks[i].SampleBitrate < videoTracks[j].SampleBitrate
		})
		for i := 0; i < len(videoTracks); i++ {
			if videoTracks[i].Duration != videoTracks[0].Duration {
				return nil, fmt.Errorf("video tracks have different durations")
			}
		}
		groups = append(groups, TrackGroup{
			AltGroupID: groupID,
			Tracks:     videoTracks,
		})
		groupID++
	}

	// Then audio group(s)
	if audioTracks, ok := tracksByType["audio"]; ok {
		sort.Slice(audioTracks, func(i, j int) bool {
			return audioTracks[i].SampleBitrate < audioTracks[j].SampleBitrate
		})
		groups = append(groups, TrackGroup{
			AltGroupID: groupID,
			Tracks:     audioTracks,
		})
	}

	asset := &Asset{
		Name:   filepath.Base(dirPath),
		Groups: groups,
	}
	if err := asset.setLoopDuration(); err != nil {
		return nil, fmt.Errorf("could not set loop duration: %w", err)
	}
	return asset, nil
}

// ParseCENCflags converts the string CENC-related parameters into a CENCInfo struct. If all flags are empty nil is returned.
func ParseCENCflags(scheme, kidStr, keyStr, ivStr string) (*CENCInfo, error) {
	if kidStr == "" && keyStr == "" && ivStr == "" {
		return nil, nil
	}

	kid, err := mp4.UnpackKey(kidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid key ID %s: %w", kidStr, err)
	}
	kidHex := hex.EncodeToString(kid)
	kidUUID, _ := mp4.NewUUIDFromString(kidHex)

	if scheme != "cenc" && scheme != "cbcs" {
		return nil, fmt.Errorf("scheme must be cenc or cbcs: %s", scheme)
	}

	if len(ivStr) != 32 && len(ivStr) != 16 {
		return nil, fmt.Errorf("hex iv must have length 16 or 32 chars; %d", len(ivStr))
	}
	iv, err := hex.DecodeString(ivStr)
	if err != nil {
		return nil, fmt.Errorf("invalid iv %s", ivStr)
	}

	if keyStr != "" && len(keyStr) != 32 {
		return nil, fmt.Errorf("hex key must have length 32 chars: %d", len(keyStr))
	}

	var key mp4.UUID
	if keyStr == "" {
		key = kidUUID
	} else {
		key, err = mp4.UnpackKey(keyStr)
		if err != nil {
			return nil, fmt.Errorf("invalid key %s, %w", keyStr, err)
		}
	}
	psshBoxes, err := createClearKeyPssh(kidUUID)
	if err != nil {
		return nil, fmt.Errorf("could not create ClearKey PSSH: %w", err)
	}

	cencInfo := CENCInfo{
		scheme:    scheme,
		kid:       kidUUID,
		key:       key,
		iv:        iv,
		psshBoxes: psshBoxes,
	}
	return &cencInfo, nil
}

// createClearKeyPssh creates a PsshBox using the provided key-id
func createClearKeyPssh(kid mp4.UUID) ([]*mp4.PsshBox, error) {
	const clearKeySystemID = "1077efecc0b24d02ace33c1e52e2fb4b"

	systemID, err := mp4.NewUUIDFromString(clearKeySystemID)
	if err != nil {
		return nil, fmt.Errorf("invalid ClearKey system ID: %w", err)
	}

	psshBox := &mp4.PsshBox{
		Version:  1,
		Flags:    0,
		SystemID: systemID,
		KIDs:     []mp4.UUID{kid},
		Data:     nil,
	}

	return []*mp4.PsshBox{psshBox}, nil
}

// setLoopDuration set a loop duration for all tracks in the asset
// based on the first track in the first group.
// All the tracks in the first group must have durations that
// are equal to the loopDuration in their timeScale.
func (a *Asset) setLoopDuration() error {
	if len(a.Groups) == 0 {
		return fmt.Errorf("no tracks found")
	}
	loopDurMS := a.Groups[0].Tracks[0].Duration * 1000 / a.Groups[0].Tracks[0].TimeScale
	for gNr, group := range a.Groups {
		for tNr, track := range group.Tracks {
			switch {
			case gNr == 0:
				if track.Duration*1000 != loopDurMS*track.TimeScale {
					return fmt.Errorf("group %d track %s not compatible with loop duration", gNr, track.Name)
				}
				group.Tracks[tNr].LoopDur = track.Duration
			case gNr > 0 && track.ContentType == "audio":
				if track.Duration*1000 < loopDurMS*track.TimeScale {
					return fmt.Errorf("group %d audio track %s not compatible with loop duration", gNr, track.Name)
				}
				group.Tracks[tNr].LoopDur = loopDurMS * track.TimeScale / 1000
			default:
				if track.Duration*1000 != loopDurMS*track.TimeScale {
					return fmt.Errorf("group %d track %s not compatible with loop duration", gNr, track.Name)
				}
				group.Tracks[tNr].LoopDur = track.Duration
			}
		}
	}
	a.LoopDurMS = loopDurMS
	return nil
}

// GenCMAFCatalogEntry generates an MSF/CMSF catalog entry for this asset, populating all available fields.
// Conforms to draft-ietf-moq-msf-00 and draft-ietf-moq-cmsf-00.
// The generatedAtMS parameter is the wallclock time in milliseconds since the Unix epoch
// to be set as the catalog's generatedAt value.
func (a *Asset) GenCMAFCatalogEntry(generatedAtMS int64) (*Catalog, error) {
	var tracks []Track
	renderGroup := 1
	for _, group := range a.Groups {
		altGroup := int(group.AltGroupID)
		for _, ct := range group.Tracks {
			initData := ""
			if ct.SpecData != nil {
				data, err := ct.SpecData.GenCMAFInitData()
				if err != nil {
					return nil, fmt.Errorf("could not generate init data for track %s: %w", ct.Name, err)
				}
				initData = base64.StdEncoding.EncodeToString(data)
			}

			frameRate := float64(ct.TimeScale) / float64(ct.SampleDur)
			cmafBitrate := calcCmafBitrate(ct.SampleBitrate, frameRate, ct.SampleBatch)

			track := Track{
				Name:        ct.Name,
				Packaging:   "cmaf",
				IsLive:      true,
				RenderGroup: &renderGroup,
				AltGroup:    &altGroup,
				InitData:    initData,
				Codec:       ct.SpecData.Codec(),
				Bitrate:     &cmafBitrate,
				Timescale:   Ptr(int(ct.TimeScale)),
				Language:    ct.Language,
			}

			// Populate optional fields if available
			switch ct.ContentType {
			case "video":
				track.Role = "video"
				track.MimeType = "video/mp4"
				track.Framerate = Ptr(frameRate)
				switch sd := ct.SpecData.(type) {
				case *AVCData:
					if sd.width != 0 {
						track.Width = Ptr(int(sd.width))
					}
					if sd.height != 0 {
						track.Height = Ptr(int(sd.height))
					}
				case *HEVCData:
					if sd.width != 0 {
						track.Width = Ptr(int(sd.width))
					}
					if sd.height != 0 {
						track.Height = Ptr(int(sd.height))
					}
				}
			case "audio":
				track.Role = "audio"
				track.MimeType = "audio/mp4"
				switch sd := ct.SpecData.(type) {
				case *AACData:
					if sd.sampleRate != 0 {
						track.SampleRate = Ptr(int(sd.sampleRate))
					}
					if sd.channelConfig != "" {
						track.ChannelConfig = sd.channelConfig
					}
				case *OpusData:
					if sd.sampleRate != 0 {
						track.SampleRate = Ptr(int(sd.sampleRate))
					}
					if sd.channelConfig != "" {
						track.ChannelConfig = sd.channelConfig
					}
				case *AC3Data:
					if sd.sampleRate != 0 {
						track.SampleRate = Ptr(int(sd.sampleRate))
					}
					if sd.channelConfig != "" {
						track.ChannelConfig = sd.channelConfig
					}
				}
			}
			track.Namespace = Namespace
			track.Name = ct.Name
			tracks = append(tracks, track)
		}
	}

	// Add subtitle tracks to catalog
	// Group by format: WVTT tracks in one altGroup, STPP in another
	wvttAltGroup := len(a.Groups) + 1
	stppAltGroup := len(a.Groups) + 2

	for _, st := range a.SubtitleTracks {
		initData := ""
		if st.SpecData != nil {
			data, err := st.SpecData.GenCMAFInitData()
			if err != nil {
				return nil, fmt.Errorf("could not generate init data for subtitle track %s: %w", st.Name, err)
			}
			initData = base64.StdEncoding.EncodeToString(data)
		}

		// Subtitles are encapsulated in MP4 (CMAF)
		mimeType := "application/mp4"

		// Determine altGroup based on format
		altGroup := wvttAltGroup
		if st.Format == SubtitleFormatSTPP {
			altGroup = stppAltGroup
		}

		track := Track{
			Name:        st.Name,
			Namespace:   Namespace,
			Packaging:   "cmaf",
			IsLive:      true,
			Role:        "subtitle",
			RenderGroup: &renderGroup,
			AltGroup:    &altGroup,
			InitData:    initData,
			Codec:       st.SpecData.Codec(),
			MimeType:    mimeType,
			Timescale:   Ptr(int(st.TimeScale)),
			Language:    st.Language,
		}
		tracks = append(tracks, track)
	}

	cat := &Catalog{
		Version:     1,
		GeneratedAt: &generatedAtMS,
		Tracks:      tracks,
	}
	return cat, nil
}

func calcCmafBitrate(sampleBitrate uint32, frameRate float64, sampleBatch int) int {
	objectRate := frameRate / float64(sampleBatch)
	cmafChunkOverhead := cmafOverheadBytes + (sampleBatch-1)*8
	return int(float64(sampleBitrate) + 8*float64(cmafChunkOverhead)*objectRate)
}

// Ptr returns a pointer to any value
func Ptr[T any](v T) *T {
	return &v
}

// GenCMAFChunk returns a raw CMAF chunk consisting of endNr-startNr samples.
// The number is 0-based relative to the UNIX epoch.
// Therefore nr is translated into data for the time interval
// [nr*d.sampleDur, (nr+1)*d.sampleDur].
// This is calculated based on wrap-around given the loopDuration
// of the asset.
func (t *ContentTrack) GenCMAFChunk(chunkNr uint32, startNr, endNr uint64) ([]byte, error) {
	f, err := mp4.CreateFragment(chunkNr, trackID)
	if err != nil {
		return nil, err
	}
	for sampleNr := startNr; sampleNr < endNr; sampleNr++ {
		startTime, origNr := t.calcSample(uint64(sampleNr))
		orig := t.Samples[origNr]
		fs := mp4.FullSample{
			Sample: mp4.Sample{
				Flags: orig.Flags,
				Dur:   uint32(t.SampleDur),
				Size:  uint32(len(orig.Data)),
			},
			DecodeTime: startTime,
			Data:       orig.Data,
		}
		f.AddFullSample(fs)
	}
	f.SetTrunDataOffsets()

	// Encode the unencrypted fragment to bytes first
	size := f.Size()
	sw := bits.NewFixedSliceWriter(int(size))
	err = f.EncodeSW(sw)
	if err != nil {
		return nil, err
	}

	if t.cenc != nil {
		encrypted, err := t.encryptFragment(sw.Bytes())
		if err != nil {
			return nil, err
		}
		return encrypted, nil
	}

	return sw.Bytes(), nil
}

// calcSample calculates the start time and original sample number for a given output sample number.
func (t *ContentTrack) calcSample(nr uint64) (startTime, origNr uint64) {
	sampleDur := uint64(t.SampleDur)
	startTime = nr * uint64(t.SampleDur)
	nrWraps := startTime / uint64(t.LoopDur)
	wrapTime := nrWraps * uint64(t.LoopDur)
	if lacking := wrapTime % sampleDur; lacking > 0 {
		offset := sampleDur - lacking
		wrapTime += offset
	}
	deltaTime := startTime - wrapTime

	origNr = deltaTime / sampleDur
	return startTime, origNr
}

// encryptFragment encrypts an encoded fragment and returns the encrypted bytes. For mp4ff.EncryptFragment to work the fragment is first decoded, then encrypted, then finally encoded.
func (t *ContentTrack) encryptFragment(fragmentBytes []byte) ([]byte, error) {
	bytesReader := bytes.NewReader(fragmentBytes)
	var pos uint64 = 0
	moofBox, err := mp4.DecodeBox(pos, bytesReader)
	if err != nil {
		return nil, fmt.Errorf("unable to decode moof: %w", err)
	}
	moof, ok := moofBox.(*mp4.MoofBox)
	if !ok {
		return nil, fmt.Errorf("expected moof box, got %T", moofBox)
	}
	pos += moof.Size()
	mdatBox, err := mp4.DecodeBox(pos, bytesReader)
	if err != nil {
		return nil, fmt.Errorf("unable to decode mdat: %w", err)
	}
	mdat, ok := mdatBox.(*mp4.MdatBox)
	if !ok {
		return nil, fmt.Errorf("expected mdat box, got %T", mdatBox)
	}

	decodedFrag := mp4.NewFragment()
	decodedFrag.AddChild(moof)
	decodedFrag.AddChild(mdat)

	err = mp4.EncryptFragment(decodedFrag, t.cenc.key, t.cenc.iv, t.ipd)
	if err != nil {
		return nil, fmt.Errorf("unable to encrypt fragment: %w", err)
	}
	sw := bits.NewFixedSliceWriter(int(decodedFrag.Size()))
	err = decodedFrag.EncodeSW(sw)
	if err != nil {
		return nil, fmt.Errorf("unable to encode encrypted fragment: %w", err)
	}
	return sw.Bytes(), nil
}
