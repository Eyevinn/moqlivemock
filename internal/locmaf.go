package internal

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
)

const (
	MoovHeader      = 21
	MoofHeader      = 23
	MoofDeltaHeader = 25
)

type locmafID int

var moofLocmafIDs = struct {
	//tfhd
	sampleDescriptionIndex locmafID
	defaultSampleDuration  locmafID
	defaultSampleSize      locmafID
	defaultSampleFlags     locmafID

	//tfdt
	baseMediaDecodeTime locmafID

	//trun
	sampleCount                  locmafID
	firstSampleFlags             locmafID
	sampleSizes                  locmafID
	sampleDurations              locmafID
	sampleCompositionTimeOffsets locmafID
	sampleFlags                  locmafID

	//senc
	perSampleIVSize      locmafID
	initializationVector locmafID
	subsampleCount       locmafID
	bytesOfClearData     locmafID
	bytesOfProtectedData locmafID
}{
	sampleDescriptionIndex:       2,
	defaultSampleDuration:        4,
	defaultSampleSize:            6,
	defaultSampleFlags:           8,
	baseMediaDecodeTime:          10,
	firstSampleFlags:             12,
	sampleCount:                  16,
	sampleSizes:                  1,
	sampleDurations:              3,
	sampleCompositionTimeOffsets: 5,
	sampleFlags:                  7,
	perSampleIVSize:              14,
	initializationVector:         9,
	subsampleCount:               11,
	bytesOfClearData:             13,
	bytesOfProtectedData:         15,
}

var moovLocmafIDs = struct {
	//mvhd
	movieTimescale locmafID
	//tkhd
	tkhdFlags locmafID
	matrix    locmafID
	//elst
	mediaTime locmafID
	//stsd
	format locmafID

	//Video
	width  locmafID
	height locmafID
	colr   locmafID
	pasp   locmafID

	//Audio
	channelCount locmafID
	chnl         locmafID

	//stsd
	codecConfigurationBox locmafID
	//schm
	schemeType locmafID
	//tenc
	tencVersion            locmafID
	defaultCryptByteBlock  locmafID
	defaultSkipByteBlock   locmafID
	defaultKID             locmafID
	defaultPerSampleIVSize locmafID
	defaultConstantIVSize  locmafID
	defaultConstantIV      locmafID

	//trex
	defaultSampleDuration locmafID
	defaultSampleSize     locmafID
	defaultSampleFlags    locmafID
}{
	movieTimescale:         2,
	tkhdFlags:              34,
	matrix:                 4,
	mediaTime:              6,
	format:                 8,
	width:                  10,
	height:                 12,
	colr:                   3,
	pasp:                   5,
	channelCount:           14,
	chnl:                   7,
	codecConfigurationBox:  1,
	schemeType:             18,
	tencVersion:            36,
	defaultCryptByteBlock:  20,
	defaultSkipByteBlock:   22,
	defaultKID:             11,
	defaultPerSampleIVSize: 24,
	defaultConstantIVSize:  26,
	defaultConstantIV:      9,
	defaultSampleDuration:  28,
	defaultSampleSize:      30,
	defaultSampleFlags:     32,
}

// CompressMoof compresses a moof box by converting it to locmaf format.
func CompressMoof(moof *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	importantFields, err := extractImportantMoofFields(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moof fields: %w", err)
	}
	return encodeFields(importantFields), nil
}

// CompressMoof compresses a moov box by converting it to locmaf format.
func CompressMoov(moov *mp4.MoovBox) ([]byte, error) {
	importantFields, err := extractImportantMoovFields(moov)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moov fields: %w", err)
	}
	return encodeFields(importantFields), nil
}

