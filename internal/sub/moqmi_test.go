package sub

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsMoqMINamespace(t *testing.T) {
	tests := []struct {
		name string
		ns   []string
		want bool
	}{
		{"exact moq-mi", []string{"moq-mi"}, true},
		{"prefixed first segment", []string{"moq-mi/live", "sub"}, true},
		{"not moq-mi", []string{"cmsf", "clear"}, false},
		{"empty namespace", nil, false},
		{"loc is not moq-mi", []string{"loc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsMoqMINamespace(tt.ns))
		})
	}
}

func TestSeqDelta(t *testing.T) {
	var last uint64
	var have bool

	// First sample — returns 0 and initialises state.
	assert.Equal(t, uint64(0), seqDelta(10, &last, &have))
	assert.True(t, have)
	assert.Equal(t, uint64(10), last)

	// Consecutive: delta 1.
	assert.Equal(t, uint64(1), seqDelta(11, &last, &have))
	assert.Equal(t, uint64(11), last)

	// Gap of 4 missing objects: delta 5.
	assert.Equal(t, uint64(5), seqDelta(16, &last, &have))
	assert.Equal(t, uint64(16), last)

	// Out-of-order / duplicate (delta wraps via uint64 subtraction): the
	// helper does not guard against this but we verify last is updated.
	_ = seqDelta(15, &last, &have)
	assert.Equal(t, uint64(15), last)
}
