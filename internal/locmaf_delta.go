package internal

import (
	"bytes"
	"fmt"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

const moofDeltaDeletedLocmafIDs locmafID = 17

// MoofDeltaCompressor stores the values of the fields transmitted in the previous locmaf object.
type MoofDeltaCompressor struct {
	previous map[locmafID][]byte
}

// CompressMoof compresses a moof box by converting it to locmaf format and
// transmitting the difference from the previous moof.
func (c *MoofDeltaCompressor) CompressMoof(moof *mp4.MoofBox, moov *mp4.MoovBox) (locmafPropertyID, []byte, error) {
	importantFields, err := extractImportantMoofFields(moof, moov)
	if err != nil {
		return 0, nil, fmt.Errorf("unable to extract important moof fields: %w", err)
	}

	if c.previous == nil {
		c.previous = cloneFieldValues(importantFields)
		return MoofHeader, encodeFields(importantFields), nil
	}

	deltaFields, err := diffMoofFields(importantFields, c.previous)
	if err != nil {
		return 0, nil, err
	}
	c.previous = cloneFieldValues(importantFields)
	return MoofDeltaHeader, encodeFields(deltaFields), nil
}

// CreateMoofProperty creates a delta moof property by compressing the moof box.
func (c *MoofDeltaCompressor) CreateMoofProperty(moof *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	headerID, payload, err := c.CompressMoof(moof, moov)
	if err != nil {
		return nil, err
	}
	return createSizedLocmafProperty(headerID, payload), nil
}

// MoofDeltaDecompressor stores the values of the fields transmitted in the previous locmaf object.
type MoofDeltaDecompressor struct {
	previous map[locmafID][]byte
}

// DecompressMoof decompresses a locmaf object to create a moof box.
// Both moof, and delta moof properties are accepted.
func (d *MoofDeltaDecompressor) DecompressMoof(data []byte,
	seqnum uint32, moov *mp4.MoovBox) (*mp4.MoofBox, []byte, error) {
	object, err := parseLocmafObject(data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse LOCMAF object: %w", err)
	}
	moof, err := d.decompressMoofProperty(object.headerID, object.properties, seqnum, moov, len(object.mdatPayload))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decompress moof property: %w", err)
	}
	return moof, object.mdatPayload, nil
}

// decompressMoofProptery converts a moof or delta moof property to a moof box.
func (d *MoofDeltaDecompressor) decompressMoofProperty(headerID locmafPropertyID, data []byte,
	seqnum uint32, moov *mp4.MoovBox, mdatPayloadLength int) (*mp4.MoofBox, error) {
	if len(data) == 0 && headerID != MoofDeltaHeader {
		return nil, fmt.Errorf("empty locmaf moof data")
	}

	fieldValues, err := separateFields(data)
	if err != nil {
		return nil, err
	}

	switch headerID {
	case MoofHeader:
		d.previous = cloneFieldValues(fieldValues)
	case MoofDeltaHeader:
		if d.previous == nil {
			return nil, fmt.Errorf("missing previous moof state for delta moof")
		}
		fieldValues, err = applyMoofDelta(d.previous, fieldValues, moov)
		if err != nil {
			return nil, err
		}
		d.previous = cloneFieldValues(fieldValues)
	default:
		return nil, fmt.Errorf("unsupported moof header id=%d", headerID)
	}

	return decompressMoofUsingFieldValues(fieldValues, seqnum, moov, mdatPayloadLength)
}

func cloneFieldValues(fields map[locmafID][]byte) map[locmafID][]byte {
	cloned := make(map[locmafID][]byte, len(fields))
	for key, value := range fields {
		cloned[key] = append([]byte(nil), value...)
	}
	return cloned
}

