package internal

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"
	"text/template"
	"time"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Subtitle constants
const (
	SubsTimeTimescale = 1000 // 1ms resolution
	DefaultCueDurMS   = 900  // Cue duration in ms
)

// SubtitleFormat represents the subtitle format type
type SubtitleFormat string

const (
	SubtitleFormatWVTT SubtitleFormat = "wvtt"
	SubtitleFormatSTPP SubtitleFormat = "stpp"
)

//go:embed stpptime.xml
var stppTimeTemplate string

//go:embed stpptimecue.xml
var stppTimeCueTemplate string

var stppTemplate *template.Template

func init() {
	var err error
	stppTemplate, err = template.New("stpptime.xml").Parse(stppTimeTemplate)
	if err != nil {
		panic(fmt.Sprintf("failed to parse stpptime.xml template: %v", err))
	}
	stppTemplate, err = stppTemplate.New("stpptimecue.xml").Parse(stppTimeCueTemplate)
	if err != nil {
		panic(fmt.Sprintf("failed to parse stpptimecue.xml template: %v", err))
	}
}

// SubtitleTrack represents a dynamically generated subtitle track
type SubtitleTrack struct {
	Name      string
	Format    SubtitleFormat
	Language  string
	TimeScale uint32
	CueDurMS  int
	Region    int // 0=bottom, 1=top
	SpecData  *SubtitleData
}

// SubtitleData implements CodecSpecificData interface for subtitles
type SubtitleData struct {
	format   SubtitleFormat
	language string
}

// GenCMAFInitData generates the CMAF init segment data for subtitles
func (d *SubtitleData) GenCMAFInitData() ([]byte, error) {
	var init *mp4.InitSegment
	switch d.format {
	case SubtitleFormatWVTT:
		init = createSubtitlesWvttInitSegment(d.language, SubsTimeTimescale)
	case SubtitleFormatSTPP:
		init = createSubtitlesStppInitSegment(d.language, SubsTimeTimescale)
	default:
		return nil, fmt.Errorf("unknown subtitle format: %s", d.format)
	}

	sw := bits.NewFixedSliceWriter(int(init.Size()))
	err := init.EncodeSW(sw)
	if err != nil {
		return nil, fmt.Errorf("failed to encode init segment: %w", err)
	}
	return sw.Bytes(), nil
}

// Codec returns the codec string for this subtitle format
func (d *SubtitleData) Codec() string {
	switch d.format {
	case SubtitleFormatWVTT:
		return "wvtt"
	case SubtitleFormatSTPP:
		return "stpp.ttml.im1t"
	default:
		return string(d.format)
	}
}

// NewSubtitleTrack creates a new subtitle track with the given parameters
func NewSubtitleTrack(name string, format SubtitleFormat, lang string) (*SubtitleTrack, error) {
	st := &SubtitleTrack{
		Name:      name,
		Format:    format,
		Language:  lang,
		TimeScale: SubsTimeTimescale,
		CueDurMS:  DefaultCueDurMS,
		Region:    0, // bottom by default
		SpecData: &SubtitleData{
			format:   format,
			language: lang,
		},
	}
	return st, nil
}

// createSubtitlesWvttInitSegment creates a WVTT init segment
func createSubtitlesWvttInitSegment(lang string, timescale uint32) *mp4.InitSegment {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, "wvtt", lang)
	trak := init.Moov.Trak
	_ = trak.SetWvttDescriptor("WEBVTT")
	return init
}

// createSubtitlesStppInitSegment creates an STPP init segment
func createSubtitlesStppInitSegment(lang string, timescale uint32) *mp4.InitSegment {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, "subt", lang)
	trak := init.Moov.Trak
	schemaLocation := ""
	auxiliaryMimeType := ""
	_ = trak.SetStppDescriptor("http://www.w3.org/ns/ttml", schemaLocation, auxiliaryMimeType)
	return init
}

// StppTimeData is information for creating an stpp media segment
type StppTimeData struct {
	Lang   string
	Region int
	Cues   []StppTimeCue
}

