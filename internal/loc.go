package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	Timestamp         = 6
	Timescale         = 8
	VideoConfig       = 13
	VideoFrameMarking = 4
	AudioLevel        = 10
	MoovHeader        = 21
	MoofHeader        = 23
	MoofDeltaHeader   = 25
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
	BytesOfClearData     locFieldID
	BytesOfProtectedData locFieldID
}{
	SampleDescriptionIndex:       2,
	DefaultSampleDuration:        4,
	DefaultSampleSize:            6,
	DefaultSampleFlags:           8,
	BaseMediaDecodeTime:          10,
	FirstSampleFlags:             12,
	SampleSizes:                  1,
	SampleDurations:              3,
	SampleCompositionTimeOffsets: 5,
	SampleFlags:                  7,
	PerSampleIVSize:              14,
	InitializationVector:         9,
	SubsampleCount:               11,
	BytesOfClearData:             13,
	BytesOfProtectedData:         15,
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
	colr   locFieldID //Might need to be separated
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
	movieTimescale:           2,
	matrix:                   4,
	mediaTime:                6,
	format:                   8,
	width:                    10,
	height:                   12,
	colr:                     3,
	pasp:                     5,
	channelCount:             14,
	sampleRate:               16,
	chnl:                     7,
	codecConfigurationBox:    1,
	schemeType:               18,
	default_crypt_byte_block: 20,
	default_skip_byte_block:  22,
	defaultKID:               11,
	DefaultPerSampleIVSize:   24,
	defaultConstantIVSize:    26,
	defaultConstantIV:        9,
	defaultSampleDuration:    28,
	defaultSampleSize:        30,
	defaultSampleFlags:       32,
}

func createVideoFramemarkingProperty(sample mp4.FullSample) []byte {
	locHeader := binary.AppendVarint(nil, VideoFrameMarking)
	videoFlags := 0b11000000       //One sample is sent per LOC so it is both the start and end of a frame. No temporal layers are used.
	if sample.Flags>>24&0x3 == 2 { //Is it an IDR-frame?
		videoFlags |= 0b1 << 5
	}
	if sample.Flags>>22&0x3 == 2 { //Is it a discardable frame?
		videoFlags |= 0b1 << 4
	}
	locHeader = binary.AppendVarint(locHeader, int64(videoFlags))
	return locHeader
}

func createMoofLOCProperty(moof *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	locPayload, err := CompressMoof(moof, moov)
	if err != nil {
		return nil, err
	}
	locHeader := prependVarintSize(locPayload)
	locHeader = append(binary.AppendVarint(nil, MoofHeader), locHeader...)
	return locHeader, nil
}

func createMoovLOCProperty(moov *mp4.MoovBox) ([]byte, error) {
	locPayload, err := CompressMoov(moov)
	if err != nil {
		return nil, err
	}
	locHeader := prependVarintSize(locPayload)
	locHeader = append(binary.AppendVarint(nil, MoovHeader), locHeader...)
	return locHeader, nil
}

func convertLOCtoCMAF(loc []byte, seqnum uint32, moov *mp4.MoovBox) (*mp4.Fragment, error) {
	var frag *mp4.Fragment
	pos := 0
	for pos < len(loc) {
		id, deltaPos := binary.Varint(loc[pos:])
		pos += deltaPos
		if id%2 == 1 {
			length, deltaPos := binary.Varint(loc[pos:])
			pos += deltaPos
			switch id {
			case MoofHeader:
				moof, err := DecompressMoof(loc[pos:int64(pos)+length], seqnum, moov)
				if err != nil {
					return nil, fmt.Errorf("unable to decompress moof: %w", err)
				}
				frag.Moof = moof
			}
		}
	}

	return frag, nil
}

func CompressMoof(moof *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	importantFields, err := extractImportantMoofFields(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moof fields: %w", err)
	}
	locPayload := make([]byte, 0)
	for key := range importantFields {
		value := importantFields[key]
		locPayload = binary.AppendVarint(locPayload, int64(key))
		locPayload = append(locPayload, value...)
	}
	return locPayload, nil
}

func CompressMoov(moov *mp4.MoovBox) ([]byte, error) {
	importantFields, err := extractImportantMoovFields(moov)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moov fields: %w", err)
	}
	locHeader := make([]byte, 0)
	for key := range importantFields {
		value := importantFields[key]
		locHeader = binary.AppendVarint(locHeader, int64(key))
		locHeader = append(locHeader, value...)
	}
	return locHeader, nil
}