// extractImportantMoofFields extracts the non-derivable fields in the moof box.
// If a moof default matches a moov default, the moof field is considered derivable.
func extractImportantMoofFields(moof *mp4.MoofBox, moov *mp4.MoovBox) (map[locmafID][]byte, error) {
	importantFields := make(map[locmafID][]byte)

	if moof == nil || moof.Traf == nil {
		return nil, fmt.Errorf("moof or traf not defined")
	}
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("moov or trex not defined")
	}

	trun := moof.Traf.Trun
	singleSample := trun != nil && len(trun.Samples) == 1

	tfhd := moof.Traf.Tfhd
	if tfhd == nil {
		return nil, fmt.Errorf("tfhd is nil")
	}
	if tfhd.SampleDescriptionIndex != moov.Mvex.Trex.DefaultSampleDescriptionIndex {
		importantFields[moofLocmafIDs.sampleDescriptionIndex] = appendVarint(nil, uint64(tfhd.SampleDescriptionIndex))
	}
	if tfhd.DefaultSampleDuration != moov.Mvex.Trex.DefaultSampleDuration {
		importantFields[moofLocmafIDs.defaultSampleDuration] = appendVarint(nil, uint64(tfhd.DefaultSampleDuration))
	}
	if !singleSample && tfhd.DefaultSampleSize != moov.Mvex.Trex.DefaultSampleSize {
		importantFields[moofLocmafIDs.defaultSampleSize] = appendVarint(nil, uint64(tfhd.DefaultSampleSize))
	}
	if tfhd.DefaultSampleFlags != moov.Mvex.Trex.DefaultSampleFlags {
		importantFields[moofLocmafIDs.defaultSampleFlags] = appendVarint(nil, uint64(tfhd.DefaultSampleFlags))
	}

	tfdt := moof.Traf.Tfdt
	if tfdt == nil {
		return nil, fmt.Errorf("tfdt is nil")
	}
	importantFields[moofLocmafIDs.baseMediaDecodeTime] = appendVarint(nil, tfdt.BaseMediaDecodeTime())

	if trun == nil {
		return nil, fmt.Errorf("trun is nil")
	}
	importantFields[moofLocmafIDs.sampleCount] = appendVarint(nil, uint64(len(trun.Samples)))
	firstSampleFlags, firstSampleFlagsPresent := trun.FirstSampleFlags()
	if firstSampleFlagsPresent {
		importantFields[moofLocmafIDs.firstSampleFlags] = appendVarint(nil, uint64(firstSampleFlags))
	}

	var sizes []byte
	var durations []byte
	var flags []byte
	var compositionTimeOffsets []byte

	for _, sample := range trun.Samples {
		sizes = appendVarint(sizes, uint64(sample.Size))
		durations = appendVarint(durations, uint64(sample.Dur))
		flags = appendVarint(flags, uint64(sample.Flags))
		compositionTimeOffsets = appendSignedVarint(compositionTimeOffsets, int64(sample.CompositionTimeOffset))
	}
	if trun.HasSampleDuration() {
		importantFields[moofLocmafIDs.sampleDurations] = durations
	}
	if trun.HasSampleFlags() {
		importantFields[moofLocmafIDs.sampleFlags] = flags
	}
	if trun.HasSampleSize() && !singleSample {
		importantFields[moofLocmafIDs.sampleSizes] = sizes
	}
	if trun.HasSampleCompositionTimeOffset() {
		importantFields[moofLocmafIDs.sampleCompositionTimeOffsets] = compositionTimeOffsets
	}

	senc, perSampleIVSize, err := getParsedSencBox(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to parse senc: %w", err)
	}
	if senc != nil {
		if perSampleIVSize != getDefaultPerSampleIVSize(moov, moof.Traf.Tfhd.TrackID) {
			importantFields[moofLocmafIDs.perSampleIVSize] = appendVarint(nil, uint64(perSampleIVSize))
		}
		if perSampleIVSize > 0 && len(senc.IVs) > 0 {
			allIVs := make([]byte, 0)
			for _, iv := range senc.IVs {
				allIVs = append(allIVs, iv...)
			}
			importantFields[moofLocmafIDs.initializationVector] = allIVs
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
				subSampleCounts = appendVarint(subSampleCounts, uint64(len(sampleSubSamples)))
				for _, subSample := range sampleSubSamples {
					bytesOfClearData = appendVarint(bytesOfClearData, uint64(subSample.BytesOfClearData))
					bytesOfProtectedData = appendVarint(bytesOfProtectedData, uint64(subSample.BytesOfProtectedData))
				}
			}
			importantFields[moofLocmafIDs.subsampleCount] = subSampleCounts
			importantFields[moofLocmafIDs.bytesOfClearData] = bytesOfClearData
			importantFields[moofLocmafIDs.bytesOfProtectedData] = bytesOfProtectedData
		}
	}
	return importantFields, nil
}

// DecompressMoof converts locmaf object to a moof box.
func DecompressMoof(data []byte, seqnum uint32, moov *mp4.MoovBox) (*mp4.MoofBox, error) {
	object, err := parseLocmafObject(data)
	if err != nil {
		return nil, err
	}
	if object.headerID != MoofHeader {
		return nil, fmt.Errorf("unsupported moof header id=%d", object.headerID)
	}
	fieldValues, err := separateFields(object.properties)
	if err != nil {
		return nil, err
	}
	return decompressMoofUsingFieldValues(fieldValues, seqnum, moov, len(object.mdatPayload))
}

