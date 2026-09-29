package internal

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"math"
	"text/template"
	"time"

	"github.com/Eyevinn/locmaf"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Subtitle constants
const (
	SubsTimeTimescale = 1000 // 1ms resolution
	DefaultCueDurMS   = 900  // Cue duration in ms
)

// subsTrackID is the track ID of the generated subtitle tracks. There is only
// one track per init segment, and mp4.InitSegment.AddEmptyTrack numbers it from 1.
const subsTrackID = 1

// SubtitleFormat represents the subtitle format type
type SubtitleFormat string

const (
	SubtitleFormatWVTT SubtitleFormat = "wvtt"
	SubtitleFormatSTPP SubtitleFormat = "stpp"
	// SubtitleFormatWVTC and SubtitleFormatSTPC are the experimental
	// paint-model variants of wvtt and stpp: a chunk that restates what the
	// previous one said is sent as an 8-byte no-change box instead. Their
	// 4CCs (and those of the ttmn, ttmb and vttn boxes) are placeholders that
	// are NOT registered with MP4RA; see
	// https://github.com/Eyevinn/paint-model-subtitles.
	SubtitleFormatWVTC SubtitleFormat = "wvtc"
	SubtitleFormatSTPC SubtitleFormat = "stpc"
)

// isWebVTT tells whether samples of the format are WebVTT cue boxes rather
// than TTML documents.
func (f SubtitleFormat) isWebVTT() bool {
	return f == SubtitleFormatWVTT || f == SubtitleFormatWVTC
}

// isPaint tells whether the format is a paint-model variant.
func (f SubtitleFormat) isPaint() bool {
	return f == SubtitleFormatSTPC || f == SubtitleFormatWVTC
}

// redundantSampleFlags marks a sync sample whose payload repeats the preceding
// sample: sample_depends_on = 2 (no dependency, as all stpp and wvtt samples
// are sync samples) plus sample_has_redundancy = 1 (ISO/IEC 14496-12 Sec.
// 8.8.3.1). Sec. 8.6.4 lets a receiver discard such a sample and add its
// duration to the preceding one.
const redundantSampleFlags uint32 = mp4.SyncSampleFlags | 1<<20

// paintDependentFlags marks a sample that is not self-contained:
// sample_is_non_sync_sample = 1 and sample_depends_on = 1. Both a no-change
// box and a body-only box are such samples: they mean nothing without an
// earlier sample of the same group.
const paintDependentFlags uint32 = 1<<24 | mp4.NonSyncSampleFlags

// vtteBox is an empty WebVTT cue box, filling the intervals where nothing is
// shown. ttmnBox and vttnBox are the 8-byte no-change samples of stpc and wvtc.
// None of them is ever modified.
var (
	vtteBox = []byte{0, 0, 0, 8, 'v', 't', 't', 'e'}
	ttmnBox = encodeWholeSampleBox(&mp4.TtmnBox{})
	vttnBox = encodeWholeSampleBox(&mp4.VttnBox{})
)

// encodeWholeSampleBox encodes a box that is itself a complete sample.
func encodeWholeSampleBox(b mp4.Box) []byte {
	sw := bits.NewFixedSliceWriter(int(b.Size()))
	if err := b.EncodeSW(sw); err != nil {
		panic(fmt.Sprintf("cannot write %s box: %s", b.Type(), err))
	}
	return sw.Bytes()
}

//go:embed stpptime.xml
var stppTimeTemplate string

//go:embed stpptimebody.xml
var stppTimeBodyTemplate string

//go:embed stpptimecue.xml
var stppTimeCueTemplate string

var stppTemplate *template.Template

func init() {
	stppTemplate = template.Must(template.New("stpptime.xml").Parse(stppTimeTemplate))
	template.Must(stppTemplate.New("stpptimebody.xml").Parse(stppTimeBodyTemplate))
	template.Must(stppTemplate.New("stpptimecue.xml").Parse(stppTimeCueTemplate))
}