func extractImportantMoofFields(moof *mp4.MoofBox, moov *mp4.MoovBox) (map[locFieldID][]byte, error) {
	importantFields := make(map[locFieldID][]byte)

	if moof == nil || moof.Traf == nil {
		return nil, fmt.Errorf("moof or traf not defined")
	}
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("moov or trex not defined")
	}

	tfhd := moof.Traf.Tfhd
	if tfhd != nil {
		if tfhd.SampleDescriptionIndex != moov.Mvex.Trex.DefaultSampleDescriptionIndex {
			importantFields[moofFieldIDs.SampleDescriptionIndex] = binary.AppendVarint(nil, int64(tfhd.SampleDescriptionIndex))
		}
		if tfhd.DefaultSampleDuration != moov.Mvex.Trex.DefaultSampleDuration {
			importantFields[moofFieldIDs.DefaultSampleDuration] = binary.AppendVarint(nil, int64(tfhd.DefaultSampleDuration))
		}
		if tfhd.DefaultSampleSize != moov.Mvex.Trex.DefaultSampleSize {
			importantFields[moofFieldIDs.DefaultSampleSize] = binary.AppendVarint(nil, int64(tfhd.DefaultSampleSize))
		}
		if tfhd.DefaultSampleFlags != moov.Mvex.Trex.DefaultSampleFlags {
			importantFields[moofFieldIDs.DefaultSampleFlags] = binary.AppendVarint(nil, int64(tfhd.DefaultSampleFlags))
		}
	}

	tfdt := moof.Traf.Tfdt
	if tfdt != nil {
		importantFields[moofFieldIDs.BaseMediaDecodeTime] = binary.AppendVarint(nil, int64(tfdt.BaseMediaDecodeTime()))
	}

	trun := moof.Traf.Trun
	if trun != nil {
		firstSampleFlags, _ := trun.FirstSampleFlags()
		importantFields[moofFieldIDs.FirstSampleFlags] = binary.AppendVarint(nil, int64(firstSampleFlags))

		var sizes []byte
		var durations []byte
		var flags []byte
		var compositionTimeOffsets []byte

		for _, sample := range trun.Samples {
			if sample.Size != 0 {
				sizes = binary.AppendVarint(sizes, int64(sample.Size))
			}
			if sample.Dur != 0 {
				durations = binary.AppendVarint(durations, int64(sample.Dur))
			}
			if sample.Flags != 0 {
				flags = binary.AppendVarint(flags, int64(sample.Flags))
			}
			compositionTimeOffsets = binary.AppendVarint(compositionTimeOffsets, int64(sample.CompositionTimeOffset))
		}
		if len(durations) > 0 {
			importantFields[moofFieldIDs.SampleDurations] = prependVarintSize(durations)
		}
		if len(flags) > 0 {
			importantFields[moofFieldIDs.SampleFlags] = prependVarintSize(flags)
		}
		if len(sizes) > 0 {
			importantFields[moofFieldIDs.SampleSizes] = prependVarintSize(sizes)
		}
		importantFields[moofFieldIDs.SampleCompositionTimeOffsets] = prependVarintSize(compositionTimeOffsets)
	}

	senc, perSampleIVSize, err := getParsedSencBox(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to parse senc: %w", err)
	}
	if senc != nil {
		if perSampleIVSize != getDefaultPerSampleIVSize(moov, moof.Traf.Tfhd.TrackID) {
			importantFields[moofFieldIDs.PerSampleIVSize] = binary.AppendVarint(nil, int64(perSampleIVSize))
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

			var subSampleCounts []byte
			var bytesOfClearData []byte
			var bytesOfProtectedData []byte
			for _, sampleSubSamples := range senc.SubSamples {
				subSampleCounts = binary.AppendVarint(subSampleCounts, int64(len(sampleSubSamples)))
				for _, subSample := range sampleSubSamples {
					bytesOfClearData = binary.AppendVarint(bytesOfClearData, int64(subSample.BytesOfClearData))
					bytesOfProtectedData = binary.AppendVarint(bytesOfProtectedData, int64(subSample.BytesOfProtectedData))
				}
			}
			importantFields[moofFieldIDs.SubsampleCount] = prependVarintSize(subSampleCounts)
			importantFields[moofFieldIDs.BytesOfClearData] = prependVarintSize(bytesOfClearData)
			importantFields[moofFieldIDs.BytesOfProtectedData] = prependVarintSize(bytesOfProtectedData)
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
	fieldValues, err := separateFields(data)
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

	sampleDescriptionIndex, ok := readVarint(moofFieldIDs.SampleDescriptionIndex, fieldValues)
	if ok {
		traf.Tfhd.SampleDescriptionIndex = uint32(sampleDescriptionIndex)
		traf.Tfhd.Flags |= mp4.TfhdSampleDescriptionIndexPresentFlag
	}

	defaultSampleDuration, ok := readVarint(moofFieldIDs.DefaultSampleDuration, fieldValues)
	if ok {
		traf.Tfhd.DefaultSampleDuration = uint32(defaultSampleDuration)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleDurationPresentFlag
	}

	defaultSampleSize, ok := readVarint(moofFieldIDs.DefaultSampleSize, fieldValues)
	if ok {
		traf.Tfhd.DefaultSampleSize = uint32(defaultSampleSize)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleSizePresentFlag
	}

	defaultSampleFlags, ok := readVarint(moofFieldIDs.DefaultSampleFlags, fieldValues)
	if ok {
		traf.Tfhd.DefaultSampleFlags = uint32(defaultSampleFlags)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	}

	ivSizeValue, ok := readVarint(moofFieldIDs.PerSampleIVSize, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		perSampleIVSize = uint8(ivSizeValue)
	}

	baseMediaDecodeTime, ok := readVarint(moofFieldIDs.BaseMediaDecodeTime, fieldValues)
	if !ok {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.BaseMediaDecodeTime)
	}
	traf.Tfdt.SetBaseMediaDecodeTime(uint64(baseMediaDecodeTime))

	firstSampleFlags, ok := readVarint(moofFieldIDs.FirstSampleFlags, fieldValues)
	if ok {
		traf.Trun.SetFirstSampleFlags(uint32(firstSampleFlags))
	}
	sampleCompositionTimeOffsets, ok, err := readVarintList(moofFieldIDs.SampleCompositionTimeOffsets, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.SampleCompositionTimeOffsets)
	}

	sampleSizes, ok, err := readVarintList(moofFieldIDs.SampleSizes, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleSizes = repeatInt64(int64(traf.Tfhd.DefaultSampleSize), len(sampleCompositionTimeOffsets))
	}

	sampleDurations, ok, err := readVarintList(moofFieldIDs.SampleDurations, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleDurations = repeatInt64(int64(traf.Tfhd.DefaultSampleDuration), len(sampleCompositionTimeOffsets))
	}

	sampleFlags, ok, err := readVarintList(moofFieldIDs.SampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleFlags = repeatInt64(int64(traf.Tfhd.DefaultSampleFlags), len(sampleCompositionTimeOffsets))
	}

	if len(sampleDurations) != len(sampleCompositionTimeOffsets) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleDurations)
	}
	if len(sampleFlags) != len(sampleCompositionTimeOffsets) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleFlags)
	}
	if len(sampleCompositionTimeOffsets) != len(sampleSizes) {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SampleCompositionTimeOffsets)
	}

	for i := range sampleCompositionTimeOffsets {
		traf.Trun.AddSample(mp4.NewSample(uint32(sampleFlags[i]), uint32(sampleDurations[i]), uint32(sampleSizes[i]), int32(sampleCompositionTimeOffsets[i])))
	}

	senc, err := reconstructSencFromFields(fieldValues, len(sampleCompositionTimeOffsets), perSampleIVSize)
	if err != nil {
		return nil, err
	}
	if senc != nil {
		if err := traf.AddChild(senc); err != nil {
			return nil, fmt.Errorf("unable to attach senc box: %w", err)
		}
	}

	return frag.Moof, nil
}