// StppTimeCue is cue information to put in template
type StppTimeCue struct {
	Id    string
	Begin string
	End   string
	Msg   string
}

// cueItvl represents a cue interval with media times and UTC second
type cueItvl struct {
	startMS, endMS, utcS int
}

// calcCueItvls calculates cue intervals for a segment
// All times are in milliseconds
func calcCueItvls(segStart, segDur, utcStart, cueDur int) []cueItvl {
	itvls := make([]cueItvl, 0, 2)

	diff := segStart - utcStart
	utcEndMS := utcStart + segDur

	cueFullS := int(math.Ceil(float64(cueDur) * 0.001))
	cueFullMS := cueFullS * 1000

	for utcS := utcStart / cueFullMS; utcS <= (utcStart+segDur)/cueFullMS; utcS += cueFullS {
		cueStartMS := utcS * 1000
		if cueStartMS == utcEndMS {
			break
		}
		ci := cueItvl{
			utcS:    utcS,
			startMS: cueStartMS,
			endMS:   cueStartMS + cueDur,
		}
		if ci.startMS < utcStart {
			ci.startMS = utcStart
		}
		if utcEndMS < ci.endMS {
			ci.endMS = utcEndMS
		}
		ci.startMS += diff
		ci.endMS += diff
		itvls = append(itvls, ci)
	}
	return itvls
}

// makeStppMessage makes a message for an stpp time cue
func makeStppMessage(lang string, utcMS, groupNr int) string {
	t := time.UnixMilli(int64(utcMS))
	utc := t.UTC().Format(time.RFC3339)
	return fmt.Sprintf("%s<br/>%s # %d", utc, lang, groupNr)
}

// msToTTMLTime returns a time that can be used in TTML
func msToTTMLTime(ms int) string {
	hours := ms / 3600_000
	ms %= 3600_000
	minutes := ms / 60_000
	ms %= 60_000
	seconds := ms / 1_000
	ms %= 1_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, ms)
}

// makeWvttCuePayload creates a WVTT cue payload
func makeWvttCuePayload(lang string, region, utcMS, groupNr int) []byte {
	t := time.UnixMilli(int64(utcMS))
	utc := t.UTC().Format(time.RFC3339)
	pl := mp4.PaylBox{
		CueText: fmt.Sprintf("%s\n%s # %d", utc, lang, groupNr),
	}
	vttc := mp4.VttcBox{}
	if region == 1 {
		sttg := mp4.SttgBox{
			Settings: "line:2",
		}
		vttc.AddChild(&sttg)
	}
	vttc.AddChild(&pl)
	sw := bits.NewFixedSliceWriter(int(vttc.Size()))
	err := vttc.EncodeSW(sw)
	if err != nil {
		panic("cannot write vttc")
	}
	return sw.Bytes()
}

// GenSubtitleGroup generates a MoQ group for subtitle content
func GenSubtitleGroup(st *SubtitleTrack, groupNr uint64, groupDurMS uint32) (*MoQGroup, error) {
	// Calculate timing - subtitle groups are 1 second aligned
	baseMediaDecodeTime := groupNr * uint64(groupDurMS)
	dur := groupDurMS

	// UTC time for cue content
	utcTimeMS := baseMediaDecodeTime

	var data []byte
	var err error

	switch st.Format {
	case SubtitleFormatWVTT:
		data, err = createSubtitlesWvttMediaData(uint32(groupNr), baseMediaDecodeTime, dur, st.Language,
			utcTimeMS, st.CueDurMS, st.Region)
	case SubtitleFormatSTPP:
		data, err = createSubtitlesStppMediaData(uint32(groupNr), baseMediaDecodeTime, dur, st.Language,
			utcTimeMS, st.CueDurMS, st.Region)
	default:
		return nil, fmt.Errorf("unknown subtitle format: %s", st.Format)
	}

	if err != nil {
		return nil, err
	}

	mg := &MoQGroup{
		id:         uint32(groupNr),
		startTime:  baseMediaDecodeTime,
		endTime:    baseMediaDecodeTime + uint64(dur),
		startNr:    groupNr,
		endNr:      groupNr + 1,
		MoQObjects: []MoQObject{data},
	}

	return mg, nil
}