// SubtitleCadence is the MoQ object cadence of the subtitle tracks. Each
// subtitle object covers the same interval as one object of the reference
// video track, so a text change reaches the player with the latency of the
// picture it belongs to, and a group holds as many subtitle objects as video
// objects. The zero value means one object per group.
type SubtitleCadence struct {
	TimeScale   uint32 // timescale of the reference video track
	SampleDur   uint32 // video sample duration in TimeScale units
	SampleBatch int    // video samples per MoQ object
}

// chunkBoundsMS returns the n+1 boundaries, in ms, of the n objects of group
// groupNr. They are the start times of the video objects of the same group,
// computed exactly as for the video track, rounded to the subtitle timescale.
func (c SubtitleCadence) chunkBoundsMS(groupNr uint64, groupDurMS uint32) []uint64 {
	if c.TimeScale == 0 || c.SampleDur == 0 {
		start := groupNr * uint64(groupDurMS)
		return []uint64{start, start + uint64(groupDurMS)}
	}
	batch := uint64(max(c.SampleBatch, 1))
	startNr, endNr := calcGroupSampleRange(c.TimeScale, c.SampleDur, groupNr, groupDurMS)
	toMS := func(nr uint64) uint64 {
		return (nr*uint64(c.SampleDur)*SubsTimeTimescale + uint64(c.TimeScale)/2) / uint64(c.TimeScale)
	}
	bounds := make([]uint64, 0, (endNr-startNr)/batch+2)
	for nr := startNr; nr < endNr; nr += batch {
		ms := toMS(nr)
		if len(bounds) > 0 && ms == bounds[len(bounds)-1] {
			continue // Sub-millisecond video objects: merge rather than emit empty chunks.
		}
		bounds = append(bounds, ms)
	}
	if end := toMS(endNr); len(bounds) == 0 || end > bounds[len(bounds)-1] {
		bounds = append(bounds, end)
	}
	return bounds
}

// SubtitleTrack represents a dynamically generated subtitle track
type SubtitleTrack struct {
	Name      string
	Format    SubtitleFormat
	Language  string
	TimeScale uint32
	CueDurMS  int
	Region    int // 0=bottom, 1=top
	// Body sends a changed stpc chunk that is not the first of its group as a
	// ttmb box carrying only the TTML body; the receiver splices in the head
	// of the group's first document. stpc only.
	Body bool
	// Cadence is the object cadence, normally that of the asset's video.
	Cadence  SubtitleCadence
	SpecData *SubtitleData
}

// SubtitleData implements CodecSpecificData interface for subtitles
type SubtitleData struct {
	format   SubtitleFormat
	language string
	init     *mp4.InitSegment
}

// initSegment returns the track's init segment, created on first use.
func (d *SubtitleData) initSegment() (*mp4.InitSegment, error) {
	if d.init != nil {
		return d.init, nil
	}
	switch d.format {
	case SubtitleFormatWVTT:
		d.init = createSubtitlesWvttInitSegment(d.language, SubsTimeTimescale)
	case SubtitleFormatSTPP:
		d.init = createSubtitlesStppInitSegment(d.language, SubsTimeTimescale)
	case SubtitleFormatWVTC:
		d.init = createSubtitlesWvtcInitSegment(d.language, SubsTimeTimescale)
	case SubtitleFormatSTPC:
		d.init = createSubtitlesStpcInitSegment(d.language, SubsTimeTimescale)
	default:
		return nil, fmt.Errorf("unknown subtitle format: %s", d.format)
	}
	return d.init, nil
}

// GenCMAFInitData generates the CMAF init segment data for subtitles
func (d *SubtitleData) GenCMAFInitData() ([]byte, error) {
	init, err := d.initSegment()
	if err != nil {
		return nil, err
	}
	sw := bits.NewFixedSliceWriter(int(init.Size()))
	err = init.EncodeSW(sw)
	if err != nil {
		return nil, fmt.Errorf("failed to encode init segment: %w", err)
	}
	return sw.Bytes(), nil
}

