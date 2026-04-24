package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

const moofDeltaDeletedLocmafIDs locmafID = 17

// MoofDeltaCompressor stores the values of the fields transmitted in the previous locmaf object.
type MoofDeltaCompressor struct {
	previous map[locmafID][]byte
}

// CompressMoof compresses a moof box by converting it to locmaf format and
// transmitting the difference from the previous moof.
func (c *MoofDeltaCompressor) CompressMoof(moof *mp4.MoofBox, moov *mp4.MoovBox) (int64, []byte, error) {
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

// MoofDeltaCompressor stores the values of the fields transmitted in the previous locmaf object.
type MoofDeltaDecompressor struct {
	previous map[locmafID][]byte
}

// DecompressMoof decompresses a locmaf object to create a moof box.
// Both moof, and delta moof properties are accepted.
func (d *MoofDeltaDecompressor) DecompressMoof(data []byte,
	seqnum uint32, moov *mp4.MoovBox) (*mp4.MoofBox, error) {
	object, err := parseLocmafObject(data)
	if err != nil {
		return nil, err
	}
	return d.decompressMoofProperty(object.headerID, object.properties, seqnum, moov, len(object.mdatPayload))
}

// decompressMoofProptery converts a moof or delta moof property to a moof box.
func (d *MoofDeltaDecompressor) decompressMoofProperty(headerID int64, data []byte,
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
		fieldValues, err = applyMoofDelta(d.previous, fieldValues)
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

// diffMoofFields returns the difference in each field value as a map if the field value is not equal to the previous value.
func diffMoofFields(current, previous map[locmafID][]byte) (map[locmafID][]byte, error) {
	deltaFields := make(map[locmafID][]byte)

	for key, currentValue := range current {
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
			deletedFields = binary.AppendVarint(deletedFields, int64(key))
		}
	}
	if len(deletedFields) > 0 {
		deltaFields[moofDeltaDeletedLocmafIDs] = deletedFields
	}

	return deltaFields, nil
}

// applyMoofDelta creates a current field value map by looking at the previous map, and the current delta map.
func applyMoofDelta(previous, deltaFields map[locmafID][]byte) (map[locmafID][]byte, error) {
	current := cloneFieldValues(previous)

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

//diffMoofFieldValue returns the difference between the current and previous field value.
func diffMoofFieldValue(id locmafID, current, previous []byte) ([]byte, error) {
	switch moofDeltaValueKind(id) {
	case moofDeltaScalar:
		currentValue, err := decodeSingleVarint(current)
		if err != nil {
			return nil, err
		}
		var previousValue int64
		if len(previous) > 0 {
			previousValue, err = decodeSingleVarint(previous)
			if err != nil {
				return nil, err
			}
		}
		return binary.AppendVarint(nil, currentValue-previousValue), nil
	case moofDeltaVarintList:
		currentValues, err := readVarintBytes(current)
		if err != nil {
			return nil, err
		}
		previousValues, err := readVarintBytes(previous)
		if err != nil {
			return nil, err
		}
		var delta []byte
		for i, currentValue := range currentValues {
			var previousValue int64
			if i < len(previousValues) {
				previousValue = previousValues[i]
			}
			delta = binary.AppendVarint(delta, currentValue-previousValue)
		}
		return delta, nil
	default:
		return nil, fmt.Errorf("unknown delta value kind")
	}
}

//applyMoofFieldDelta returns the current value by summing the delta and previous field value.
func applyMoofFieldDelta(id locmafID, delta, previous []byte) ([]byte, error) {
	switch moofDeltaValueKind(id) {
	case moofDeltaScalar:
		deltaValue, err := decodeSingleVarint(delta)
		if err != nil {
			return nil, err
		}
		var previousValue int64
		if len(previous) > 0 {
			previousValue, err = decodeSingleVarint(previous)
			if err != nil {
				return nil, err
			}
		}
		return binary.AppendVarint(nil, previousValue+deltaValue), nil
	case moofDeltaVarintList:
		deltaValues, err := readVarintBytes(delta)
		if err != nil {
			return nil, err
		}
		previousValues, err := readVarintBytes(previous)
		if err != nil {
			return nil, err
		}
		var current []byte
		for i, deltaValue := range deltaValues {
			var previousValue int64
			if i < len(previousValues) {
				previousValue = previousValues[i]
			}
			current = binary.AppendVarint(current, previousValue+deltaValue)
		}
		return current, nil
	default:
		return nil, fmt.Errorf("unknown delta value kind")
	}
}

type moofDeltaValueType int

const (
	moofDeltaScalar moofDeltaValueType = iota
	moofDeltaVarintList
)

func moofDeltaValueKind(id locmafID) moofDeltaValueType {
	if id%2 == 1 {
		return moofDeltaVarintList
	}
	return moofDeltaScalar
}

func decodeSingleVarint(value []byte) (int64, error) {
	varint, n := binary.Varint(value)
	if n <= 0 {
		return 0, fmt.Errorf("invalid varint")
	}
	if n != len(value) {
		return 0, fmt.Errorf("trailing bytes after varint")
	}
	return varint, nil
}

func readVarintBytes(value []byte) ([]int64, error) {
	var values []int64
	pos := 0
	for pos < len(value) {
		varint, deltaPos := binary.Varint(value[pos:])
		if deltaPos <= 0 {
			return nil, fmt.Errorf("invalid varint")
		}
		values = append(values, varint)
		pos += deltaPos
	}
	return values, nil
}