// decompressMoofUsingFieldValues converts a map of locmaf field values to a moof box.
func decompressMoofUsingFieldValues(fieldValues map[locmafID][]byte, seqnum uint32, moov *mp4.MoovBox,
	mdatPayloadLength int) (*mp4.MoofBox, error) {
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("moov or trex not defined")
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

	if sampleDescriptionIndex, ok := readVarint(moofLocmafIDs.sampleDescriptionIndex, fieldValues); ok {
		traf.Tfhd.SampleDescriptionIndex = uint32(sampleDescriptionIndex)
		traf.Tfhd.Flags |= mp4.TfhdSampleDescriptionIndexPresentFlag
	}

	if defaultSampleDuration, ok := readVarint(moofLocmafIDs.defaultSampleDuration, fieldValues); ok {
		traf.Tfhd.DefaultSampleDuration = uint32(defaultSampleDuration)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleDurationPresentFlag
	}

	defaultSampleSize, hasDefaultSampleSize := readVarint(moofLocmafIDs.defaultSampleSize, fieldValues)
	if hasDefaultSampleSize {
		traf.Tfhd.DefaultSampleSize = uint32(defaultSampleSize)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleSizePresentFlag
	}

	if defaultSampleFlags, ok := readVarint(moofLocmafIDs.defaultSampleFlags, fieldValues); ok {
		traf.Tfhd.DefaultSampleFlags = uint32(defaultSampleFlags)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	}

	if ivSizeValue, ok := readVarint(moofLocmafIDs.perSampleIVSize, fieldValues); ok {
		perSampleIVSize = uint8(ivSizeValue)
	}

	baseMediaDecodeTime, ok := readVarint(moofLocmafIDs.baseMediaDecodeTime, fieldValues)
	if !ok {
		return nil, fmt.Errorf("missing locmaf id=%d", moofLocmafIDs.baseMediaDecodeTime)
	}
	traf.Tfdt.SetBaseMediaDecodeTime(uint64(baseMediaDecodeTime))

	if firstSampleFlags, ok := readVarint(moofLocmafIDs.firstSampleFlags, fieldValues); ok {
		traf.Trun.SetFirstSampleFlags(uint32(firstSampleFlags))
	}
	sampleCountValue, ok := readVarint(moofLocmafIDs.sampleCount, fieldValues)
	if !ok {
		return nil, fmt.Errorf("missing locmaf id=%d", moofLocmafIDs.sampleCount)
	}
	sampleCount := int(sampleCountValue)
	sampleCompositionTimeOffsets, hasCompositionTimeOffsets, err :=
		readSignedVarintList(moofLocmafIDs.sampleCompositionTimeOffsets, fieldValues)
	if err != nil {
		return nil, err
	}
	if !hasCompositionTimeOffsets {
		sampleCompositionTimeOffsets = repeatInt64(0, sampleCount)
		traf.Trun.Flags &^= mp4.TrunSampleCompositionTimeOffsetPresentFlag
	}
	if len(sampleCompositionTimeOffsets) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.sampleCompositionTimeOffsets)
	}

	sampleSizes, ok, err := readVarintList(moofLocmafIDs.sampleSizes, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		if sampleCount == 1 && !hasDefaultSampleSize {
			if mdatPayloadLength <= 0 {
				return nil, fmt.Errorf("missing sample size for single-sample moof from mdat payload length")
			}
			sampleSizes = []uint64{uint64(mdatPayloadLength)}
		} else {
			sampleSizes = repeatUint64(uint64(traf.Tfhd.DefaultSampleSize), sampleCount)
		}
	}

	sampleDurations, ok, err := readVarintList(moofLocmafIDs.sampleDurations, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleDurations = repeatUint64(uint64(traf.Tfhd.DefaultSampleDuration), sampleCount)
	}

	sampleFlags, ok, err := readVarintList(moofLocmafIDs.sampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleFlags = repeatUint64(uint64(traf.Tfhd.DefaultSampleFlags), sampleCount)
	}

	if len(sampleDurations) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.sampleDurations)
	}
	if len(sampleFlags) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.sampleFlags)
	}
	if len(sampleSizes) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.sampleSizes)
	}

	for i := 0; i < sampleCount; i++ {
		traf.Trun.AddSample(mp4.NewSample(uint32(sampleFlags[i]), uint32(sampleDurations[i]),
			uint32(sampleSizes[i]), int32(sampleCompositionTimeOffsets[i])))
	}

	senc, err := reconstructSencFromFields(fieldValues, sampleCount, perSampleIVSize,
		shouldCreateEmptySenc(moov, traf.Tfhd.TrackID, perSampleIVSize))
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