// Codec returns the codec string for this subtitle format. The paint-model
// variants are not im1t: not all of their samples are IMSC documents.
func (d *SubtitleData) Codec() string {
	switch d.format {
	case SubtitleFormatSTPP:
		return "stpp.ttml.im1t"
	default:
		return string(d.format)
	}
}

// NewSubtitleTrack creates a new subtitle track with the given parameters.
// It has one object per group until its Cadence is set.
func NewSubtitleTrack(name string, format SubtitleFormat, lang string) (*SubtitleTrack, error) {
	switch format {
	case SubtitleFormatWVTT, SubtitleFormatSTPP, SubtitleFormatWVTC, SubtitleFormatSTPC:
	default:
		return nil, fmt.Errorf("unknown subtitle format: %s", format)
	}
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

// createSubtitlesWvtcInitSegment creates an init segment with the
// experimental paint-model wvtc sample entry.
func createSubtitlesWvtcInitSegment(lang string, timescale uint32) *mp4.InitSegment {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, "text", lang)
	trak := init.Moov.Trak
	_ = trak.SetWvtcDescriptor("WEBVTT")
	return init
}

// createSubtitlesStpcInitSegment creates an init segment with the
// experimental paint-model stpc sample entry.
func createSubtitlesStpcInitSegment(lang string, timescale uint32) *mp4.InitSegment {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, "subt", lang)
	trak := init.Moov.Trak
	_ = trak.SetStpcDescriptor("http://www.w3.org/ns/ttml", "", "")
	return init
}

// StppTimeData is information for creating an stpp media segment
type StppTimeData struct {
	Lang   string
	Region int
	Cues   []StppTimeCue
}

// StppTimeCue is cue information to put in template.
// End is empty for a cue that is still active at the end of the sample,
// in which case no end attribute is written.
type StppTimeCue struct {
	Id    string
	Begin string
	End   string
	Msg   string
}

// cueItvl represents a cue interval with media times and UTC second.
// startMS and endMS are the true times of the cue and are not clipped to the
// group or chunk that carries it.
type cueItvl struct {
	startMS, endMS, utcS int
}

// overlaps tells whether the cue is on screen at some point during [startMS, endMS).
func (c cueItvl) overlaps(startMS, endMS int) bool {
	return c.startMS < endMS && c.endMS > startMS
}

