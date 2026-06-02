package locmafv02

import (
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
)

// appendVarint appends a MOQT (QUIC) unsigned varint.
func appendVarint(payload []byte, value uint64) []byte {
	return quicvarint.Append(payload, value)
}

// appendZigzag appends a zigzag-encoded signed varint (draft §12.4).
//
//	encode: z = (n << 1) ^ (n >> 63)
//
// In Go we use the form below so the negation is explicit and the
// bit-cast away from arithmetic-shift behaviour is auditable. The two
// forms are equivalent for all int64 inputs.
func appendZigzag(payload []byte, value int64) []byte {
	encoded := uint64(value) << 1
	if value < 0 {
		encoded = ^encoded
	}
	return quicvarint.Append(payload, encoded)
}

// parseVarint reads a single unsigned varint.
func parseVarint(data []byte) (uint64, int, error) {
	v, n, err := quicvarint.Parse(data)
	if err != nil {
		return 0, 0, err
	}
	return v, n, nil
}

// parseZigzag reads a single zigzag-encoded signed varint.
//
//	decode: n = (z >> 1) ^ -(z & 1)
func parseZigzag(data []byte) (int64, int, error) {
	z, n, err := quicvarint.Parse(data)
	if err != nil {
		return 0, 0, err
	}
	dec := int64(z >> 1)
	if z&1 != 0 {
		dec = ^dec
	}
	return dec, n, nil
}

// parseVarintList reads a sequence of unsigned varints until value
// is exhausted.
func parseVarintList(value []byte) ([]uint64, error) {
	var out []uint64
	pos := 0
	for pos < len(value) {
		v, n, err := parseVarint(value[pos:])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		pos += n
	}
	return out, nil
}

// parseZigzagList reads zigzag-encoded signed varints until value
// is exhausted.
func parseZigzagList(value []byte) ([]int64, error) {
	var out []int64
	pos := 0
	for pos < len(value) {
		v, n, err := parseZigzag(value[pos:])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		pos += n
	}
	return out, nil
}

// errShort is returned when a length-prefixed value claims more bytes
// than remain in the buffer.
func errShort(id fieldID) error {
	return fmt.Errorf("locmafv02 id=%d exceeds payload length", id)
}
