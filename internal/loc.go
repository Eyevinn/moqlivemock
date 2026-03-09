package internal

import (
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	CaptureTimestamp  = 2
	VideoConfig       = 13
	VideoFrameMarking = 4
	AudioLevel        = 6
	MoovHeader        = 20
	MoofHeader        = 22
)

type locFieldID int

var moofFieldIDs = struct {
	//tfhd
	SampleDescriptionIndex locFieldID
	DefaultSampleDuration  locFieldID
	DefaultSampleSize      locFieldID
	DefaultSampleFlags     locFieldID

	//tfdt
	BaseMediaDecodeTime locFieldID

	//trun
	FirstSampleFlags locFieldID
	// SampleCount                  locFieldID
	SampleSizes                  locFieldID
	SampleDurations              locFieldID
	SampleCompositionTimeOffsets locFieldID
	SampleFlags                  locFieldID

	//senc
	PerSampleIVSize      locFieldID
	InitializationVector locFieldID
	SubsampleCount       locFieldID
	Subsamples           locFieldID
}{
	SampleDescriptionIndex: 1,
	DefaultSampleDuration:  3,
	DefaultSampleSize:      5,
	DefaultSampleFlags:     7,
	BaseMediaDecodeTime:    9,
	// SampleCount:                  13,
	FirstSampleFlags:             11,
	SampleSizes:                  2,
	SampleDurations:              4,
	SampleCompositionTimeOffsets: 6,
	SampleFlags:                  8,
	PerSampleIVSize:              13,
	InitializationVector:         10,
	SubsampleCount:               12,
	Subsamples:                   14,
}

var moovFieldIDs = struct {
	//mvhd
	movieTimescale locFieldID
	//tkhd
	matrix locFieldID
	//layer, alternate_group, volume: 0 for video, -1 for audio layer. The client can derive this based on whether it is reconstructing an audio or video track. [DROP]
	//elst
	mediaTime locFieldID
	//mdhd info is stored in catalog already.
	//hdlr info is stored in catalog already
	//vmhd/smhd/sthd only depends on media type
	//dref derivable
	//stbl empty in CMAF
	//stsd
	format locFieldID
	//Video
	width  locFieldID
	height locFieldID
	colr   locFieldID
	pasp   locFieldID
	//Audio
	channelCount locFieldID
	sampleRate   locFieldID //Derivable from mdhd timescale.
	chnl         locFieldID
	//Common box in stsd
	codecConfigurationBox locFieldID

	//frma derivable (both not necessary and in catalog)
	//schm
	schemeType locFieldID
	//tenc
	default_crypt_byte_block locFieldID //TODO: There are default values for this.
	default_skip_byte_block  locFieldID //here too. Might be same variable.
	defaultKID               locFieldID
	DefaultPerSampleIVSize   locFieldID
	defaultConstantIVSize    locFieldID
	defaultConstantIV        locFieldID

	defaultSampleDuration locFieldID
	defaultSampleSize     locFieldID
	defaultSampleFlags    locFieldID
}{
	movieTimescale:           1,
	matrix:                   3,
	mediaTime:                5,
	format:                   7,
	width:                    9,
	height:                   11,
	colr:                     13,
	pasp:                     15,
	channelCount:             17,
	sampleRate:               19,
	chnl:                     21, //?
	codecConfigurationBox:    2,
	schemeType:               23,
	default_crypt_byte_block: 25, // ?
	default_skip_byte_block:  27, // ?
	defaultKID:               29, //Can this be different sizes?
	DefaultPerSampleIVSize:   31,
	defaultConstantIVSize:    33,
	defaultConstantIV:        35,
	defaultSampleDuration:    37,
	defaultSampleSize:        39,
	defaultSampleFlags:       41,
}

func CompressMoof(frag *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	importantFields, err := extractImportantMoofFields(frag, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moof fields: %w", err)
	}
	locHeader := make([]byte, 0)
	for key := range importantFields {
		value := importantFields[key]
		locHeader = binary.AppendVarint(locHeader, int64(key))
		locHeader = append(locHeader, value...)
	}
	locHeader = prependVarintSize(locHeader)
	return locHeader, nil
}