func DecompressInit(data []byte, track Track) (*mp4.InitSegment, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty compressed init data")
	}

	fieldValues, err := separateFields(data)
	if err != nil {
		return nil, err
	}

	init := mp4.CreateEmptyInit()
	mp4.NewMP4Init()
	moov := init.Moov

	if moov.Mvhd == nil {
		return nil, fmt.Errorf("mvhd not defined")
	}

	movieTimescale, ok := readVarint(moovFieldIDs.movieTimescale, fieldValues)
	if ok {
		moov.Mvhd.Timescale = uint32(movieTimescale)
	}

	trak, sampleEntry, err := ensurePrimarySampleEntry(init, fieldValues)
	if err != nil {
		return nil, err
	}
	if track.Timescale == nil {
		return nil, fmt.Errorf("No timescale found in MSF track")
	}
	trak.Mdia.Mdhd.Timescale = uint32(*track.Timescale)
	if track.Width == nil {
		return nil, fmt.Errorf("No width found in MSF track")
	}
	trak.Tkhd.Width = mp4.Fixed32(*track.Width)
	if track.Height == nil {
		return nil, fmt.Errorf("No timescale found in MSF track")
	}
	trak.Tkhd.Height = mp4.Fixed32(*track.Height)

	mediaTime, ok := readVarint(moovFieldIDs.mediaTime, fieldValues)
	if ok {
		elstEntry := ensureTrackElstEntry(trak)
		elstEntry.MediaTime = mediaTime
	}

	formatCode, ok := readVarint(moovFieldIDs.format, fieldValues)
	if ok {
		format := uint32ToFourCC(uint32(formatCode))
		switch entry := sampleEntry.(type) {
		case *mp4.VisualSampleEntryBox:
			entry.SetType(format)
		case *mp4.AudioSampleEntryBox:
			entry.SetType(format)
		default:
			return nil, fmt.Errorf("unsupported sample entry type %T", sampleEntry)
		}
	}

	width, ok := readVarint(moovFieldIDs.width, fieldValues)
	if ok {
		visualEntry, ok := sampleEntry.(*mp4.VisualSampleEntryBox)
		if !ok {
			return nil, fmt.Errorf("field id=%d requires visual sample entry", moovFieldIDs.width)
		}
		visualEntry.Width = uint16(width)
	}

	height, ok := readVarint(moovFieldIDs.height, fieldValues)
	if ok {
		visualEntry, ok := sampleEntry.(*mp4.VisualSampleEntryBox)
		if !ok {
			return nil, fmt.Errorf("field id=%d requires visual sample entry", moovFieldIDs.height)
		}
		visualEntry.Height = uint16(height)
	}

	channelCount, ok := readVarint(moovFieldIDs.channelCount, fieldValues)
	if ok {
		audioEntry, ok := sampleEntry.(*mp4.AudioSampleEntryBox)
		if !ok {
			return nil, fmt.Errorf("field id=%d requires audio sample entry", moovFieldIDs.channelCount)
		}
		audioEntry.ChannelCount = uint16(channelCount)
	}

	sampleRate, ok := readVarint(moovFieldIDs.sampleRate, fieldValues)
	if ok {
		audioEntry, ok := sampleEntry.(*mp4.AudioSampleEntryBox)
		if !ok {
			return nil, fmt.Errorf("field id=%d requires audio sample entry", moovFieldIDs.sampleRate)
		}
		audioEntry.SampleRate = uint16(sampleRate)
	}

	colrBox, ok, err := readBoxField(moovFieldIDs.colr, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := setSampleEntryChildBox(sampleEntry, colrBox); err != nil {
			return nil, err
		}
	}

	paspBox, ok, err := readBoxField(moovFieldIDs.pasp, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := setSampleEntryChildBox(sampleEntry, paspBox); err != nil {
			return nil, err
		}
	}

	chnlBox, ok, err := readBoxField(moovFieldIDs.chnl, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := setSampleEntryChildBox(sampleEntry, chnlBox); err != nil {
			return nil, err
		}
	}

	codecConfigurationBox, ok, err := readBoxField(moovFieldIDs.codecConfigurationBox, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := setSampleEntryChildBox(sampleEntry, codecConfigurationBox); err != nil {
			return nil, err
		}
	}

	schemeTypeCode, ok := readVarint(moovFieldIDs.schemeType, fieldValues)
	if ok {
		sinf := getSampleEntrySinf(sampleEntry)
		if sinf == nil || sinf.Schm == nil {
			return nil, fmt.Errorf("field id=%d present but schm is missing", moovFieldIDs.schemeType)
		}
		sinf.Schm.SchemeType = uint32ToFourCC(uint32(schemeTypeCode))
	}

	tenc := getSampleEntryTenc(sampleEntry)

	defaultCryptByteBlock, ok := readVarint(moovFieldIDs.default_crypt_byte_block, fieldValues)
	if ok {
		if tenc == nil {
			return nil, fmt.Errorf("field id=%d present but tenc is missing", moovFieldIDs.default_crypt_byte_block)
		}
		tenc.DefaultCryptByteBlock = uint8(defaultCryptByteBlock)
	}

	defaultSkipByteBlock, ok := readVarint(moovFieldIDs.default_skip_byte_block, fieldValues)
	if ok {
		if tenc == nil {
			return nil, fmt.Errorf("field id=%d present but tenc is missing", moovFieldIDs.default_skip_byte_block)
		}
		tenc.DefaultSkipByteBlock = uint8(defaultSkipByteBlock)
	}

	if defaultKID, ok := fieldValues[moovFieldIDs.defaultKID]; ok {
		if tenc == nil {
			return nil, fmt.Errorf("field id=%d present but tenc is missing", moovFieldIDs.defaultKID)
		}
		if len(defaultKID) != 16 {
			return nil, fmt.Errorf("invalid field id=%d", moovFieldIDs.defaultKID)
		}
		tenc.DefaultKID = append(mp4.UUID(nil), defaultKID...)
	}

	defaultPerSampleIVSize, ok := readVarint(moovFieldIDs.DefaultPerSampleIVSize, fieldValues)
	if ok {
		if tenc == nil {
			return nil, fmt.Errorf("field id=%d present but tenc is missing", moovFieldIDs.DefaultPerSampleIVSize)
		}
		tenc.DefaultPerSampleIVSize = uint8(defaultPerSampleIVSize)
	}

	defaultConstantIVSize, hasConstantIVSize := readVarint(moovFieldIDs.defaultConstantIVSize, fieldValues)
	defaultConstantIV, hasConstantIV := fieldValues[moovFieldIDs.defaultConstantIV]
	if hasConstantIV {
		if tenc == nil {
			return nil, fmt.Errorf("field id=%d present but tenc is missing", moovFieldIDs.defaultConstantIV)
		}
		if hasConstantIVSize && int(defaultConstantIVSize) != len(defaultConstantIV) {
			return nil, fmt.Errorf("field id=%d length mismatch", moovFieldIDs.defaultConstantIV)
		}
		tenc.DefaultConstantIV = append([]byte(nil), defaultConstantIV...)
	}

	defaultSampleDuration, ok := readVarint(moovFieldIDs.defaultSampleDuration, fieldValues)
	if ok {
		if moov.Mvex == nil || moov.Mvex.Trex == nil {
			return nil, fmt.Errorf("field id=%d present but trex is missing", moovFieldIDs.defaultSampleDuration)
		}
		moov.Mvex.Trex.DefaultSampleDuration = uint32(defaultSampleDuration)
	}

	defaultSampleSize, ok := readVarint(moovFieldIDs.defaultSampleSize, fieldValues)
	if ok {
		if moov.Mvex == nil || moov.Mvex.Trex == nil {
			return nil, fmt.Errorf("field id=%d present but trex is missing", moovFieldIDs.defaultSampleSize)
		}
		moov.Mvex.Trex.DefaultSampleSize = uint32(defaultSampleSize)
	}

	defaultSampleFlags, ok := readVarint(moovFieldIDs.defaultSampleFlags, fieldValues)
	if ok {
		if moov.Mvex == nil || moov.Mvex.Trex == nil {
			return nil, fmt.Errorf("field id=%d present but trex is missing", moovFieldIDs.defaultSampleFlags)
		}
		moov.Mvex.Trex.DefaultSampleFlags = uint32(defaultSampleFlags)
	}

	return init, nil
}

