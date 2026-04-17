package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

const moofDeltaDeletedFields locFieldID = 17

type MoofDeltaCompressor struct {
	previous map[locFieldID][]byte
}

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

func (c *MoofDeltaCompressor) CreateMoofLOCProperty(moof *mp4.MoofBox, moov *mp4.MoovBox) ([]byte, error) {
	headerID, payload, err := c.CompressMoof(moof, moov)
	if err != nil {
		return nil, err
	}
	return createSizedLOCProperty(headerID, payload), nil
}

type MoofDeltaDecompressor struct {
	previous map[locFieldID][]byte
}

func (d *MoofDeltaDecompressor) ConvertLOCtoCMAF(loc []byte, seqnum uint32, moov *mp4.MoovBox) (*mp4.Fragment, error) {
	frag := mp4.NewFragment()
	pos := 0
	for pos < len(loc) {
		id, deltaPos := binary.Varint(loc[pos:])
		if deltaPos <= 0 {
			return nil, fmt.Errorf("invalid loc property id at offset %d", pos)
		}
		pos += deltaPos
		if id%2 == 0 {
			_, deltaPos := binary.Varint(loc[pos:])
			if deltaPos <= 0 {
				return nil, fmt.Errorf("invalid loc property value for id=%d", id)
			}
			pos += deltaPos
			continue
		}

		length, deltaPos := binary.Varint(loc[pos:])
		if deltaPos <= 0 {
			return nil, fmt.Errorf("invalid loc property length for id=%d", id)
		}
		pos += deltaPos
		if length < 0 || pos+int(length) > len(loc) {
			return nil, fmt.Errorf("loc property id=%d exceeds payload length", id)
		}

		payload := loc[pos : pos+int(length)]
		pos += int(length)

		switch id {
		case MoofHeader, MoofDeltaHeader:
			moof, err := d.DecompressMoof(id, payload, seqnum, moov)
			if err != nil {
				return nil, fmt.Errorf("unable to decompress moof: %w", err)
			}
			frag.Moof = moof
		}
	}

	return frag, nil
}

func (d *MoofDeltaDecompressor) DecompressMoof(headerID int64, data []byte, seqnum uint32, moov *mp4.MoovBox) (*mp4.MoofBox, error) {
	if len(data) == 0 && headerID != MoofDeltaHeader {
		return nil, fmt.Errorf("empty compressed moof data")
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

	return decompressMoofFields(fieldValues, seqnum, moov)
}

func cloneFieldValues(fields map[locFieldID][]byte) map[locFieldID][]byte {
	cloned := make(map[locFieldID][]byte, len(fields))
	for key, value := range fields {
		cloned[key] = append([]byte(nil), value...)
	}
	return cloned
}

func diffMoofFields(current, previous map[locFieldID][]byte) (map[locFieldID][]byte, error) {
	deltaFields := make(map[locFieldID][]byte)

	for key, currentValue := range current {
		previousValue := previous[key]
		if bytes.Equal(currentValue, previousValue) {
			continue
		}
		deltaValue, err := diffMoofFieldValue(key, currentValue, previousValue)
		if err != nil {
			return nil, fmt.Errorf("unable to delta field id=%d: %w", key, err)
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
		deltaFields[moofDeltaDeletedFields] = deletedFields
	}

	return deltaFields, nil
}

func applyMoofDelta(previous, deltaFields map[locFieldID][]byte) (map[locFieldID][]byte, error) {
	current := cloneFieldValues(previous)

	deletedFields, ok, err := readVarintList(moofDeltaDeletedFields, deltaFields)
	if err != nil {
		return nil, err
	}
	if ok {
		for _, id := range deletedFields {
			delete(current, locFieldID(id))
		}
	}

	for key, deltaValue := range deltaFields {
		if key == moofDeltaDeletedFields {
			continue
		}
		value, err := applyMoofFieldDelta(key, deltaValue, previous[key])
		if err != nil {
			return nil, fmt.Errorf("unable to apply delta field id=%d: %w", key, err)
		}
		current[key] = value
	}

	return current, nil
}

func diffMoofFieldValue(id locFieldID, current, previous []byte) ([]byte, error) {
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

func applyMoofFieldDelta(id locFieldID, delta, previous []byte) ([]byte, error) {
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

func moofDeltaValueKind(id locFieldID) moofDeltaValueType {
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
