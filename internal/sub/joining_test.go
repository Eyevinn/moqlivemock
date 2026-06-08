package sub

import (
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

// TestAfterLocation verifies the dedup predicate used by the joining-catalog
// path: objects at or before the subscription's largest location are covered by
// the joining FETCH and must be skipped on the subscription.
func TestAfterLocation(t *testing.T) {
	largest := moqtransport.Location{Group: 4, Object: 2}
	cases := []struct {
		group, object uint64
		want          bool
	}{
		{4, 2, false}, // == largest: covered by the fetch, skip
		{4, 1, false}, // earlier object, same group
		{3, 9, false}, // earlier group
		{4, 3, true},  // next object in the same group
		{5, 0, true},  // later group
	}
	for _, c := range cases {
		o := &moqtransport.Object{GroupID: c.group, ObjectID: c.object}
		assert.Equal(t, c.want, afterLocation(o, largest), "group=%d object=%d", c.group, c.object)
	}
}