// DecompressMoof converts locmaf packaged init segment to a InitSegment box.
func DecompressInit(data []byte, track Track) (*mp4.InitSegment, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty locmaf init data")
	}

	fieldValues, err := separateFields(data)
	if err != nil {
		return nil, err
	}

	init := mp4.CreateEmptyInit()

	moov := init.Moov

	if moov.Mvhd == nil {
		return nil, fmt.Errorf("mvhd not defined")
	}

	movieTimescale, ok := readVarint(moovLocmafIDs.movieTimescale, fieldValues)
	if ok {
		moov.Mvhd.Timescale = uint32(movieTimescale)
	}
	role := track.Role
	if role == "" {
		return nil, fmt.Errorf("MSF role is not defined")
	}

	trak, err := ensurePrimarySampleEntry(init, fieldValues, role)
	if err != nil {
		return nil, fmt.Errorf("unable to create trak: %w", err)
	}
	if track.Timescale == nil {
		return nil, fmt.Errorf("no timescale found in MSF track")
	}

	tkhdFlags, ok := readVarint(moovLocmafIDs.tkhdFlags, fieldValues)
	if ok {
		trak.Tkhd.Flags = uint32(tkhdFlags)
	}

	mediaTime, ok := readVarint(moovLocmafIDs.mediaTime, fieldValues)
	if ok {
		elstEntry := ensureTrackElstEntry(trak)
		elstEntry.MediaTime = int64(mediaTime)
	}

	trak.Mdia.Mdhd.Timescale = uint32(*track.Timescale)

	sampleEntry := trak.Mdia.Minf.Stbl.Stsd.Children[0]
	if _, isVideo := sampleEntry.(*mp4.VisualSampleEntryBox); isVideo {
		if track.Width == nil {
			return nil, fmt.Errorf("no width found in MSF track")
		}
		trak.Tkhd.Width = mp4.Fixed32(*track.Width << 16)
		if track.Height == nil {
			return nil, fmt.Errorf("no height found in MSF track")
		}
		trak.Tkhd.Height = mp4.Fixed32(*track.Height << 16)
	}

	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		if track.Width != nil {
			entry.Width = uint16(*track.Width)
		}
		if track.Height != nil {
			entry.Height = uint16(*track.Height)
		}
	case *mp4.AudioSampleEntryBox:
		if track.SampleRate == nil {
			return nil, fmt.Errorf("no sample rate found in MSF track")
		}
		entry.SampleRate = uint16(*track.SampleRate)
	}

	channelCount, ok := readVarint(moovLocmafIDs.channelCount, fieldValues)
	if ok {
		if audioEntry, ok := sampleEntry.(*mp4.AudioSampleEntryBox); ok {
			audioEntry.ChannelCount = uint16(channelCount)
		}
	}

	colrBox, ok, err := readBoxField(moovLocmafIDs.colr, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, colrBox)
	}

	codecConfigurationBox, ok, err := readBoxField(moovLocmafIDs.codecConfigurationBox, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, codecConfigurationBox)
	}

	paspBox, ok, err := readBoxField(moovLocmafIDs.pasp, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, paspBox)
	}

	chnlBox, ok, err := readBoxField(moovLocmafIDs.chnl, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, chnlBox)
	}

	if (sampleEntry.Type() == "encv" || sampleEntry.Type() == "enca") && getSampleEntrySinf(sampleEntry) == nil {
		addDefaultEncryptionBoxes(sampleEntry)
	}

	schemeTypeCode, ok := readVarint(moovLocmafIDs.schemeType, fieldValues)
	if ok {
		sinf := getSampleEntrySinf(sampleEntry)
		if sinf != nil && sinf.Schm != nil {
			sinf.Schm.SchemeType = uint32ToFourCC(uint32(schemeTypeCode))
		}
		if sinf != nil && track.Codec != "" {
			fourCC := strings.Split(track.Codec, ".")[0]
			if len(fourCC) != 4 {
				return nil, fmt.Errorf("codec is not four characters long before \".\" %s", fourCC)
			}
			sinf.Frma.DataFormat = fourCC
		}
	}

	tenc := getSampleEntryTenc(sampleEntry)

	tencVersion, ok := readVarint(moovLocmafIDs.tencVersion, fieldValues)
	if ok && tenc != nil {
		tenc.Version = byte(tencVersion)
	}

	defaultCryptByteBlock, ok := readVarint(moovLocmafIDs.defaultCryptByteBlock, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultCryptByteBlock = uint8(defaultCryptByteBlock)
	}

	defaultSkipByteBlock, ok := readVarint(moovLocmafIDs.defaultSkipByteBlock, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultSkipByteBlock = uint8(defaultSkipByteBlock)
	}

	if defaultKID, ok := fieldValues[moovLocmafIDs.defaultKID]; ok {
		if tenc != nil && len(defaultKID) == 16 {
			tenc.DefaultKID = append(mp4.UUID(nil), defaultKID...)
		}
	}

	defaultPerSampleIVSize, ok := readVarint(moovLocmafIDs.defaultPerSampleIVSize, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultPerSampleIVSize = uint8(defaultPerSampleIVSize)
	}

	defaultConstantIVSize, hasConstantIVSize := readVarint(moovLocmafIDs.defaultConstantIVSize, fieldValues)
	defaultConstantIV, hasConstantIV := fieldValues[moovLocmafIDs.defaultConstantIV]
	if hasConstantIV && tenc != nil {
		if hasConstantIVSize && int(defaultConstantIVSize) != len(defaultConstantIV) {
			defaultConstantIV = defaultConstantIV[:0]
		}
		tenc.DefaultConstantIV = append([]byte(nil), defaultConstantIV...)
	}

	defaultSampleDuration, ok := readVarint(moovLocmafIDs.defaultSampleDuration, fieldValues)
	if ok && moov.Mvex != nil && moov.Mvex.Trex != nil {
		moov.Mvex.Trex.DefaultSampleDuration = uint32(defaultSampleDuration)
	}

	defaultSampleSize, ok := readVarint(moovLocmafIDs.defaultSampleSize, fieldValues)
	if ok && moov.Mvex != nil && moov.Mvex.Trex != nil {
		moov.Mvex.Trex.DefaultSampleSize = uint32(defaultSampleSize)
	}

	defaultSampleFlags, ok := readVarint(moovLocmafIDs.defaultSampleFlags, fieldValues)
	if ok && moov.Mvex != nil && moov.Mvex.Trex != nil {
		moov.Mvex.Trex.DefaultSampleFlags = uint32(defaultSampleFlags)
	}

	return init, nil
}