func separateFields(data []byte) (map[locFieldID][]byte, error) {
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
		if id%2 == 0 { //no length field
			_, deltaPos := binary.Varint(data[pos:])
			if deltaPos <= 0 {
				return nil, fmt.Errorf("invalid varint field value for id=%d", id)
			}
			if pos+deltaPos > headerEnd {
				return nil, fmt.Errorf("field id=%d exceeds header length", id)
			}
			fieldValues[id] = append([]byte(nil), data[pos:pos+deltaPos]...)
			pos += deltaPos
		} else { //has length field
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
		}
	}
	return fieldValues, nil
}

func extractImportantMoovFields(moov *mp4.MoovBox) (map[locFieldID][]byte, error) {
	importantFields := make(map[locFieldID][]byte)
	if moov == nil {
		return nil, fmt.Errorf("moov not defined")
	}
	if moov.Mvhd == nil {
		return nil, fmt.Errorf("mvhd not defined")
	}
	importantFields[moovFieldIDs.movieTimescale] = binary.AppendVarint(nil, int64(moov.Mvhd.Timescale))

	track := moov.Trak
	if track == nil && len(moov.Traks) > 0 {
		track = moov.Traks[0]
	}
	if track == nil || track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
		return nil, fmt.Errorf("track sample description not defined")
	}

	if track.Edts != nil && len(track.Edts.Elst) > 0 && len(track.Edts.Elst[0].Entries) > 0 {
		importantFields[moovFieldIDs.mediaTime] = binary.AppendVarint(nil, track.Edts.Elst[0].Entries[0].MediaTime)
	}

	sampleEntry := track.Mdia.Minf.Stbl.Stsd.Children[0]
	format := sampleEntry.Type()
	if len(format) != 4 {
		return nil, fmt.Errorf("unable to set field id=%d: expected 4-byte code, got %q", moovFieldIDs.format, format)
	}
	formatCode := uint32(format[0])<<24 | uint32(format[1])<<16 | uint32(format[2])<<8 | uint32(format[3])
	importantFields[moovFieldIDs.format] = binary.AppendVarint(nil, int64(formatCode))

	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		importantFields[moovFieldIDs.width] = binary.AppendVarint(nil, int64(entry.Width))
		importantFields[moovFieldIDs.height] = binary.AppendVarint(nil, int64(entry.Height))

		if colr := findChildBoxByType(entry.Children, "colr"); colr != nil {
			if err := setFieldBox(importantFields, moovFieldIDs.colr, colr); err != nil {
				return nil, fmt.Errorf("unable to set field id=%d: %w", moovFieldIDs.colr, err)
			}
		}
		if pasp := findChildBoxByType(entry.Children, "pasp"); pasp != nil {
			if err := setFieldBox(importantFields, moovFieldIDs.pasp, pasp); err != nil {
				return nil, fmt.Errorf("unable to set field id=%d: %w", moovFieldIDs.pasp, err)
			}
		}
	case *mp4.AudioSampleEntryBox:
		importantFields[moovFieldIDs.channelCount] = binary.AppendVarint(nil, int64(entry.ChannelCount))
		importantFields[moovFieldIDs.sampleRate] = binary.AppendVarint(nil, int64(entry.SampleRate))

		if chnl := findChildBoxByType(entry.Children, "chnl"); chnl != nil {
			if err := setFieldBox(importantFields, moovFieldIDs.chnl, chnl); err != nil {
				return nil, fmt.Errorf("unable to set field id=%d: %w", moovFieldIDs.chnl, err)
			}
		}
	}

	codecConfigurationBox := getCodecConfigurationBox(sampleEntry)
	if codecConfigurationBox != nil {
		if err := setFieldBox(importantFields, moovFieldIDs.codecConfigurationBox, codecConfigurationBox); err != nil {
			return nil, fmt.Errorf("unable to set field id=%d: %w", moovFieldIDs.codecConfigurationBox, err)
		}
	}

	sinf := getSampleEntrySinf(sampleEntry)
	if sinf != nil {
		if sinf.Schm != nil {
			schemeType := sinf.Schm.SchemeType
			if len(schemeType) != 4 {
				return nil, fmt.Errorf("unable to set field id=%d: expected 4-byte code, got %q", moovFieldIDs.schemeType, schemeType)
			}
			schemeTypeCode := uint32(schemeType[0])<<24 | uint32(schemeType[1])<<16 | uint32(schemeType[2])<<8 | uint32(schemeType[3])
			importantFields[moovFieldIDs.schemeType] = binary.AppendVarint(nil, int64(schemeTypeCode))
		}

		if sinf.Schi != nil && sinf.Schi.Tenc != nil {
			tenc := sinf.Schi.Tenc
			importantFields[moovFieldIDs.default_crypt_byte_block] = binary.AppendVarint(nil, int64(tenc.DefaultCryptByteBlock))
			importantFields[moovFieldIDs.default_skip_byte_block] = binary.AppendVarint(nil, int64(tenc.DefaultSkipByteBlock))
			if len(tenc.DefaultKID) == 16 {
				importantFields[moovFieldIDs.defaultKID] = prependVarintSize(append([]byte(nil), tenc.DefaultKID...))
			}
			importantFields[moovFieldIDs.DefaultPerSampleIVSize] = binary.AppendVarint(nil, int64(tenc.DefaultPerSampleIVSize))
			if len(tenc.DefaultConstantIV) > 0 {
				importantFields[moovFieldIDs.defaultConstantIVSize] = binary.AppendVarint(nil, int64(len(tenc.DefaultConstantIV)))
				importantFields[moovFieldIDs.defaultConstantIV] = prependVarintSize(append([]byte(nil), tenc.DefaultConstantIV...))
			}
		}
	}

	if moov.Mvex != nil && moov.Mvex.Trex != nil {
		importantFields[moovFieldIDs.defaultSampleDuration] = binary.AppendVarint(nil, int64(moov.Mvex.Trex.DefaultSampleDuration))
		importantFields[moovFieldIDs.defaultSampleSize] = binary.AppendVarint(nil, int64(moov.Mvex.Trex.DefaultSampleSize))
		importantFields[moovFieldIDs.defaultSampleFlags] = binary.AppendVarint(nil, int64(moov.Mvex.Trex.DefaultSampleFlags))
	}
	return importantFields, nil
}