func extractImportantMoofFields(moof *mp4.MoofBox, moov *mp4.MoovBox) (map[locFieldID][]byte, error) {
	importantFields := make(map[locFieldID][]byte)

	if moof == nil || moof.Traf == nil {
		return nil, fmt.Errorf("moof or traf not defined")
	}

	tfhd := moof.Traf.Tfhd
	if tfhd != nil {
		if tfhd.SampleDescriptionIndex != moov.Mvex.Trex.DefaultSampleDescriptionIndex {
			setFieldUint32(importantFields, moofFieldIDs.SampleDescriptionIndex, tfhd.SampleDescriptionIndex)
		}
		if tfhd.DefaultSampleDuration != moov.Mvex.Trex.DefaultSampleDuration {
			setFieldUint32(importantFields, moofFieldIDs.DefaultSampleDuration, tfhd.DefaultSampleDuration)
		}
		if tfhd.DefaultSampleSize != moov.Mvex.Trex.DefaultSampleSize {
			setFieldUint32(importantFields, moofFieldIDs.DefaultSampleSize, tfhd.DefaultSampleSize)
		}
		if tfhd.DefaultSampleFlags != moov.Mvex.Trex.DefaultSampleFlags {
			setFieldUint32(importantFields, moofFieldIDs.DefaultSampleFlags, tfhd.DefaultSampleFlags)
		}
	}

	tfdt := moof.Traf.Tfdt
	if tfdt != nil {
		setFieldUint64(importantFields, moofFieldIDs.BaseMediaDecodeTime, tfdt.BaseMediaDecodeTime())
	}

	trun := moof.Traf.Trun
	if trun != nil {
		// setFieldUint32(importantFields, moofFieldIDs.SampleCount, trun.SampleCount())
		firstSampleFlags, _ := trun.FirstSampleFlags()
		setFieldUint32(importantFields, moofFieldIDs.FirstSampleFlags, firstSampleFlags)

		sizes := make([]byte, 0, len(trun.Samples)*4)
		durations := make([]byte, 0, len(trun.Samples)*4)
		flags := make([]byte, 0, len(trun.Samples)*4)
		compositionOffsets := make([]byte, 0, len(trun.Samples)*4)
		for _, sample := range trun.Samples {
			sizes = appendUint32(sizes, sample.Size)
			durations = appendUint32(durations, sample.Dur)
			flags = appendUint32(flags, sample.Flags)
			compositionOffsets = appendInt32(compositionOffsets, sample.CompositionTimeOffset)
		}
		allDurationsEqual := true
		allFlagsEqual := true
		for i, _ := range trun.Samples {
			if moov.Mvex.Trex.DefaultSampleDuration != binary.BigEndian.Uint32(durations[i*4:i*4+4]) {
				allDurationsEqual = false
			}
			if moov.Mvex.Trex.DefaultSampleFlags != binary.BigEndian.Uint32(flags[i*4:i*4+4]) {
				allFlagsEqual = false
			}
		}
		if !allDurationsEqual {
			importantFields[moofFieldIDs.SampleDurations] = prependVarintSize(durations)
		}
		if !allFlagsEqual {
			importantFields[moofFieldIDs.SampleFlags] = prependVarintSize(flags)
		}
		importantFields[moofFieldIDs.SampleSizes] = prependVarintSize(sizes)
		importantFields[moofFieldIDs.SampleCompositionTimeOffsets] = prependVarintSize(compositionOffsets)
	}

	senc, perSampleIVSize, err := getParsedSencBox(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to parse senc: %w", err)
	}
	if senc != nil {
		if perSampleIVSize != getDefaultPerSampleIVSize(moov, moof.Traf.Tfhd.TrackID) {
			setFieldUint8(importantFields, moofFieldIDs.PerSampleIVSize, perSampleIVSize)
		}
		if perSampleIVSize > 0 && len(senc.IVs) > 0 {
			allIVs := make([]byte, 0)
			for _, iv := range senc.IVs {
				allIVs = append(allIVs, iv...)
			}
			importantFields[moofFieldIDs.InitializationVector] = prependVarintSize(allIVs)
		}
		if len(senc.SubSamples) > 0 {
			totalSubsamples := 0
			for _, sampleSubSamples := range senc.SubSamples {
				totalSubsamples += len(sampleSubSamples)
			}

			subSampleCounts := make([]byte, 0, len(senc.SubSamples)*4)
			allSubsamples := make([]byte, 0, totalSubsamples*6)
			for _, sampleSubSamples := range senc.SubSamples {
				subSampleCounts = appendUint32(subSampleCounts, uint32(len(sampleSubSamples)))
				for _, subSample := range sampleSubSamples {
					allSubsamples = appendUint16(allSubsamples, subSample.BytesOfClearData)
					allSubsamples = appendUint32(allSubsamples, subSample.BytesOfProtectedData)
				}
			}
			importantFields[moofFieldIDs.SubsampleCount] = prependVarintSize(subSampleCounts)
			importantFields[moofFieldIDs.Subsamples] = prependVarintSize(allSubsamples)
		}
	}
	return importantFields, nil
}