// separateFields takes an byte array of locmaf properties and
// separates the fields to a map from locmafID to the byte encoding.
func separateFields(data []byte) (map[locmafID][]byte, error) {
	fieldValues := make(map[locmafID][]byte)
	pos := 0
	for pos < len(data) {
		idValue, deltaPos, err := quicvarint.Parse(data[pos:])
		if err != nil {
			return nil, fmt.Errorf("invalid locmaf id at offset %d", pos)
		}
		pos += deltaPos
		id := locmafID(idValue)
		if id%2 == 0 { //no length field
			_, deltaPos, err := quicvarint.Parse(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("invalid varint field value for id=%d", id)
			}
			if pos+deltaPos > len(data) {
				return nil, fmt.Errorf("locmaf id=%d exceeds payload length", id)
			}
			fieldValues[id] = append([]byte(nil), data[pos:pos+deltaPos]...)
			pos += deltaPos
		} else { //has length field
			valueLength, deltaPos, err := quicvarint.Parse(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("invalid field length for id=%d", id)
			}
			pos += deltaPos
			if pos+int(valueLength) > len(data) {
				return nil, fmt.Errorf("locmaf id=%d exceeds payload length", id)
			}
			fieldValues[id] = append([]byte(nil), data[pos:pos+int(valueLength)]...)
			pos += int(valueLength)
		}
	}
	return fieldValues, nil
}

func createSizedLocmafProperty(headerID int64, payload []byte) []byte {
	locmafHeader := prependVarintSize(payload)
	locmafHeader = append(appendVarint(nil, uint64(headerID)), locmafHeader...)
	return locmafHeader
}

// encodeFields encodes a map of fields to a single contiguous byte array.
func encodeFields(fields map[locmafID][]byte) []byte {
	keys := make([]locmafID, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})

	payload := make([]byte, 0)
	for _, key := range keys {
		value := fields[key]
		payload = appendVarint(payload, uint64(key))
		if key%2 == 1 {
			payload = appendVarint(payload, uint64(len(value)))
		}
		payload = append(payload, value...)
	}
	return payload
}

type locmafObject struct {
	headerID    int64
	properties  []byte
	mdatPayload []byte
}

func parseLocmafObject(payload []byte) (*locmafObject, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty locmaf moof data")
	}
	headerID, n, err := quicvarint.Parse(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid locmaf header")
	}
	pos := n

	propertiesLength, n, err := quicvarint.Parse(payload[pos:])
	if err != nil {
		return nil, fmt.Errorf("invalid locmaf payload length")
	}
	pos += n
	if pos+int(propertiesLength) > len(payload) {
		return nil, fmt.Errorf("locmaf payload exceeds object length")
	}

	propertiesPayload := payload[pos : pos+int(propertiesLength)]
	mdatPayload := payload[pos+int(propertiesLength):]

	return &locmafObject{
		headerID:    int64(headerID),
		properties:  propertiesPayload,
		mdatPayload: mdatPayload,
	}, nil
}