func findChildBoxByType(children []mp4.Box, boxType string) mp4.Box {
	for _, child := range children {
		if child.Type() == boxType {
			return child
		}
	}
	return nil
}

func getCodecConfigurationBox(sampleEntry mp4.Box) mp4.Box {
	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		for _, boxType := range []string{"avcC", "hvcC", "av1C", "av3c", "vvcC", "vpcC"} {
			if box := findChildBoxByType(entry.Children, boxType); box != nil {
				return box
			}
		}
	case *mp4.AudioSampleEntryBox:
		for _, boxType := range []string{"esds", "dac3", "dac4", "dec3", "dOps", "mhaC"} {
			if box := findChildBoxByType(entry.Children, boxType); box != nil {
				return box
			}
		}
	}
	return nil
}

func getSampleEntrySinf(sampleEntry mp4.Box) *mp4.SinfBox {
	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		return entry.Sinf
	case *mp4.AudioSampleEntryBox:
		return entry.Sinf
	default:
		return nil
	}
}

func getSampleEntryTenc(sampleEntry mp4.Box) *mp4.TencBox {
	sinf := getSampleEntrySinf(sampleEntry)
	if sinf == nil || sinf.Schi == nil {
		return nil
	}
	return sinf.Schi.Tenc
}

func getPrimarySampleEntry(moov *mp4.MoovBox) (*mp4.TrakBox, mp4.Box, error) {
	if moov == nil {
		return nil, nil, fmt.Errorf("moov not defined")
	}
	track := moov.Trak
	if track == nil && len(moov.Traks) > 0 {
		track = moov.Traks[0]
	}
	if track == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
		return nil, nil, fmt.Errorf("track sample description not defined")
	}
	return track, track.Mdia.Minf.Stbl.Stsd.Children[0], nil
}