// calcCueItvls returns all cue intervals that overlap the interval of length
// durMS starting at media time startMS, which corresponds to UTC time
// utcStartMS. All times are in milliseconds.
//
// The cues repeat with a period of cueDurMS rounded up to a full second, and
// each starts on a UTC second that is a multiple of that period. The returned
// times are the true times of the cues: a cue that starts before startMS or
// ends after startMS+durMS keeps its own begin and end, and it is up to the
// caller to decide what to write out.
func calcCueItvls(startMS, durMS, utcStartMS, cueDurMS int) []cueItvl {
	itvls := make([]cueItvl, 0, 2)

	diff := startMS - utcStartMS
	utcEndMS := utcStartMS + durMS

	cueFullS := int(math.Ceil(float64(cueDurMS) * 0.001))
	if cueFullS <= 0 {
		return itvls
	}
	cueFullMS := cueFullS * 1000

	// First cue period starting at or before the start of the interval.
	firstS := utcStartMS / cueFullMS * cueFullS
	for utcS := firstS; utcS*1000 < utcEndMS; utcS += cueFullS {
		ci := cueItvl{
			utcS:    utcS,
			startMS: utcS*1000 + diff,
			endMS:   utcS*1000 + cueDurMS + diff,
		}
		if !ci.overlaps(startMS, startMS+durMS) {
			continue
		}
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

// stppCueID returns the xml:id of a cue. It identifies the cue itself - the
// UTC second it announces - and not its position in a group or chunk, so the
// same cue keeps the same id wherever it is restated.
func stppCueID(utcS int) string {
	return fmt.Sprintf("c%d", utcS) // xml:id is an NCName, so it cannot start with a digit
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

// makeWvttCuePayload creates a WVTT cue payload.
//
// The cue carries a vsid (CueSourceIDBox) whose source ID is the UTC second
// the cue announces. ISO/IEC 14496-30 Sec. 6.6 makes a matching source_ID the
// sign that the same cue is still active, so a cue restated in a later chunk
// is recognisable as the same one.
func makeWvttCuePayload(lang string, region, utcMS, groupNr int) []byte {
	t := time.UnixMilli(int64(utcMS))
	utc := t.UTC().Format(time.RFC3339)
	pl := mp4.PaylBox{
		CueText: fmt.Sprintf("%s\n%s # %d", utc, lang, groupNr),
	}
	vttc := mp4.VttcBox{}
	// The box order of 14496-30 Sec. 6.6 is vsid, iden, ctim, sttg, payl.
	vttc.AddChild(&mp4.VsidBox{SourceID: uint32(utcMS / 1000)})
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

// wvttTimeSamples returns the wvtt samples that tile [startMS, endMS) with no gaps.
//
// A wvtt sample carries no timing of its own, so a cue is always clipped to
// the chunk that carries it. A cue that continues across a chunk boundary is
// therefore restated, but with a byte-identical payload, and so are
// consecutive chunks with nothing on screen.
func wvttTimeSamples(cues []cueItvl, startMS, endMS int, lang string, groupNr uint32, region int) []mp4.FullSample {
	samples := make([]mp4.FullSample, 0, 2*len(cues)+1)
	currEnd := startMS
	for _, ci := range cues {
		if !ci.overlaps(startMS, endMS) {
			continue
		}
		cueStart := max(ci.startMS, startMS)
		cueEnd := min(ci.endMS, endMS)
		if cueStart > currEnd {
			samples = append(samples, fullSample(currEnd, cueStart, vtteBox))
		}
		samples = append(samples,
			fullSample(cueStart, cueEnd, makeWvttCuePayload(lang, region, ci.utcS*1000, int(groupNr))))
		currEnd = cueEnd
	}
	if currEnd < endMS {
		samples = append(samples, fullSample(currEnd, endMS, vtteBox))
	}
	return samples
}

// stppTimeSample renders the TTML document template tmplName over the cues
// that overlap [startMS, endMS) and returns it as one sample with that
// duration. tmplName is stpptime.xml for a complete document, or
// stpptimebody.xml for the body alone.
//
// Following the paint model, a cue keeps its true begin time even when that
// lies before the start of the sample (ISO/IEC 14496-30 Sec. 5.9(1)), and is
// given an end time only in the sample where it ends. An unchanged cue is
// therefore restated byte for byte, which is what lets a restatement be
// marked redundant or replaced by a no-change box.
func stppTimeSample(tmplName string, cues []cueItvl, startMS, endMS int, lang string, groupNr uint32,
	region int) (mp4.FullSample, error) {
	stppd := StppTimeData{
		Lang:   lang,
		Region: region,
		Cues:   make([]StppTimeCue, 0, len(cues)),
	}
	for _, ci := range cues {
		if !ci.overlaps(startMS, endMS) {
			continue
		}
		cue := StppTimeCue{
			Id:    stppCueID(ci.utcS),
			Begin: msToTTMLTime(ci.startMS),
			Msg:   makeStppMessage(lang, ci.utcS*1000, int(groupNr)),
		}
		if ci.endMS <= endMS {
			cue.End = msToTTMLTime(ci.endMS)
		}
		stppd.Cues = append(stppd.Cues, cue)
	}
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	err := stppTemplate.ExecuteTemplate(buf, tmplName, stppd)
	if err != nil {
		return mp4.FullSample{}, fmt.Errorf("execute %s template: %w", tmplName, err)
	}
	return fullSample(startMS, endMS, buf.Bytes()), nil
}

// chunkSamples returns the samples of the chunk [startMS, endMS): one TTML
// document, or the wvtt cue and empty samples that tile the chunk.
func (st *SubtitleTrack) chunkSamples(cues []cueItvl, startMS, endMS int, groupNr uint32) ([]mp4.FullSample, error) {
	if st.Format.isWebVTT() {
		return wvttTimeSamples(cues, startMS, endMS, st.Language, groupNr, st.Region), nil
	}
	s, err := stppTimeSample("stpptime.xml", cues, startMS, endMS, st.Language, groupNr, st.Region)
	if err != nil {
		return nil, err
	}
	return []mp4.FullSample{s}, nil
}

// paintNoChangeSample turns a restatement into the 8-byte no-change box of its
// format, keeping the sample's time and duration so that the track still
// tiles the timeline.
func paintNoChangeSample(s mp4.FullSample, format SubtitleFormat) mp4.FullSample {
	data := ttmnBox
	if format.isWebVTT() {
		data = vttnBox
	}
	s.Data = data
	s.Size = uint32(len(data))
	s.Flags = paintDependentFlags
	return s
}

// stpcBodySample renders only the body element of the TTML document covering
// [startMS, endMS) and returns it as one sample carrying a ttmb box. The head
// comes from the first chunk of the same group, so the sample is not
// self-contained.
func (st *SubtitleTrack) stpcBodySample(cues []cueItvl, startMS, endMS int, groupNr uint32) (mp4.FullSample, error) {
	s, err := stppTimeSample("stpptimebody.xml", cues, startMS, endMS, st.Language, groupNr, st.Region)
	if err != nil {
		return s, err
	}
	ttmb := mp4.TtmbBox{Body: string(s.Data)}
	sw := bits.NewFixedSliceWriter(int(ttmb.Size()))
	if err := ttmb.EncodeSW(sw); err != nil {
		return s, fmt.Errorf("write ttmb box: %w", err)
	}
	s.Data = sw.Bytes()
	s.Size = uint32(len(s.Data))
	s.Flags = paintDependentFlags
	return s, nil
}

// genFragments generates the CMAF chunks of one group, one fragment per
// object, and returns them with the chunk boundaries in ms.
//
// A chunk whose generated document (or cue box) equals the previous one is a
// restatement. On stpp and wvtt it is sent anyway and marked redundant; on the
// paint-model stpc and wvtc it becomes an 8-byte no-change box, and with Body
// set a changed stpc chunk after the first sends only its body. Comparison is
// always between the generated documents, never between what was sent, so a
// chunk that follows a substituted one is still compared with the content it
// stands for. The first chunk of a group is always complete, since it is what
// a subscriber joining at the group boundary starts from.
func (st *SubtitleTrack) genFragments(groupNr uint64, groupDurMS uint32) ([]*mp4.Fragment, []uint64, error) {
	bounds := st.Cadence.chunkBoundsMS(groupNr, groupDurMS)
	groupStart, groupEnd := int(bounds[0]), int(bounds[len(bounds)-1])
	// Media time is UTC time under mlmpub, so the cue clock is the media clock.
	cues := calcCueItvls(groupStart, groupEnd-groupStart, groupStart, st.CueDurMS)
	frags := make([]*mp4.Fragment, 0, len(bounds)-1)
	var prevData []byte
	for i := 0; i+1 < len(bounds); i++ {
		start, end := int(bounds[i]), int(bounds[i+1])
		frag, err := mp4.CreateFragment(uint32(groupNr), subsTrackID)
		if err != nil {
			return nil, nil, err
		}
		samples, err := st.chunkSamples(cues, start, end, uint32(groupNr))
		if err != nil {
			return nil, nil, err
		}
		for _, s := range samples {
			generated := s.Data
			switch {
			case bytes.Equal(prevData, generated):
				if st.Format.isPaint() {
					s = paintNoChangeSample(s, st.Format)
				} else {
					s.Flags = redundantSampleFlags
				}
			case st.Format == SubtitleFormatSTPC && st.Body && i > 0:
				s, err = st.stpcBodySample(cues, start, end, uint32(groupNr))
				if err != nil {
					return nil, nil, err
				}
			}
			prevData = generated
			frag.AddFullSample(s)
		}
		frags = append(frags, frag)
	}
	return frags, bounds, nil
}

// GenSubtitleGroup generates one MoQ group of a subtitle track: one object per
// chunk of the track's cadence, packaged as "cmaf" (moof+mdat) or "locmaf"
// (a full LOCMAF header for the first object, delta headers after it).
func GenSubtitleGroup(st *SubtitleTrack, groupNr uint64, groupDurMS uint32, packaging string) (*MoQGroup, error) {
	frags, bounds, err := st.genFragments(groupNr, groupDurMS)
	if err != nil {
		return nil, err
	}
	var moov *mp4.MoovBox
	var state *locmaf.State
	switch packaging {
	case "cmaf":
	case "locmaf":
		init, err := st.SpecData.initSegment()
		if err != nil {
			return nil, err
		}
		moov = init.Moov
		state = locmaf.NewState()
	default:
		return nil, fmt.Errorf("unknown subtitle packaging %q", packaging)
	}
	objects := make([]MoQObject, 0, len(frags))
	for i, frag := range frags {
		sw := bits.NewFixedSliceWriter(int(frag.Size()))
		if err := frag.EncodeSW(sw); err != nil {
			return nil, fmt.Errorf("encode subtitle chunk %d of group %d: %w", i, groupNr, err)
		}
		if packaging == "cmaf" {
			objects = append(objects, sw.Bytes())
			continue
		}
		obj, err := locmaf.EncodeCanonical(nil, frag.Moof, frag.Mdat.Data, state, moov)
		if err != nil {
			return nil, fmt.Errorf("encode LOCMAF subtitle chunk %d of group %d: %w", i, groupNr, err)
		}
		objects = append(objects, obj)
	}
	return &MoQGroup{
		id:         uint32(groupNr),
		startTime:  bounds[0],
		endTime:    bounds[len(bounds)-1],
		startNr:    groupNr,
		endNr:      groupNr + 1,
		MoQObjects: objects,
		objEndMS:   bounds[1:],
	}, nil
}

// WriteSubtitleGroup writes the objects of a subtitle group, each when the
// chunk it carries has ended in wall-clock time, as for the video objects.
// If the context is done, the function returns the error from the context.
func WriteSubtitleGroup(ctx context.Context, moq *MoQGroup, cb ObjectWriter) error {
	for nr, moqObj := range moq.MoQObjects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		waitTime := int64(moq.objEndMS[nr]) - time.Now().UnixMilli()
		if waitTime > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(waitTime) * time.Millisecond):
			}
		}
		if _, err := cb(uint64(nr), moqObj); err != nil {
			return err
		}
	}
	return nil
}

// calcSubtitleBitrate returns the wire bitrate of a subtitle track in the
// given packaging, in bits per second, measured on one generated group. The
// cue pattern repeats every second, so any group is representative.
func calcSubtitleBitrate(st *SubtitleTrack, packaging string) (int, error) {
	const refGroupNr = 1_000_000 // An arbitrary group with a 20-character UTC time in its cues.
	mg, err := GenSubtitleGroup(st, refGroupNr, MoqGroupDurMS, packaging)
	if err != nil {
		return 0, fmt.Errorf("measure %s bitrate for %s: %w", packaging, st.Name, err)
	}
	total := 0
	for _, obj := range mg.MoQObjects {
		total += len(obj) + cmafObjectOverheadBytes
	}
	return int(int64(total) * 8 * 1000 / int64(MoqGroupDurMS)), nil
}

// fullSample creates a sync FullSample from start/end times and data
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