// extractImportantMoofFields extracts the non-derivable fields in the moov box.
func extractImportantMoovFields(moov *mp4.MoovBox) (map[locmafID][]byte, error) {
	importantFields := make(map[locmafID][]byte)
	if moov == nil {
		return nil, fmt.Errorf("moov not defined")
	}
	if moov.Mvhd == nil {
		return nil, fmt.Errorf("mvhd not defined")
	}
	importantFields[moovLocmafIDs.movieTimescale] = appendVarint(nil, uint64(moov.Mvhd.Timescale))

	track := moov.Trak
	if track == nil && len(moov.Traks) > 0 {
		track = moov.Traks[0]
	}
	if track == nil || track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil ||
		track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
		return nil, fmt.Errorf("track sample description not defined")
	}
	importantFields[moovLocmafIDs.tkhdFlags] = appendVarint(nil, uint64(track.Tkhd.Flags))

	if track.Edts != nil && len(track.Edts.Elst) > 0 && len(track.Edts.Elst[0].Entries) > 0 {
		mediaTime := track.Edts.Elst[0].Entries[0].MediaTime
		if mediaTime < 0 {
			return nil, fmt.Errorf("unable to set locmaf id=%d: negative media time", moovLocmafIDs.mediaTime)
		}
		importantFields[moovLocmafIDs.mediaTime] = appendVarint(nil, uint64(mediaTime))
	}

	sampleEntry := track.Mdia.Minf.Stbl.Stsd.Children[0]
	format := sampleEntry.Type()
	if len(format) != 4 {
		return nil, fmt.Errorf("unable to set locmaf id=%d: expected 4-byte code, got %q", moovLocmafIDs.format, format)
	}
	formatCode := uint32(format[0])<<24 | uint32(format[1])<<16 | uint32(format[2])<<8 | uint32(format[3])
	importantFields[moovLocmafIDs.format] = appendVarint(nil, uint64(formatCode))

	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		if colr, err := findChildBoxByType(entry.Children, "colr"); err != nil {
			if err := setFieldBox(importantFields, moovLocmafIDs.colr, colr); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovLocmafIDs.colr, err)
			}
		}
		if pasp, err := findChildBoxByType(entry.Children, "pasp"); err != nil {
			if err = setFieldBox(importantFields, moovLocmafIDs.pasp, pasp); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovLocmafIDs.pasp, err)
			}
		}
	case *mp4.AudioSampleEntryBox:
		importantFields[moovLocmafIDs.channelCount] = appendVarint(nil, uint64(entry.ChannelCount))

		if chnl, err := findChildBoxByType(entry.Children, "chnl"); err != nil {
			if err := setFieldBox(importantFields, moovLocmafIDs.chnl, chnl); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovLocmafIDs.chnl, err)
			}
		}
	}

	codecConfigurationBox, _ := getCodecConfigurationBox(sampleEntry)
	if codecConfigurationBox != nil {
		if err := setFieldBox(importantFields, moovLocmafIDs.codecConfigurationBox, codecConfigurationBox); err != nil {
			return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovLocmafIDs.codecConfigurationBox, err)
		}
	}

	sinf := getSampleEntrySinf(sampleEntry)
	if sinf != nil {
		if sinf.Schm != nil {
			schemeType := sinf.Schm.SchemeType
			if len(schemeType) != 4 {
				return nil, fmt.Errorf("unable to set locmaf id=%d: expected 4-byte code, got %q",
					moovLocmafIDs.schemeType, schemeType)
			}
			schemeTypeCode := uint32(schemeType[0])<<24 | uint32(schemeType[1])<<16 |
				uint32(schemeType[2])<<8 | uint32(schemeType[3])

			importantFields[moovLocmafIDs.schemeType] = appendVarint(nil, uint64(schemeTypeCode))
		}

		if sinf.Schi != nil && sinf.Schi.Tenc != nil {
			tenc := sinf.Schi.Tenc
			importantFields[moovLocmafIDs.tencVersion] = appendVarint(nil, uint64(tenc.Version))
			importantFields[moovLocmafIDs.defaultCryptByteBlock] = appendVarint(nil, uint64(tenc.DefaultCryptByteBlock))
			importantFields[moovLocmafIDs.defaultSkipByteBlock] = appendVarint(nil, uint64(tenc.DefaultSkipByteBlock))
			if len(tenc.DefaultKID) == 16 {
				importantFields[moovLocmafIDs.defaultKID] = append([]byte(nil), tenc.DefaultKID...)
			}
			importantFields[moovLocmafIDs.defaultPerSampleIVSize] = appendVarint(nil, uint64(tenc.DefaultPerSampleIVSize))
			if len(tenc.DefaultConstantIV) > 0 {
				importantFields[moovLocmafIDs.defaultConstantIVSize] = appendVarint(nil, uint64(len(tenc.DefaultConstantIV)))
				importantFields[moovLocmafIDs.defaultConstantIV] = append([]byte(nil), tenc.DefaultConstantIV...)
			}
		}
	}

	if moov.Mvex != nil && moov.Mvex.Trex != nil {
		importantFields[moovLocmafIDs.defaultSampleDuration] = appendVarint(
			nil, uint64(moov.Mvex.Trex.DefaultSampleDuration))
		importantFields[moovLocmafIDs.defaultSampleSize] = appendVarint(nil, uint64(moov.Mvex.Trex.DefaultSampleSize))
		importantFields[moovLocmafIDs.defaultSampleFlags] = appendVarint(nil, uint64(moov.Mvex.Trex.DefaultSampleFlags))
	}
	return importantFields, nil
}

func findChildBoxByType(children []mp4.Box, boxType string) (mp4.Box, error) {
	for _, child := range children {
		if child.Type() == boxType {
			return child, nil
		}
	}
	return nil, fmt.Errorf("no child of type %s found", boxType)
}

func findCodecBox(children []mp4.Box) (mp4.Box, error) {
	for _, child := range children {
		for _, nonCodecBox := range []string{"Btrt", "Clap", "Pasp", "Sinf", "SmDm", "CoLL"} {
			if child.Type() != nonCodecBox {
				return child, nil
			}
		}
	}
	return nil, fmt.Errorf("no codec configuration box found")
}