func ensurePrimarySampleEntry(init *mp4.InitSegment, fieldValues map[locFieldID][]byte) (*mp4.TrakBox, mp4.Box, error) {
	if init == nil || init.Moov == nil {
		return nil, nil, fmt.Errorf("moov not defined")
	}
	moov := init.Moov

	track, sampleEntry, err := getPrimarySampleEntry(moov)
	if err == nil {
		return track, sampleEntry, nil
	}

	formatCode, ok := readVarint(moovFieldIDs.format, fieldValues)
	if !ok {
		return nil, nil, fmt.Errorf("missing field id=%d", moovFieldIDs.format)
	}
	format := uint32ToFourCC(uint32(formatCode))
	mediaType := inferMediaTypeFromFormat(format, fieldValues)

	if moov.Mvex == nil {
		moov.AddChild(mp4.NewMvexBox())
	}

	track = moov.Trak
	if track == nil && len(moov.Traks) > 0 {
		track = moov.Traks[0]
	}
	if track == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil {
		init.AddEmptyTrack(moov.Mvhd.Timescale, mediaType, "und")
		if len(moov.Traks) == 0 {
			return nil, nil, fmt.Errorf("track sample description not defined")
		}
		track = moov.Traks[len(moov.Traks)-1]
	}

	stsd := track.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) == 0 {
		entry, err := createSampleEntryForFormat(format, mediaType)
		if err != nil {
			return nil, nil, err
		}
		stsd.AddChild(entry)
	}
	if len(stsd.Children) == 0 {
		return nil, nil, fmt.Errorf("track sample description not defined")
	}

	return track, stsd.Children[0], nil
}