// diffMoofFields returns the difference in each field value
// as a map if the field value is not equal to the previous value.
func diffMoofFields(current, previous map[locmafID][]byte) (map[locmafID][]byte, error) {
	deltaFields := make(map[locmafID][]byte)

	for key, currentValue := range current {
		if key == moofBaseMediaDecodeTime {
			continue
		}
		previousValue := previous[key]
		if bytes.Equal(currentValue, previousValue) {
			continue
		}
		deltaValue, err := diffMoofFieldValue(key, currentValue, previousValue)
		if err != nil {
			return nil, fmt.Errorf("unable to delta locmaf id=%d: %w", key, err)
		}
		deltaFields[key] = deltaValue
	}

	var deletedFields []byte
	for key := range previous {
		if _, ok := current[key]; !ok {
			deletedFields = appendVarint(deletedFields, uint64(key))
		}
	}
	if len(deletedFields) > 0 {
		deltaFields[moofDeltaDeletedLocmafIDs] = deletedFields
	}

	return deltaFields, nil
}

// applyMoofDelta creates a current field value map by looking at the previous map, and the current delta map.
func applyMoofDelta(previous, deltaFields map[locmafID][]byte, moov *mp4.MoovBox) (map[locmafID][]byte, error) {
	current := cloneFieldValues(previous)
	baseMediaDecodeTime, err := deriveNextBaseMediaDecodeTime(previous, moov)
	if err != nil {
		return nil, err
	}
	current[moofBaseMediaDecodeTime] = appendVarint(nil, baseMediaDecodeTime)

	deletedFields, ok, err := readVarintList(moofDeltaDeletedLocmafIDs, deltaFields)
	if err != nil {
		return nil, err
	}
	if ok {
		for _, id := range deletedFields {
			delete(current, locmafID(id))
		}
	}

	for key, deltaValue := range deltaFields {
		if key == moofDeltaDeletedLocmafIDs {
			continue
		}
		value, err := applyMoofFieldDelta(key, deltaValue, previous[key])
		if err != nil {
			return nil, fmt.Errorf("unable to apply delta locmaf id=%d: %w", key, err)
		}
		current[key] = value
	}

	return current, nil
}

func deriveNextBaseMediaDecodeTime(previous map[locmafID][]byte, moov *mp4.MoovBox) (uint64, error) {
	baseMediaDecodeTime, ok := readVarint(moofBaseMediaDecodeTime, previous)
	if !ok {
		return 0, fmt.Errorf("missing previous locmaf id=%d", moofBaseMediaDecodeTime)
	}

	previousDuration, err := moofFieldDuration(previous, moov)
	if err != nil {
		return 0, err
	}
	if baseMediaDecodeTime > ^uint64(0)-previousDuration {
		return 0, fmt.Errorf("derived base media decode time overflows uint64")
	}
	return baseMediaDecodeTime + previousDuration, nil
}

func moofFieldDuration(fields map[locmafID][]byte, moov *mp4.MoovBox) (uint64, error) {
	sampleCount, ok := readVarint(moofSampleCount, fields)
	if !ok {
		return 0, fmt.Errorf("missing previous locmaf id=%d", moofSampleCount)
	}

	sampleDurations, ok, err := readVarintList(moofSampleDurations, fields)
	if err != nil {
		return 0, err
	}
	if ok {
		if uint64(len(sampleDurations)) != sampleCount {
			return 0, fmt.Errorf("locmaf id=%d length mismatch", moofSampleDurations)
		}
		var duration uint64
		for _, sampleDuration := range sampleDurations {
			duration += sampleDuration
		}
		return duration, nil
	}

	defaultSampleDuration, ok := readVarint(moofDefaultSampleDuration, fields)
	if !ok {
		if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
			return 0, fmt.Errorf("moov or trex not defined")
		}
		defaultSampleDuration = uint64(moov.Mvex.Trex.DefaultSampleDuration)
	}
	return defaultSampleDuration * sampleCount, nil
}

// diffMoofFieldValue returns the difference between the current and previous field value.
func diffMoofFieldValue(id locmafID, current, previous []byte) ([]byte, error) {
	switch moofDeltaValueKind(id) {
	case moofDeltaScalar:
		currentValue, err := readSingleMoofFieldValue(id, current)
		if err != nil {
			return nil, err
		}
		var previousValue int64
		if len(previous) > 0 {
			previousValue, err = readSingleMoofFieldValue(id, previous)
			if err != nil {
				return nil, err
			}
		}
		return appendSignedVarint(nil, currentValue-previousValue), nil
	case moofDeltaVarintList:
		currentValues, err := readMoofFieldValues(id, current)
		if err != nil {
			return nil, err
		}
		previousValues, err := readMoofFieldValues(id, previous)
		if err != nil {
			return nil, err
		}
		var delta []byte
		for i, currentValue := range currentValues {
			var previousValue int64
			if i < len(previousValues) {
				previousValue = previousValues[i]
			}
			delta = appendSignedVarint(delta, currentValue-previousValue)
		}
		return delta, nil
	case moofDeltaRawBytes:
		return append([]byte(nil), current...), nil
	default:
		return nil, fmt.Errorf("unknown delta value kind")
	}
}