func getCodecConfigurationBox(sampleEntry mp4.Box) (mp4.Box, error) {
	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		return findCodecBox(entry.Children)
	case *mp4.AudioSampleEntryBox:
		return findCodecBox(entry.Children)
	}
	return nil, fmt.Errorf("VisualSampleEntryBox or AudioSampleEntryBox not found")
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

func ensurePrimarySampleEntry(init *mp4.InitSegment, fieldValues map[locmafID][]byte, role string) (*mp4.TrakBox, error) {
	moov := init.Moov
	format := ""
	if formatCode, ok := readVarint(moovLocmafIDs.format, fieldValues); ok {
		format = uint32ToFourCC(uint32(formatCode))
	} else {
		return nil, fmt.Errorf("no format code provided")
	}
	trak := init.AddEmptyTrack(moov.Mvhd.Timescale, role, "und")

	entry := createSampleEntryForFormat(format, role)
	trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)

	return trak, nil
}

func createSampleEntryForFormat(format, mediaType string) mp4.Box {
	switch mediaType {
	case "audio":
		entry := mp4.CreateAudioSampleEntryBox(format, 0, 0, 0, nil)
		return entry
	default:
		entry := mp4.CreateVisualSampleEntryBox(format, 0, 0, nil)
		return entry
	}
}

