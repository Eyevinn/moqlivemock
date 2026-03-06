package internal

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	CaptureTimestamp  = 2
	VideoConfig       = 13
	VideoFrameMarking = 4
	AudioLevel        = 6
	MoofHeader        = 8

	//tfhd
	Sample_description_index = 9
	Default_sample_duration  = 10
	Default_sample_size      = 11
	Default_sample_flags     = 12

	//tfdt
	Base_media_decode_time = 99

	//trun
	Sample_count                    = 13
	First_sample_flags              = 14
	Sample_sizes                    = 15
	Sample_durations                = 16
	Sample_composition_time_offsets = 17
	Sample_flags                    = 18

	//senc
	Initialization_vector = 19
	Submsample_count      = 20
	Subsamples            = 21
)

func CompressMoof(frag *mp4.Fragment) ([]byte, error) {
	importantFields, err := extractImportantFields(frag)
	if err != nil {
		return nil, fmt.Errorf("unable to extract important moof fields: %w", err)
	}
	locHeader := make([]byte, 0)
	locHeader = binary.AppendVarint(locHeader, int64(MoofHeader))
	// var headerSize uint32 = 0
	// for _, value := range importantFields {
	// 	headerSize += uint32(len(value))
	// }
	// locHeader = binary.BigEndian.AppendUint32(locHeader, headerSize)
	keys := make([]int, 0, len(importantFields))
	for key := range importantFields {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	for key := range importantFields {
		value := importantFields[key]
		locHeader = binary.AppendVarint(locHeader, int64(key))
		locHeader = append(locHeader, value...)
	}
	locHeader = prependVarintSize(locHeader)
	return locHeader, nil
}

func extractImportantFields(frag *mp4.Fragment) (map[int][]byte, error) {
	importantFields := make(map[int][]byte)

	setUint32 := func(key int, value uint32) {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, value)
		importantFields[key] = b
	}
	setUint64 := func(key int, value uint64) {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, value)
		importantFields[key] = b
	}

	if frag.Moof == nil || frag.Moof.Traf == nil {
		return nil, fmt.Errorf("moof or traf not defined")
	}

	tfhd := frag.Moof.Traf.Tfhd
	if tfhd != nil {
		setUint32(Sample_description_index, tfhd.SampleDescriptionIndex)
		setUint32(Default_sample_duration, tfhd.DefaultSampleDuration)
		setUint32(Default_sample_size, tfhd.DefaultSampleSize)
		setUint32(Default_sample_flags, tfhd.DefaultSampleFlags)
	}

	tfdt := frag.Moof.Traf.Tfdt
	if tfdt != nil {
		setUint64(Base_media_decode_time, tfdt.BaseMediaDecodeTime())
	}

	trun := frag.Moof.Traf.Trun
	if trun != nil {
		setUint32(Sample_count, trun.SampleCount())
		firstSampleFlags, _ := trun.FirstSampleFlags()
		setUint32(First_sample_flags, firstSampleFlags)

		sizes := make([]byte, 0, len(trun.Samples)*4)
		durations := make([]byte, 0, len(trun.Samples)*4)
		flags := make([]byte, 0, len(trun.Samples)*4)
		compositionOffsets := make([]byte, 0, len(trun.Samples)*4)
		for _, trunSample := range trun.Samples {
			sizes = appendUint32(sizes, trunSample.Size)
			durations = appendUint32(durations, trunSample.Dur)
			flags = appendUint32(flags, trunSample.Flags)
			compositionOffsets = appendInt32(compositionOffsets, trunSample.CompositionTimeOffset)
		}
		importantFields[Sample_sizes] = prependVarintSize(sizes)
		importantFields[Sample_durations] = prependVarintSize(durations)
		importantFields[Sample_flags] = prependVarintSize(flags)
		importantFields[Sample_composition_time_offsets] = prependVarintSize(compositionOffsets)
	}

	senc := frag.Moof.Traf.Senc
	if senc != nil {
		if len(senc.IVs) > 0 {
			allIVs := make([]byte, 0)
			for _, iv := range senc.IVs {
				allIVs = append(allIVs, iv...)
			}
			importantFields[Initialization_vector] = allIVs
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
			importantFields[Submsample_count] = prependVarintSize(subSampleCounts)
			importantFields[Subsamples] = prependVarintSize(allSubsamples)
		}
	}
	return importantFields, nil
}

