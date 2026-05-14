package internal

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
)

type locmafPropertyID int

const (
	MoovHeader      locmafPropertyID = 21
	MoofHeader      locmafPropertyID = 23
	MoofDeltaHeader locmafPropertyID = 25
)

type locmafID int

const (
	// tfhd
	moofSampleDescriptionIndex locmafID = 2
	moofDefaultSampleDuration  locmafID = 4
	moofDefaultSampleSize      locmafID = 6
	moofDefaultSampleFlags     locmafID = 8

	// tfdt
	moofBaseMediaDecodeTime locmafID = 10

	// trun
	moofFirstSampleFlags             locmafID = 12
	moofSampleCount                  locmafID = 14
	moofSampleSizes                  locmafID = 1
	moofSampleDurations              locmafID = 3
	moofSampleCompositionTimeOffsets locmafID = 5
	moofSampleFlags                  locmafID = 7

	// senc
	moofPerSampleIVSize      locmafID = 16
	moofInitializationVector locmafID = 9
	moofSubsampleCount       locmafID = 11
	moofBytesOfClearData     locmafID = 13
	moofBytesOfProtectedData locmafID = 15
)

const (
	// mvhd
	moovMovieTimescale locmafID = 2

	// tkhd
	moovTkhdFlags locmafID = 4

	// elst
	moovMediaTime locmafID = 6

	// stsd
	moovFormat locmafID = 8

	// Video
	moovColr locmafID = 1
	moovPasp locmafID = 3

	// Audio
	moovChannelCount locmafID = 10
	moovChnl         locmafID = 5

	// stsd
	moovCodecConfigurationBox locmafID = 7

	// schm
	moovSchemeType locmafID = 12

	// tenc
	moovDefaultKID             locmafID = 9
	moovDefaultConstantIV      locmafID = 11
	moovTencVersion            locmafID = 14
	moovDefaultCryptByteBlock  locmafID = 16
	moovDefaultSkipByteBlock   locmafID = 18
	moovDefaultPerSampleIVSize locmafID = 20
	moovDefaultConstantIVSize  locmafID = 22

	// trex
	moovDefaultSampleDuration locmafID = 24
	moovDefaultSampleSize     locmafID = 26
	moovDefaultSampleFlags    locmafID = 28
)

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
	trex := moov.Mvex.Trex
	// Only emit a tfhd default when the tfhd actually carries the override flag
	// AND its value differs from trex. Without the Has*() guard the zero from
	// an unset tfhd field would be written as an explicit zero, suppressing
	// the trex default on the decoder side.
	if tfhd.HasSampleDescriptionIndex() && tfhd.SampleDescriptionIndex != trex.DefaultSampleDescriptionIndex {
		importantFields[moofSampleDescriptionIndex] = appendVarint(nil, uint64(tfhd.SampleDescriptionIndex))
	}
	if tfhd.HasDefaultSampleDuration() && tfhd.DefaultSampleDuration != trex.DefaultSampleDuration {
		importantFields[moofDefaultSampleDuration] = appendVarint(nil, uint64(tfhd.DefaultSampleDuration))
	}
	if !singleSample && tfhd.HasDefaultSampleSize() && tfhd.DefaultSampleSize != trex.DefaultSampleSize {
		importantFields[moofDefaultSampleSize] = appendVarint(nil, uint64(tfhd.DefaultSampleSize))
	}
	if tfhd.HasDefaultSampleFlags() && tfhd.DefaultSampleFlags != trex.DefaultSampleFlags {
		importantFields[moofDefaultSampleFlags] = appendVarint(nil, uint64(tfhd.DefaultSampleFlags))
	}

	tfdt := moof.Traf.Tfdt
	if tfdt == nil {
		return nil, fmt.Errorf("tfdt is nil")
	}
	importantFields[moofBaseMediaDecodeTime] = appendVarint(nil, tfdt.BaseMediaDecodeTime())

	if trun == nil {
		return nil, fmt.Errorf("trun is nil")
	}
	importantFields[moofSampleCount] = appendVarint(nil, uint64(len(trun.Samples)))
	firstSampleFlags, firstSampleFlagsPresent := trun.FirstSampleFlags()
	if firstSampleFlagsPresent {
		importantFields[moofFirstSampleFlags] = appendVarint(nil, uint64(firstSampleFlags))
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
		importantFields[moofSampleDurations] = durations
	}
	if trun.HasSampleFlags() {
		importantFields[moofSampleFlags] = flags
	}
	if trun.HasSampleSize() && !singleSample {
		importantFields[moofSampleSizes] = sizes
	}
	if trun.HasSampleCompositionTimeOffset() {
		importantFields[moofSampleCompositionTimeOffsets] = compositionTimeOffsets
	}

	senc, perSampleIVSize, err := getParsedSencBox(moof, moov)
	if err != nil {
		return nil, fmt.Errorf("unable to parse senc: %w", err)
	}
	if senc != nil {
		if perSampleIVSize != getDefaultPerSampleIVSize(moov, moof.Traf.Tfhd.TrackID) {
			importantFields[moofPerSampleIVSize] = appendVarint(nil, uint64(perSampleIVSize))
		}
		if perSampleIVSize > 0 && len(senc.IVs) > 0 {
			allIVs := make([]byte, 0)
			for _, iv := range senc.IVs {
				allIVs = append(allIVs, iv...)
			}
			importantFields[moofInitializationVector] = allIVs
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
			importantFields[moofSubsampleCount] = subSampleCounts
			importantFields[moofBytesOfClearData] = bytesOfClearData
			importantFields[moofBytesOfProtectedData] = bytesOfProtectedData
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
	// LOCMAF does not carry track_id (CMAF allows only one track per moov),
	// so reuse whatever the moov advertises. Falls back to 1 if the caller
	// hasn't populated tkhd.
	trackID := uint32(1)
	if moov.Trak != nil && moov.Trak.Tkhd != nil && moov.Trak.Tkhd.TrackID != 0 {
		trackID = moov.Trak.Tkhd.TrackID
	}
	frag, err := mp4.CreateFragment(seqnum, trackID)
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

	if sampleDescriptionIndex, ok := readVarint(moofSampleDescriptionIndex, fieldValues); ok {
		traf.Tfhd.SampleDescriptionIndex = uint32(sampleDescriptionIndex)
		traf.Tfhd.Flags |= mp4.TfhdSampleDescriptionIndexPresentFlag
	}

	if defaultSampleDuration, ok := readVarint(moofDefaultSampleDuration, fieldValues); ok {
		traf.Tfhd.DefaultSampleDuration = uint32(defaultSampleDuration)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleDurationPresentFlag
	}

	defaultSampleSize, hasDefaultSampleSize := readVarint(moofDefaultSampleSize, fieldValues)
	if hasDefaultSampleSize {
		traf.Tfhd.DefaultSampleSize = uint32(defaultSampleSize)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleSizePresentFlag
	}

	if defaultSampleFlags, ok := readVarint(moofDefaultSampleFlags, fieldValues); ok {
		traf.Tfhd.DefaultSampleFlags = uint32(defaultSampleFlags)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	}

	if ivSizeValue, ok := readVarint(moofPerSampleIVSize, fieldValues); ok {
		perSampleIVSize = uint8(ivSizeValue)
	}

	baseMediaDecodeTime, ok := readVarint(moofBaseMediaDecodeTime, fieldValues)
	if !ok {
		return nil, fmt.Errorf("missing locmaf id=%d", moofBaseMediaDecodeTime)
	}
	traf.Tfdt.SetBaseMediaDecodeTime(uint64(baseMediaDecodeTime))

	if firstSampleFlags, ok := readVarint(moofFirstSampleFlags, fieldValues); ok {
		traf.Trun.SetFirstSampleFlags(uint32(firstSampleFlags))
	}
	sampleCountValue, ok := readVarint(moofSampleCount, fieldValues)
	if !ok {
		return nil, fmt.Errorf("missing locmaf id=%d", moofSampleCount)
	}
	sampleCount := int(sampleCountValue)
	sampleCompositionTimeOffsets, hasCompositionTimeOffsets, err :=
		readSignedVarintList(moofSampleCompositionTimeOffsets, fieldValues)
	if err != nil {
		return nil, err
	}
	if !hasCompositionTimeOffsets {
		sampleCompositionTimeOffsets = repeatInt64(0, sampleCount)
		traf.Trun.Flags &^= mp4.TrunSampleCompositionTimeOffsetPresentFlag
	}
	if len(sampleCompositionTimeOffsets) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofSampleCompositionTimeOffsets)
	}

	sampleSizes, ok, err := readVarintList(moofSampleSizes, fieldValues)
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

	sampleDurations, ok, err := readVarintList(moofSampleDurations, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleDurations = repeatUint64(uint64(traf.Tfhd.DefaultSampleDuration), sampleCount)
	}

	sampleFlags, ok, err := readVarintList(moofSampleFlags, fieldValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		sampleFlags = repeatUint64(uint64(traf.Tfhd.DefaultSampleFlags), sampleCount)
	}

	if len(sampleDurations) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofSampleDurations)
	}
	if len(sampleFlags) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofSampleFlags)
	}
	if len(sampleSizes) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofSampleSizes)
	}

	for i := range sampleCount {
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

	movieTimescale, ok := readVarint(moovMovieTimescale, fieldValues)
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

	tkhdFlags, ok := readVarint(moovTkhdFlags, fieldValues)
	if ok {
		trak.Tkhd.Flags = uint32(tkhdFlags)
	}

	mediaTime, ok, err := readSignedVarint(moovMediaTime, fieldValues)
	if err != nil {
		return nil, fmt.Errorf("invalid locmaf id=%d: %w", moovMediaTime, err)
	}
	if ok {
		elstEntry := ensureTrackElstEntry(trak)
		elstEntry.MediaTime = mediaTime
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

	channelCount, ok := readVarint(moovChannelCount, fieldValues)
	if ok {
		if audioEntry, ok := sampleEntry.(*mp4.AudioSampleEntryBox); ok {
			audioEntry.ChannelCount = uint16(channelCount)
		}
	}

	colrBox, ok, err := readBoxField(moovColr, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, colrBox)
	}

	codecConfigurationBox, ok, err := readBoxField(moovCodecConfigurationBox, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, codecConfigurationBox)
	}

	paspBox, ok, err := readBoxField(moovPasp, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, paspBox)
	}

	chnlBox, ok, err := readBoxField(moovChnl, fieldValues)
	if err != nil {
		return nil, err
	}
	if ok {
		setSampleEntryChildBox(sampleEntry, chnlBox)
	}

	if (sampleEntry.Type() == "encv" || sampleEntry.Type() == "enca") && getSampleEntrySinf(sampleEntry) == nil {
		addDefaultEncryptionBoxes(sampleEntry)
	}

	schemeTypeCode, ok := readVarint(moovSchemeType, fieldValues)
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

	tencVersion, ok := readVarint(moovTencVersion, fieldValues)
	if ok && tenc != nil {
		tenc.Version = byte(tencVersion)
	}

	defaultCryptByteBlock, ok := readVarint(moovDefaultCryptByteBlock, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultCryptByteBlock = uint8(defaultCryptByteBlock)
	}

	defaultSkipByteBlock, ok := readVarint(moovDefaultSkipByteBlock, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultSkipByteBlock = uint8(defaultSkipByteBlock)
	}

	if defaultKID, ok := fieldValues[moovDefaultKID]; ok {
		if tenc != nil && len(defaultKID) == 16 {
			tenc.DefaultKID = append(mp4.UUID(nil), defaultKID...)
		}
	}

	defaultPerSampleIVSize, ok := readVarint(moovDefaultPerSampleIVSize, fieldValues)
	if ok && tenc != nil {
		tenc.DefaultPerSampleIVSize = uint8(defaultPerSampleIVSize)
	}

	defaultConstantIVSize, hasConstantIVSize := readVarint(moovDefaultConstantIVSize, fieldValues)
	defaultConstantIV, hasConstantIV := fieldValues[moovDefaultConstantIV]
	if hasConstantIV && tenc != nil {
		if hasConstantIVSize && int(defaultConstantIVSize) != len(defaultConstantIV) {
			defaultConstantIV = defaultConstantIV[:0]
		}
		tenc.DefaultConstantIV = append([]byte(nil), defaultConstantIV...)
	}

	defaultSampleDuration, ok := readVarint(moovDefaultSampleDuration, fieldValues)
	if ok && moov.Mvex != nil && moov.Mvex.Trex != nil {
		moov.Mvex.Trex.DefaultSampleDuration = uint32(defaultSampleDuration)
	}

	defaultSampleSize, ok := readVarint(moovDefaultSampleSize, fieldValues)
	if ok && moov.Mvex != nil && moov.Mvex.Trex != nil {
		moov.Mvex.Trex.DefaultSampleSize = uint32(defaultSampleSize)
	}

	defaultSampleFlags, ok := readVarint(moovDefaultSampleFlags, fieldValues)
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