func addDefaultEncryptionBoxes(sampleEntry mp4.Box) {
	sinf := &mp4.SinfBox{}
	sinf.AddChild(&mp4.FrmaBox{DataFormat: "test"})
	sinf.AddChild(&mp4.SchmBox{SchemeType: "test", SchemeVersion: 0x00010000})
	schi := &mp4.SchiBox{}
	schi.AddChild(&mp4.TencBox{DefaultKID: make(mp4.UUID, 16), DefaultIsProtected: 1})
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

func setSampleEntryChildBox(sampleEntry mp4.Box, child mp4.Box) {
	if child == nil {
		return
	}
	childType := child.Type()
	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		entry.Children = removeChildBoxesByType(entry.Children, childType)
		entry.AddChild(child)
	case *mp4.AudioSampleEntryBox:
		entry.Children = removeChildBoxesByType(entry.Children, childType)
		entry.AddChild(child)
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

func readBoxField(id locmafID, fieldValues map[locmafID][]byte) (mp4.Box, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	box, err := decodeBox(value)
	if err != nil {
		return nil, true, fmt.Errorf("invalid locmaf id=%d: %w", id, err)
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

func uint32ToFourCC(value uint32) string {
	return string([]byte{
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	})
}

func setFieldBox(fields map[locmafID][]byte, key locmafID, box mp4.Box) error {
	if box == nil {
		return nil
	}
	encoded, err := encodeBox(box)
	if err != nil {
		return err
	}
	fields[key] = encoded
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
	payloadLen := uint64(len(payload))
	withSize := make([]byte, 0, quicvarint.Len(payloadLen)+len(payload))
	withSize = appendVarint(withSize, payloadLen)
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

func shouldCreateEmptySenc(moov *mp4.MoovBox, trackID uint32, perSampleIVSize uint8) bool {
	if moov == nil || perSampleIVSize != 0 {
		return false
	}
	sinf := moov.GetSinf(trackID)
	if sinf == nil || sinf.Schi == nil || sinf.Schi.Tenc == nil {
		return false
	}
	tenc := sinf.Schi.Tenc
	return tenc.DefaultIsProtected == 1 && len(tenc.DefaultConstantIV) > 0
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

func repeatUint64(value uint64, count int) []uint64 {
	values := make([]uint64, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func reconstructSencFromFields(fieldValues map[locmafID][]byte, sampleCount int,
	perSampleIVSize uint8, createEmpty bool) (*mp4.SencBox, error) {
	ivsPayload, hasIVs := fieldValues[moofLocmafIDs.initializationVector]
	subSampleCounts, hasSubSampleCounts, err := readVarintList(moofLocmafIDs.subsampleCount, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfClearData, hasBytesOfClearData, err := readVarintList(moofLocmafIDs.bytesOfClearData, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfProtectedData, hasBytesOfProtectedData, err := readVarintList(moofLocmafIDs.bytesOfProtectedData, fieldValues)
	if err != nil {
		return nil, err
	}

	if !createEmpty && !hasIVs && !hasSubSampleCounts && !hasBytesOfClearData && !hasBytesOfProtectedData {
		return nil, nil
	}

	senc := mp4.CreateSencBox()

	if hasIVs {
		if perSampleIVSize == 0 {
			return nil, fmt.Errorf("locmaf id=%d present but per-sample IV size is 0", moofLocmafIDs.initializationVector)
		}
		if len(ivsPayload)%int(perSampleIVSize) != 0 {
			return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.initializationVector)
		}
		if len(ivsPayload)/int(perSampleIVSize) != sampleCount {
			return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.initializationVector)
		}
		senc.SetPerSampleIVSize(perSampleIVSize)
	}

	if hasSubSampleCounts && len(subSampleCounts) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.subsampleCount)
	}

	totalSubsamples := 0
	if hasSubSampleCounts {
		for _, count := range subSampleCounts {
			totalSubsamples += int(count)
		}
	}

	if hasBytesOfClearData && len(bytesOfClearData) != totalSubsamples {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.bytesOfClearData)
	}
	if hasBytesOfProtectedData && len(bytesOfProtectedData) != totalSubsamples {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofLocmafIDs.bytesOfProtectedData)
	}
	if (hasBytesOfClearData || hasBytesOfProtectedData) && !hasSubSampleCounts {
		return nil, fmt.Errorf("missing locmaf id=%d", moofLocmafIDs.subsampleCount)
	}

	for i := range sampleCount {
		var sampleIV []byte
		if hasIVs {
			ivSize := int(perSampleIVSize)
			sampleIV = append([]byte(nil), ivsPayload[:ivSize]...)
			ivsPayload = ivsPayload[ivSize:]
		}

		var subsamples []mp4.SubSamplePattern
		if hasSubSampleCounts {
			subsampleCount := int(subSampleCounts[i])
			subsamples = make([]mp4.SubSamplePattern, subsampleCount)
			for j := range subsampleCount {
				clearData := bytesOfClearData[j]
				protectedData := bytesOfProtectedData[j]
				if clearData > 0xffff {
					return nil, fmt.Errorf("invalid locmaf id=%d", moofLocmafIDs.bytesOfClearData)
				}
				if protectedData > 0xffffffff {
					return nil, fmt.Errorf("invalid locmaf id=%d", moofLocmafIDs.bytesOfProtectedData)
				}
				subsamples[j] = mp4.SubSamplePattern{
					BytesOfClearData:     uint16(clearData),
					BytesOfProtectedData: uint32(protectedData),
				}
			}
		}

		if err := senc.AddSample(mp4.SencSample{IV: sampleIV, SubSamples: subsamples}); err != nil {
			return nil, fmt.Errorf("unable to reconstruct senc: %w", err)
		}
	}

	return senc, nil
}

func appendVarint(payload []byte, value uint64) []byte {
	return quicvarint.Append(payload, value)
}

// appendSignedVarint uses zigzag-scanning to convert a signed int to a varint encoding.
// 0 -> 0,
// -1 -> 1,
// 1 -> 2,
// -2 -> 3,
// 2 -> 4,
func appendSignedVarint(payload []byte, value int64) []byte {
	encoded := uint64(value) << 1
	if value < 0 {
		encoded = ^encoded
	}
	return quicvarint.Append(payload, encoded)
}

// appendSignedVarint converts a (zigzag-scanned) varint encoding to an int.
// 0 -> 0,
// 1 -> -1,
// 2 -> 1,
// 3 -> -2,
// 4 -> 2,
func parseSignedVarint(value []byte) (int64, int, error) {
	encoded, deltaPos, err := quicvarint.Parse(value)
	if err != nil {
		return 0, 0, err
	}
	decoded := int64(encoded >> 1)
	if encoded&1 != 0 {
		decoded = ^decoded
	}
	return decoded, deltaPos, nil
}

// readVarint reads a single varint from the fieldValues map with the specified locmafID.
// Returned values are: the uint64-encoded varint and a bool representing
// if the map contained a field with the specified locmafID.
func readVarint(id locmafID, fieldValues map[locmafID][]byte) (uint64, bool) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false
	}
	varint, _, _ := quicvarint.Parse(value)
	return varint, true
}

// readVarintList reads a sequence of varints from the fieldValues map with the specified locmafID.
// Returned values are: the uint64-encoded varint array and a bool representing
// if the map contained a field with the specified locmafID.
func readVarintList(id locmafID, fieldValues map[locmafID][]byte) ([]uint64, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	var varintList []uint64
	pos := 0
	for pos < len(value) {
		varint, deltaPos, err := quicvarint.Parse(value[pos:])
		if err != nil {
			return nil, true, fmt.Errorf("invalid locmaf id=%d", id)
		}
		varintList = append(varintList, varint)
		pos += deltaPos
	}
	return varintList, true, nil
}

// readSignedVarintList reads a sequence of signed varints from the fieldValues map with the specified locmafID.
// Returned values are: the uint64-encoded varint array and a bool representing
// if the map contained a field with the specified locmafID.
func readSignedVarintList(id locmafID, fieldValues map[locmafID][]byte) ([]int64, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return nil, false, nil
	}
	var varintList []int64
	pos := 0
	for pos < len(value) {
		varint, deltaPos, err := parseSignedVarint(value[pos:])
		if err != nil {
			return nil, true, fmt.Errorf("invalid locmaf id=%d", id)
		}
		varintList = append(varintList, varint)
		pos += deltaPos
	}
	return varintList, true, nil
}