func DecompressMoof(data []byte, seqnum uint32) (*mp4.Fragment, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty compressed moof data")
	}
	fieldValues := separateFields(data)
	frag, err := mp4.CreateFragment(seqnum, 1)
	traf := frag.Moof.Traf
	if err != nil {
		return nil, fmt.Errorf("unable to create fragment: %w", err)
	}
	if traf.Tfhd.SampleDescriptionIndex, err = readU32(Sample_description_index, fieldValues); err != nil {
		return nil, err
	}
	if traf.Tfhd.DefaultSampleDuration, err = readU32(Default_sample_duration, fieldValues); err != nil {
		return nil, err
	}
	if traf.Tfhd.DefaultSampleSize, err = readU32(Default_sample_size, fieldValues); err != nil {
		return nil, err
	}
	if traf.Tfhd.DefaultSampleFlags, err = readU32(Default_sample_flags, fieldValues); err != nil {
		return nil, err
	}

	baseMediaDecodeTime, err := readU64(Base_media_decode_time, fieldValues)
	if err != nil {
		return nil, err
	} else {
		traf.Tfdt.SetBaseMediaDecodeTime(baseMediaDecodeTime)
	}
	firstSampleFlags, err := readU32(First_sample_flags, fieldValues)
	if err != nil {
		return nil, err
	} else {
		traf.Trun.SetFirstSampleFlags(firstSampleFlags)
	}

	sampleSizes, err := readU32List(Sample_sizes, fieldValues)
	if err != nil {
		return nil, err
	}
	sampleDurations, err := readU32List(Sample_durations, fieldValues)
	if err != nil {
		return nil, err
	}
	sampleFlags, err := readU32List(Sample_flags, fieldValues)
	if err != nil {
		return nil, err
	}
	sampleCompositionTimeOffsets, err := readInt32List(Sample_composition_time_offsets, fieldValues)
	if err != nil {
		return nil, err
	}
	for i := range sampleSizes {
		traf.Trun.AddSample(mp4.NewSample(sampleFlags[i], sampleDurations[i], sampleSizes[i], sampleCompositionTimeOffsets[i]))
	}
	return frag, nil
}

func separateFields(data []byte) map[int64][]byte {
	fieldLengths := map[int64]int{
		Sample_description_index:        4,
		Default_sample_duration:         4,
		Default_sample_size:             4,
		Default_sample_flags:            4,
		Base_media_decode_time:          8,
		Sample_count:                    4,
		First_sample_flags:              4,
		Sample_sizes:                    -1,
		Sample_durations:                -1,
		Sample_flags:                    -1,
		Sample_composition_time_offsets: -1,
		Initialization_vector:           -1,
		Submsample_count:                -1,
		Subsamples:                      -1,
	}

	fieldValues := make(map[int64][]byte)
	headerLength, pos := binary.Varint(data)
	headerEnd := pos + int(headerLength)
	// data = data[4:]
	// pos := 0
	for pos < headerEnd {
		id, deltaPos := binary.Varint(data[pos:])
		pos += deltaPos
		if fieldLengths[id] == 8 {
			value := data[pos : pos+8]
			fieldValues[id] = value
			pos += 8
		} else if fieldLengths[id] == 4 {
			value := data[pos : pos+4]
			fieldValues[id] = value
			pos += 4
		} else if fieldLengths[id] == -1 {
			fieldLength, deltaPos := binary.Varint(data[pos:])
			pos += deltaPos
			valueList := data[pos : pos+int(fieldLength)]
			valueListPos := 0
			for valueListPos < len(valueList) {
				switch id {
				case Sample_sizes, Sample_durations, Sample_flags, Sample_composition_time_offsets, Submsample_count:
					fieldValues[id] = append(fieldValues[id], valueList[valueListPos:valueListPos+4]...)
					valueListPos += 4
				case Subsamples:
					//Clear bytes
					fieldValues[id] = append(fieldValues[id], valueList[valueListPos:valueListPos+2]...)
					valueListPos += 2
					//Protected bytes
					fieldValues[id] = append(fieldValues[id], valueList[valueListPos:valueListPos+4]...)
					valueListPos += 4
				}
			}
		}
	}
	return fieldValues
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

func readU32(id int64, fieldValues map[int64][]byte) (uint32, error) {
	value, ok := fieldValues[id]
	if !ok || len(value) != 4 {
		return 0, fmt.Errorf("missing or invalid field id=%d", id)
	}
	return binary.BigEndian.Uint32(value), nil
}

func readU64(id int64, fieldValues map[int64][]byte) (uint64, error) {
	value, ok := fieldValues[id]
	if !ok || len(value) != 8 {
		return 0, fmt.Errorf("missing or invalid field id=%d", id)
	}
	return binary.BigEndian.Uint64(value), nil
}

func readU32List(id int64, fieldValues map[int64][]byte) ([]uint32, error) {
	value, ok := fieldValues[id]
	if !ok || len(value)%4 != 0 {
		return nil, fmt.Errorf("missing or invalid field id=%d", id)
	}
	uint32list := make([]uint32, 0, len(value)/4)
	for i := 0; i < len(value)/4; i++ {
		uint32list = append(uint32list, binary.BigEndian.Uint32(value[i*4:i*4+4]))
	}
	return uint32list, nil
}

func readInt32List(id int64, fieldValues map[int64][]byte) ([]int32, error) {
	u32List, err := readU32List(id, fieldValues)
	if err != nil {
		return nil, err
	}
	int32List := make([]int32, len(u32List))
	for i := range u32List {
		int32List[i] = int32(u32List[i])
	}
	return int32List, nil
}