func DecompressMoof(data []byte, seqnum uint32, moov *mp4.MoovBox) (*mp4.MoofBox, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty compressed moof data")
	}
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("moov or trex not defined")
	}
	fieldValues, err := separateMoofFields(data)
	if err != nil {
		return nil, err
	}
	frag, err := mp4.CreateFragment(seqnum, 1)
	if err != nil {
		return nil, fmt.Errorf("unable to create fragment: %w", err)
	}
	traf := frag.Moof.Traf
	trex := moov.Mvex.Trex

	traf.Tfhd.SampleDescriptionIndex = trex.DefaultSampleDescriptionIndex
	traf.Tfhd.DefaultSampleDuration = trex.DefaultSampleDuration
	traf.Tfhd.DefaultSampleSize = trex.DefaultSampleSize
	traf.Tfhd.DefaultSampleFlags = trex.DefaultSampleFlags
	perSampleIVSize := getDefaultPerSampleIVSize(moov, traf.Tfhd.TrackID)

	sampleDescriptionIndex, ok, err := readU32(moofFieldIDs.SampleDescriptionIndex, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		traf.Tfhd.SampleDescriptionIndex = sampleDescriptionIndex
		traf.Tfhd.Flags |= mp4.TfhdSampleDescriptionIndexPresentFlag
	}

	defaultSampleDuration, ok, err := readU32(moofFieldIDs.DefaultSampleDuration, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		traf.Tfhd.DefaultSampleDuration = defaultSampleDuration
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleDurationPresentFlag
	}

	defaultSampleSize, ok, err := readU32(moofFieldIDs.DefaultSampleSize, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		traf.Tfhd.DefaultSampleSize = defaultSampleSize
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleSizePresentFlag
	}

	defaultSampleFlags, ok, err := readU32(moofFieldIDs.DefaultSampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		traf.Tfhd.DefaultSampleFlags = defaultSampleFlags
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	}

	ivSizeValue, ok, err := readU8(moofFieldIDs.PerSampleIVSize, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		perSampleIVSize = ivSizeValue
	}

	baseMediaDecodeTime, ok, err := readU64(moofFieldIDs.BaseMediaDecodeTime, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.BaseMediaDecodeTime)
	}
	traf.Tfdt.SetBaseMediaDecodeTime(baseMediaDecodeTime)

	firstSampleFlags, ok, err := readU32(moofFieldIDs.FirstSampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		traf.Trun.SetFirstSampleFlags(firstSampleFlags)
	}

	sampleSizes, ok, err := readU32List(moofFieldIDs.SampleSizes, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.SampleSizes)
	}

	sampleDurations, ok, err := readU32List(moofFieldIDs.SampleDurations, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleDurations = repeatU32(traf.Tfhd.DefaultSampleDuration, len(sampleSizes))
	}

	sampleFlags, ok, err := readU32List(moofFieldIDs.SampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleFlags = repeatU32(traf.Tfhd.DefaultSampleFlags, len(sampleSizes))
	}

	sampleCompositionTimeOffsets, ok, err := readInt32List(moofFieldIDs.SampleCompositionTimeOffsets, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.SampleCompositionTimeOffsets)
	}
	if len(sampleDurations) != len(sampleSizes) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleDurations)
	}
	if len(sampleFlags) != len(sampleSizes) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleFlags)
	}
	if len(sampleCompositionTimeOffsets) != len(sampleSizes) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleCompositionTimeOffsets)
	}
	if initializationVectors, ok := fieldValues[moofFieldIDs.InitializationVector]; ok {
		if perSampleIVSize == 0 {
			return nil, fmt.Errorf("field id=%d present but per-sample IV size is 0", moofFieldIDs.InitializationVector)
		}
		if len(initializationVectors)%int(perSampleIVSize) != 0 {
			return nil, fmt.Errorf("field id=%d length mismatch for IV size %d", moofFieldIDs.InitializationVector, perSampleIVSize)
		}
	}
	for i := range sampleSizes {
		traf.Trun.AddSample(mp4.NewSample(sampleFlags[i], sampleDurations[i], sampleSizes[i], sampleCompositionTimeOffsets[i]))
	}
	return frag.Moof, nil
}