func inferMediaTypeFromFormat(format string, fieldValues map[locFieldID][]byte) string {
	switch format {
	case "mp4a", "ac-3", "ec-3", "ac-4", "Opus", "mha1", "mha2", "mhm1", "mhm2", "enca":
		return "audio"
	case "stpp", "wvtt", "evte":
		return "text"
	case "avc1", "avc3", "hvc1", "hev1", "vvc1", "vvi1", "av01", "vp08", "vp09", "avs3", "encv":
		return "video"
	}
	if _, ok := fieldValues[moovFieldIDs.channelCount]; ok {
		return "audio"
	}
	if _, ok := fieldValues[moovFieldIDs.sampleRate]; ok {
		return "audio"
	}
	return "video"
}

func createSampleEntryForFormat(format, mediaType string) (mp4.Box, error) {
	switch mediaType {
	case "audio":
		entry := mp4.CreateAudioSampleEntryBox(format, 2, 16, 48000, nil)
		if format == "enca" {
			addDefaultEncryptionBoxes(entry, "mp4a")
		}
		return entry, nil
	case "video":
		entry := mp4.CreateVisualSampleEntryBox(format, 0, 0, nil)
		if format == "encv" {
			addDefaultEncryptionBoxes(entry, "avc1")
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func addDefaultEncryptionBoxes(sampleEntry mp4.Box, frma string) {
	sinf := &mp4.SinfBox{}
	sinf.AddChild(&mp4.FrmaBox{DataFormat: frma})
	sinf.AddChild(&mp4.SchmBox{SchemeType: "cenc", SchemeVersion: 0x00010000})
	schi := &mp4.SchiBox{}
	schi.AddChild(&mp4.TencBox{DefaultKID: make(mp4.UUID, 16)})
	sinf.AddChild(schi)

	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		entry.AddChild(sinf)
	case *mp4.AudioSampleEntryBox:
		entry.AddChild(sinf)
	}
}

func ensureTrackElstEntry(track *mp4.TrakBox) *mp4.ElstEntry {
	if track.Edts == nil {
		track.AddChild(&mp4.EdtsBox{})
	}
	if len(track.Edts.Elst) == 0 {
		elst := &mp4.ElstBox{
			Version: 0,
			Entries: []mp4.ElstEntry{{
				SegmentDuration:   0,
				MediaTime:         0,
				MediaRateInteger:  1,
				MediaRateFraction: 0,
			}},
		}
		track.Edts.Elst = append(track.Edts.Elst, elst)
		track.Edts.Children = append(track.Edts.Children, elst)
	}
	if len(track.Edts.Elst[0].Entries) == 0 {
		track.Edts.Elst[0].Entries = append(track.Edts.Elst[0].Entries, mp4.ElstEntry{
			SegmentDuration:   0,
			MediaTime:         0,
			MediaRateInteger:  1,
			MediaRateFraction: 0,
		})
	}
	return &track.Edts.Elst[0].Entries[0]
}

func setSampleEntryChildBox(sampleEntry mp4.Box, child mp4.Box) error {
	if child == nil {
		return nil
	}
	childType := child.Type()
	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		entry.Children = removeChildBoxesByType(entry.Children, childType)
		entry.AddChild(child)
		return nil
	case *mp4.AudioSampleEntryBox:
		entry.Children = removeChildBoxesByType(entry.Children, childType)
		entry.AddChild(child)
		return nil
	default:
		return fmt.Errorf("unsupported sample entry type %T", sampleEntry)
	}
}

func removeChildBoxesByType(children []mp4.Box, boxType string) []mp4.Box {
	filtered := make([]mp4.Box, 0, len(children))
	for _, child := range children {
		if child.Type() != boxType {
			filtered = append(filtered, child)
		}
	}
	return filtered
}

func readBoxField(id locFieldID, fieldValues map[locFieldID][]byte) (mp4.Box, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	box, err := decodeBox(value)
	if err != nil {
		return nil, true, fmt.Errorf("invalid field id=%d: %w", id, err)
	}
	return box, true, nil
}

func decodeBox(data []byte) (mp4.Box, error) {
	reader := bytes.NewReader(data)
	box, err := mp4.DecodeBox(0, reader)
	if err != nil {
		return nil, err
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("trailing bytes after box decode")
	}
	return box, nil
}

func cloneMoovBox(moov *mp4.MoovBox) (*mp4.MoovBox, error) {
	encoded, err := encodeBox(moov)
	if err != nil {
		return nil, err
	}
	box, err := decodeBox(encoded)
	if err != nil {
		return nil, err
	}
	clonedMoov, ok := box.(*mp4.MoovBox)
	if !ok {
		return nil, fmt.Errorf("decoded box is %T, expected *mp4.MoovBox", box)
	}
	return clonedMoov, nil
}

func uint32ToFourCC(value uint32) string {
	return string([]byte{
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	})
}

func setFieldBox(fields map[locFieldID][]byte, key locFieldID, box mp4.Box) error {
	if box == nil {
		return nil
	}
	encoded, err := encodeBox(box)
	if err != nil {
		return err
	}
	fields[key] = prependVarintSize(encoded)
	return nil
}

func encodeBox(box mp4.Box) ([]byte, error) {
	var buffer bytes.Buffer
	if err := box.Encode(&buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
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

func repeatInt64(value int64, count int) []int64 {
	values := make([]int64, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func reconstructSencFromFields(fieldValues map[locFieldID][]byte, sampleCount int, perSampleIVSize uint8) (*mp4.SencBox, error) {
	ivsPayload, hasIVs := fieldValues[moofFieldIDs.InitializationVector]
	subSampleCounts, hasSubSampleCounts, err := readVarintList(moofFieldIDs.SubsampleCount, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfClearData, hasBytesOfClearData, err := readVarintList(moofFieldIDs.BytesOfClearData, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfProtectedData, hasBytesOfProtectedData, err := readVarintList(moofFieldIDs.BytesOfProtectedData, fieldValues)
	if err != nil {
		return nil, err
	}

	if !hasIVs && !hasSubSampleCounts && !hasBytesOfClearData && !hasBytesOfProtectedData {
		return nil, nil
	}

	senc := mp4.CreateSencBox()

	if hasIVs {
		if perSampleIVSize == 0 {
			return nil, fmt.Errorf("field id=%d present but per-sample IV size is 0", moofFieldIDs.InitializationVector)
		}
		if len(ivsPayload)%int(perSampleIVSize) != 0 {
			return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.InitializationVector)
		}
		if len(ivsPayload)/int(perSampleIVSize) != sampleCount {
			return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.InitializationVector)
		}
		senc.SetPerSampleIVSize(perSampleIVSize)
	}

	if hasSubSampleCounts && len(subSampleCounts) != sampleCount {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.SubsampleCount)
	}

	totalSubsamples := 0
	if hasSubSampleCounts {
		for _, count := range subSampleCounts {
			if count < 0 {
				return nil, fmt.Errorf("invalid field id=%d", moofFieldIDs.SubsampleCount)
			}
			totalSubsamples += int(count)
		}
	}

	if hasBytesOfClearData && len(bytesOfClearData) != totalSubsamples {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.BytesOfClearData)
	}
	if hasBytesOfProtectedData && len(bytesOfProtectedData) != totalSubsamples {
		return nil, fmt.Errorf("field id=%d length mismatch", moofFieldIDs.BytesOfProtectedData)
	}
	if (hasBytesOfClearData || hasBytesOfProtectedData) && !hasSubSampleCounts {
		return nil, fmt.Errorf("missing field id=%d", moofFieldIDs.SubsampleCount)
	}

	for i := 0; i < sampleCount; i++ {
		var sampleIV []byte
		if hasIVs {
			sampleIV = append([]byte(nil), ivsPayload[:int(perSampleIVSize)]...)
			ivsPayload = ivsPayload[int(perSampleIVSize):]
		}

		var subsamples []mp4.SubSamplePattern
		if hasSubSampleCounts {
			subsampleCount := int(subSampleCounts[i])
			subsamples = make([]mp4.SubSamplePattern, subsampleCount)
			for j := 0; j < subsampleCount; j++ {
				clearData := bytesOfClearData[0]
				protectedData := bytesOfProtectedData[0]
				if clearData < 0 || clearData > 0xffff {
					return nil, fmt.Errorf("invalid field id=%d", moofFieldIDs.BytesOfClearData)
				}
				if protectedData < 0 || protectedData > 0xffffffff {
					return nil, fmt.Errorf("invalid field id=%d", moofFieldIDs.BytesOfProtectedData)
				}
				subsamples[j] = mp4.SubSamplePattern{
					BytesOfClearData:     uint16(clearData),
					BytesOfProtectedData: uint32(protectedData),
				}
				bytesOfClearData = bytesOfClearData[1:]
				bytesOfProtectedData = bytesOfProtectedData[1:]
			}
		}

		if err := senc.AddSample(mp4.SencSample{IV: sampleIV, SubSamples: subsamples}); err != nil {
			return nil, fmt.Errorf("unable to reconstruct senc: %w", err)
		}
	}

	return senc, nil
}

func readVarint(id locFieldID, fieldValues map[locFieldID][]byte) (int64, bool) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false
	}
	varint, _ := binary.Varint(value)
	return varint, true
}

func readVarintList(id locFieldID, fieldValues map[locFieldID][]byte) ([]int64, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	var varintList []int64
	pos := 0
	for pos < len(value) {
		varint, deltaPos := binary.Varint(value)
		varintList = append(varintList, varint)
		pos += deltaPos
	}
	return varintList, true, nil
}