func createSizedLocmafProperty(headerID locmafPropertyID, payload []byte) []byte {
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
	slices.Sort(keys)

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
	headerID    locmafPropertyID
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
		headerID:    locmafPropertyID(headerID),
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
	importantFields[moovMovieTimescale] = appendVarint(nil, uint64(moov.Mvhd.Timescale))

	track := moov.Trak
	if track == nil && len(moov.Traks) > 0 {
		track = moov.Traks[0]
	}
	if track == nil || track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil ||
		track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
		return nil, fmt.Errorf("track sample description not defined")
	}
	importantFields[moovTkhdFlags] = appendVarint(nil, uint64(track.Tkhd.Flags))

	if track.Edts != nil && len(track.Edts.Elst) > 0 && len(track.Edts.Elst[0].Entries) > 0 {
		mediaTime := track.Edts.Elst[0].Entries[0].MediaTime
		importantFields[moovMediaTime] = appendSignedVarint(nil, mediaTime)
	}

	sampleEntry := track.Mdia.Minf.Stbl.Stsd.Children[0]
	format := sampleEntry.Type()
	if len(format) != 4 {
		return nil, fmt.Errorf("unable to set locmaf id=%d: expected 4-byte code, got %q", moovFormat, format)
	}
	formatCode := uint32(format[0])<<24 | uint32(format[1])<<16 | uint32(format[2])<<8 | uint32(format[3])
	importantFields[moovFormat] = appendVarint(nil, uint64(formatCode))

	switch entry := sampleEntry.(type) {
	case *mp4.VisualSampleEntryBox:
		if colr, err := findChildBoxByType(entry.Children, "colr"); err != nil {
			if err := setFieldBox(importantFields, moovColr, colr); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovColr, err)
			}
		}
		if pasp, err := findChildBoxByType(entry.Children, "pasp"); err != nil {
			if err = setFieldBox(importantFields, moovPasp, pasp); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovPasp, err)
			}
		}
	case *mp4.AudioSampleEntryBox:
		importantFields[moovChannelCount] = appendVarint(nil, uint64(entry.ChannelCount))

		if chnl, err := findChildBoxByType(entry.Children, "chnl"); err != nil {
			if err := setFieldBox(importantFields, moovChnl, chnl); err != nil {
				return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovChnl, err)
			}
		}
	}

	codecConfigurationBox, _ := getCodecConfigurationBox(sampleEntry)
	if codecConfigurationBox != nil {
		if err := setFieldBox(importantFields, moovCodecConfigurationBox, codecConfigurationBox); err != nil {
			return nil, fmt.Errorf("unable to set locmaf id=%d: %w", moovCodecConfigurationBox, err)
		}
	}

	sinf := getSampleEntrySinf(sampleEntry)
	if sinf != nil {
		if sinf.Schm != nil {
			schemeType := sinf.Schm.SchemeType
			if len(schemeType) != 4 {
				return nil, fmt.Errorf("unable to set locmaf id=%d: expected 4-byte code, got %q",
					moovSchemeType, schemeType)
			}
			schemeTypeCode := uint32(schemeType[0])<<24 | uint32(schemeType[1])<<16 |
				uint32(schemeType[2])<<8 | uint32(schemeType[3])

			importantFields[moovSchemeType] = appendVarint(nil, uint64(schemeTypeCode))
		}

		if sinf.Schi != nil && sinf.Schi.Tenc != nil {
			tenc := sinf.Schi.Tenc
			importantFields[moovTencVersion] = appendVarint(nil, uint64(tenc.Version))
			importantFields[moovDefaultCryptByteBlock] = appendVarint(nil, uint64(tenc.DefaultCryptByteBlock))
			importantFields[moovDefaultSkipByteBlock] = appendVarint(nil, uint64(tenc.DefaultSkipByteBlock))
			if len(tenc.DefaultKID) == 16 {
				importantFields[moovDefaultKID] = append([]byte(nil), tenc.DefaultKID...)
			}
			importantFields[moovDefaultPerSampleIVSize] = appendVarint(nil, uint64(tenc.DefaultPerSampleIVSize))
			if len(tenc.DefaultConstantIV) > 0 {
				importantFields[moovDefaultConstantIVSize] = appendVarint(nil, uint64(len(tenc.DefaultConstantIV)))
				importantFields[moovDefaultConstantIV] = append([]byte(nil), tenc.DefaultConstantIV...)
			}
		}
	}

	if moov.Mvex != nil && moov.Mvex.Trex != nil {
		importantFields[moovDefaultSampleDuration] = appendVarint(
			nil, uint64(moov.Mvex.Trex.DefaultSampleDuration))
		importantFields[moovDefaultSampleSize] = appendVarint(nil, uint64(moov.Mvex.Trex.DefaultSampleSize))
		importantFields[moovDefaultSampleFlags] = appendVarint(nil, uint64(moov.Mvex.Trex.DefaultSampleFlags))
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

func ensurePrimarySampleEntry(init *mp4.InitSegment,
	fieldValues map[locmafID][]byte, role string) (*mp4.TrakBox, error) {

	moov := init.Moov
	format := ""
	if formatCode, ok := readVarint(moovFormat, fieldValues); ok {
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
	switch format {
	case "stpp":
		// Namespace / schemaLocation are not currently carried in the
		// LOCMAF moov payload, so the reconstructed StppBox has empty
		// strings. Consumers that need the namespace should supply it
		// out-of-band (e.g. as a catalog-side field) — see TODO.
		return mp4.NewStppBox("", "", "")
	case "wvtt":
		return &mp4.WvttBox{}
	}
	switch mediaType {
	case "audio":
		return mp4.CreateAudioSampleEntryBox(format, 0, 0, 0, nil)
	default:
		return mp4.CreateVisualSampleEntryBox(format, 0, 0, nil)
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
	ivsPayload, hasIVs := fieldValues[moofInitializationVector]
	subSampleCounts, hasSubSampleCounts, err := readVarintList(moofSubsampleCount, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfClearData, hasBytesOfClearData, err := readVarintList(moofBytesOfClearData, fieldValues)
	if err != nil {
		return nil, err
	}
	bytesOfProtectedData, hasBytesOfProtectedData, err := readVarintList(moofBytesOfProtectedData, fieldValues)
	if err != nil {
		return nil, err
	}

	if !createEmpty && !hasIVs && !hasSubSampleCounts && !hasBytesOfClearData && !hasBytesOfProtectedData {
		return nil, nil
	}

	senc := mp4.CreateSencBox()

	if hasIVs {
		if perSampleIVSize == 0 {
			return nil, fmt.Errorf("locmaf id=%d present but per-sample IV size is 0", moofInitializationVector)
		}
		if len(ivsPayload)%int(perSampleIVSize) != 0 {
			return nil, fmt.Errorf("locmaf id=%d length mismatch", moofInitializationVector)
		}
		if len(ivsPayload)/int(perSampleIVSize) != sampleCount {
			return nil, fmt.Errorf("locmaf id=%d length mismatch", moofInitializationVector)
		}
		senc.SetPerSampleIVSize(perSampleIVSize)
	}

	if hasSubSampleCounts && len(subSampleCounts) != sampleCount {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofSubsampleCount)
	}

	totalSubsamples := 0
	if hasSubSampleCounts {
		for _, count := range subSampleCounts {
			totalSubsamples += int(count)
		}
	}

	if hasBytesOfClearData && len(bytesOfClearData) != totalSubsamples {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofBytesOfClearData)
	}
	if hasBytesOfProtectedData && len(bytesOfProtectedData) != totalSubsamples {
		return nil, fmt.Errorf("locmaf id=%d length mismatch", moofBytesOfProtectedData)
	}
	if (hasBytesOfClearData || hasBytesOfProtectedData) && !hasSubSampleCounts {
		return nil, fmt.Errorf("missing locmaf id=%d", moofSubsampleCount)
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
					return nil, fmt.Errorf("invalid locmaf id=%d", moofBytesOfClearData)
				}
				if protectedData > 0xffffffff {
					return nil, fmt.Errorf("invalid locmaf id=%d", moofBytesOfProtectedData)
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

// readSignedVarint reads a single zigzag-encoded signed varint from the
// fieldValues map.
func readSignedVarint(id locmafID, fieldValues map[locmafID][]byte) (int64, bool, error) {
	value, ok := fieldValues[id]
	if !ok {
		return 0, false, nil
	}
	decoded, _, err := parseSignedVarint(value)
	if err != nil {
		return 0, true, err
	}
	return decoded, true, nil
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

// readSignedVarintList reads a sequence of signed (zigzag-encoded) varints
// for the given id. The bool is false if the field is absent.
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