// applyMoofFieldDelta returns the current value by summing the delta and previous field value.
func applyMoofFieldDelta(id locmafID, delta, previous []byte) ([]byte, error) {
	switch moofDeltaValueKind(id) {
	case moofDeltaScalar:
		deltaValue, err := readSingleSignedDeltaValue(id, delta)
		if err != nil {
			return nil, err
		}
		var previousValue int64
		if len(previous) > 0 {
			previousValue, err = readSingleMoofFieldValue(id, previous)
			if err != nil {
				return nil, err
			}
		}
		return appendLocmafFieldValue(id, nil, previousValue+deltaValue)
	case moofDeltaVarintList:
		deltaValues, err := readSignedDeltaValues(id, delta)
		if err != nil {
			return nil, err
		}
		previousValues, err := readMoofFieldValues(id, previous)
		if err != nil {
			return nil, err
		}
		var current []byte
		for i, deltaValue := range deltaValues {
			var previousValue int64
			if i < len(previousValues) {
				previousValue = previousValues[i]
			}
			current, err = appendLocmafFieldValue(id, current, previousValue+deltaValue)
			if err != nil {
				return nil, err
			}
		}
		return current, nil
	case moofDeltaRawBytes:
		return append([]byte(nil), delta...), nil
	default:
		return nil, fmt.Errorf("unknown delta value kind")
	}
}

type moofDeltaValueType int

const (
	moofDeltaScalar moofDeltaValueType = iota
	moofDeltaVarintList
	moofDeltaRawBytes
)

func moofDeltaValueKind(id locmafID) moofDeltaValueType {
	if id == moofInitializationVector {
		return moofDeltaRawBytes
	}
	if id%2 == 1 {
		return moofDeltaVarintList
	}
	return moofDeltaScalar
}

func readSingleMoofFieldValue(id locmafID, value []byte) (int64, error) {
	values, err := readMoofFieldValues(id, value)
	if err != nil {
		return 0, err
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("expected single varint, got %d", len(values))
	}
	return values[0], nil
}

func readMoofFieldValues(id locmafID, value []byte) ([]int64, error) {
	if id == moofSampleCompositionTimeOffsets {
		values, _, err := readSignedVarintList(id, map[locmafID][]byte{id: value})
		return values, err
	}

	unsignedValues, _, err := readVarintList(id, map[locmafID][]byte{id: value})
	if err != nil {
		return nil, err
	}
	values := make([]int64, 0, len(unsignedValues))
	for _, value := range unsignedValues {
		if value > math.MaxInt64 {
			return nil, fmt.Errorf("varint overflows int64")
		}
		values = append(values, int64(value))
	}
	return values, nil
}

func readSingleSignedDeltaValue(id locmafID, value []byte) (int64, error) {
	values, err := readSignedDeltaValues(id, value)
	if err != nil {
		return 0, err
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("expected single delta varint, got %d", len(values))
	}
	return values[0], nil
}

func readSignedDeltaValues(id locmafID, value []byte) ([]int64, error) {
	values, _, err := readSignedVarintList(id, map[locmafID][]byte{id: value})
	return values, err
}

// appendLocmafFieldValue writes a reconstructed full locmaf field, not a delta field.
// Delta fields are always signed varints, while full moof fields keep the encoding
// used by CompressMoof and decompressMoofUsingFieldValues.
func appendLocmafFieldValue(id locmafID, payload []byte, value int64) ([]byte, error) {
	if id == moofSampleCompositionTimeOffsets {
		return appendSignedVarint(payload, value), nil
	}
	if value < 0 {
		return nil, fmt.Errorf("negative value for unsigned locmaf field")
	}
	return appendVarint(payload, uint64(value)), nil
}