// createSubtitlesWvttMediaData creates WVTT media segment data (raw bytes)
func createSubtitlesWvttMediaData(nr uint32, baseMediaDecodeTime uint64, dur uint32, lang string, utcTimeMS uint64,
	cueDurMS, region int) ([]byte, error) {
	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(nr, 1)
	if err != nil {
		return nil, err
	}
	seg.AddFragment(frag)

	cueItvls := calcCueItvls(int(baseMediaDecodeTime), int(dur), int(utcTimeMS), cueDurMS)
	currEnd := baseMediaDecodeTime
	vtte := []byte{0, 0, 0, 8, 0x76, 0x74, 0x74, 0x65} // Empty VTT cue box

	for _, ci := range cueItvls {
		start := ci.startMS
		end := ci.endMS
		cuePL := makeWvttCuePayload(lang, region, ci.utcS*1000, int(nr))
		if start > int(currEnd) {
			frag.AddFullSample(fullSample(int(currEnd), start, vtte))
		}
		frag.AddFullSample(fullSample(start, end, cuePL))
		currEnd = uint64(end)
	}
	segEnd := int(baseMediaDecodeTime) + int(dur)
	if int(currEnd) < segEnd {
		frag.AddFullSample(fullSample(int(currEnd), segEnd, vtte))
	}

	size := int(seg.Size())
	sw := bits.NewFixedSliceWriter(size)
	err = seg.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

// createSubtitlesStppMediaData creates STPP media segment data (raw bytes)
func createSubtitlesStppMediaData(nr uint32, baseMediaDecodeTime uint64, dur uint32, lang string, utcTimeMS uint64,
	cueDurMS, region int) ([]byte, error) {
	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(nr, 1)
	if err != nil {
		return nil, err
	}
	seg.AddFragment(frag)

	cueItvls := calcCueItvls(int(baseMediaDecodeTime), int(dur), int(utcTimeMS), cueDurMS)
	stppd := StppTimeData{
		Lang:   lang,
		Region: region,
		Cues:   make([]StppTimeCue, 0, len(cueItvls)),
	}
	for i, ci := range cueItvls {
		cue := StppTimeCue{
			Id:    fmt.Sprintf("%d-%d", nr, i),
			Begin: msToTTMLTime(ci.startMS),
			End:   msToTTMLTime(ci.endMS),
			Msg:   makeStppMessage(lang, ci.utcS*1000, int(nr)),
		}
		stppd.Cues = append(stppd.Cues, cue)
	}

	data := make([]byte, 0, 1024)
	buf := bytes.NewBuffer(data)

	err = stppTemplate.ExecuteTemplate(buf, "stpptime.xml", stppd)
	if err != nil {
		return nil, fmt.Errorf("execute stpp template: %w", err)
	}

	sampleData := buf.Bytes()
	s := mp4.Sample{
		Flags: mp4.SyncSampleFlags,
		Dur:   dur,
		Size:  uint32(len(sampleData)),
	}
	fs := mp4.FullSample{
		Sample:     s,
		DecodeTime: baseMediaDecodeTime,
		Data:       sampleData,
	}
	frag.AddFullSample(fs)

	size := int(seg.Size())
	sw := bits.NewFixedSliceWriter(size)
	err = seg.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

// fullSample creates a FullSample from start/end times and data
func fullSample(start int, end int, data []byte) mp4.FullSample {
	return mp4.FullSample{
		Sample: mp4.Sample{
			Flags: mp4.SyncSampleFlags,
			Dur:   uint32(end - start),
			Size:  uint32(len(data)),
		},
		DecodeTime: uint64(start),
		Data:       data,
	}
}

// CurrSubtitleGroupNr returns the current MoQ group number for subtitle tracks
func CurrSubtitleGroupNr(nowMS uint64, groupDurMS uint32) uint64 {
	return nowMS / uint64(groupDurMS)
}