func separateMoofFields(data []byte) (map[locFieldID][]byte, error) {
	fieldLengths := map[locFieldID]int{
		moofFieldIDs.SampleDescriptionIndex: 4,
		moofFieldIDs.DefaultSampleDuration:  4,
		moofFieldIDs.DefaultSampleSize:      4,
		moofFieldIDs.DefaultSampleFlags:     4,
		moofFieldIDs.BaseMediaDecodeTime:    8,
		// moofFieldIDs.SampleCount:                  4,
		moofFieldIDs.FirstSampleFlags:             4,
		moofFieldIDs.SampleSizes:                  -1,
		moofFieldIDs.SampleDurations:              -1,
		moofFieldIDs.SampleFlags:                  -1,
		moofFieldIDs.SampleCompositionTimeOffsets: -1,
		moofFieldIDs.PerSampleIVSize:              1,
		moofFieldIDs.InitializationVector:         -1,
		moofFieldIDs.SubsampleCount:               -1,
		moofFieldIDs.Subsamples:                   -1,
	}

	fieldValues := make(map[locFieldID][]byte)
	headerLength, pos := binary.Varint(data)
	if pos <= 0 {
		return nil, fmt.Errorf("invalid header length")
	}
	headerEnd := pos + int(headerLength)
	if headerLength < 0 || headerEnd > len(data) {
		return nil, fmt.Errorf("header length %d exceeds data length %d", headerLength, len(data))
	}

	for pos < headerEnd {
		idValue, deltaPos := binary.Varint(data[pos:])
		if deltaPos <= 0 {
			return nil, fmt.Errorf("invalid field id at offset %d", pos)
		}
		pos += deltaPos
		id := locFieldID(idValue)
		fieldLength, ok := fieldLengths[id]
		if !ok {
			return nil, fmt.Errorf("unknown field id=%d", id)
		}

		switch fieldLength {
		case 8, 4, 1:
			if pos+fieldLength > headerEnd {
				return nil, fmt.Errorf("field id=%d exceeds header length", id)
			}
			fieldValues[id] = append([]byte(nil), data[pos:pos+fieldLength]...)
			pos += fieldLength
		case -1:
			valueLength, deltaPos := binary.Varint(data[pos:])
			if deltaPos <= 0 {
				return nil, fmt.Errorf("invalid field length for id=%d", id)
			}
			pos += deltaPos
			if valueLength < 0 || pos+int(valueLength) > headerEnd {
				return nil, fmt.Errorf("field id=%d exceeds header length", id)
			}
			fieldValues[id] = append([]byte(nil), data[pos:pos+int(valueLength)]...)
			pos += int(valueLength)
		default:
			return nil, fmt.Errorf("unsupported field length %d for id=%d", fieldLength, id)
		}
	}
	return fieldValues, nil
}

func extractImportantMoovFields(moov *mp4.MoovBox) (map[locFieldID][]byte, error) {
	importantFields := make(map[locFieldID][]byte)
	if moov == nil {
		return nil, fmt.Errorf("moov not defined")
	}
	return importantFields, nil
}

func setFieldUint8(fields map[locFieldID][]byte, key locFieldID, value uint8) {
	fields[key] = []byte{value}
}

func setFieldUint32(fields map[locFieldID][]byte, key locFieldID, value uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, value)
	fields[key] = b
}

func setFieldUint64(fields map[locFieldID][]byte, key locFieldID, value uint64) {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, value)
	fields[key] = b
}

func appendUint32(dst []byte, value uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], value)
	return append(dst, b[:]...)
}

func appendUint16(dst []byte, value uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], value)
	return append(dst, b[:]...)
}

func appendInt32(dst []byte, value int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(value))
	return append(dst, b[:]...)
}

func prependVarintSize(payload []byte) []byte {
	withSize := make([]byte, 0, binary.MaxVarintLen64+len(payload))
	withSize = binary.AppendVarint(withSize, int64(len(payload)))
	withSize = append(withSize, payload...)
	return withSize
}

func getDefaultPerSampleIVSize(moov *mp4.MoovBox, trackID uint32) byte {
	if moov == nil {
		return 0
	}
	sinf := moov.GetSinf(trackID)
	if sinf == nil || sinf.Schi == nil || sinf.Schi.Tenc == nil {
		return 0
	}
	return sinf.Schi.Tenc.DefaultPerSampleIVSize
}

func getParsedSencBox(moof *mp4.MoofBox, moov *mp4.MoovBox) (*mp4.SencBox, uint8, error) {
	if moof == nil || moof.Traf == nil {
		return nil, 0, fmt.Errorf("moof or traf not defined")
	}
	traf := moof.Traf
	ok, parsed := traf.ContainsSencBox()
	if !ok {
		return nil, 0, nil
	}

	defaultIVSize := getDefaultPerSampleIVSize(moov, traf.Tfhd.TrackID)

	if !parsed {
		if err := traf.ParseReadSenc(defaultIVSize, moof.StartPos); err != nil {
			return nil, 0, err
		}
	}

	senc := traf.Senc
	if senc == nil && traf.UUIDSenc != nil {
		senc = traf.UUIDSenc.Senc
	}
	if senc == nil {
		return nil, 0, nil
	}
	return senc, senc.PerSampleIVSize(), nil
}

func readU8(id locFieldID, fieldValues map[locFieldID][]byte) (uint8, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false, nil
	}
	if len(value) != 1 {
		return 0, true, fmt.Errorf("invalid field id=%d", id)
	}
	return value[0], true, nil
}

func repeatU32(value uint32, count int) []uint32 {
	values := make([]uint32, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func readU32(id locFieldID, fieldValues map[locFieldID][]byte) (uint32, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false, nil
	}
	if len(value) != 4 {
		return 0, true, fmt.Errorf("invalid field id=%d", id)
	}
	return binary.BigEndian.Uint32(value), true, nil
}

func readU64(id locFieldID, fieldValues map[locFieldID][]byte) (uint64, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false, nil
	}
	if len(value) != 8 {
		return 0, true, fmt.Errorf("invalid field id=%d", id)
	}
	return binary.BigEndian.Uint64(value), true, nil
}

func readU32List(id locFieldID, fieldValues map[locFieldID][]byte) ([]uint32, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	if len(value)%4 != 0 {
		return nil, true, fmt.Errorf("invalid field id=%d", id)
	}
	uint32list := make([]uint32, 0, len(value)/4)
	for i := 0; i < len(value)/4; i++ {
		uint32list = append(uint32list, binary.BigEndian.Uint32(value[i*4:i*4+4]))
	}
	return uint32list, true, nil
}

func readInt32List(id locFieldID, fieldValues map[locFieldID][]byte) ([]int32, bool, error) {
	u32List, ok, err := readU32List(id, fieldValues)
	if err != nil {
		return nil, ok, err
	}
	if !ok {
		return nil, false, nil
	}
	int32List := make([]int32, len(u32List))
	for i := range u32List {
		int32List[i] = int32(u32List[i])
	}
	return int32List, true, nil
}
