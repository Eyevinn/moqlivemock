package pub

import (
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

// TestLocationInFetchRange verifies the catalog FETCH range check: the catalog's
// single object at {group:0, object:0} is served only for a range that covers
// it, and not for any other group or object.
func TestLocationInFetchRange(t *testing.T) {
	loc := func(g, o uint64) moqtransport.Location { return moqtransport.Location{Group: g, Object: o} }
	catalog := loc(0, 0)

	cases := []struct {
		name        string
		start, end  moqtransport.Location
		wantCatalog bool
	}{
		{"relative joining offset 0", loc(0, 0), loc(0, 1), true}, // [{0,0},{0,1})
		{"standalone whole group 0", loc(0, 0), loc(0, 0), true},  // EndObject 0 = whole group
		{"standalone up to later group", loc(0, 0), loc(5, 0), true},
		{"start at object 1", loc(0, 1), loc(0, 5), false},
		{"start at group 5", loc(5, 0), loc(5, 1), false},
		{"end before catalog (group 0 excluded)", loc(0, 0), loc(0, 0), true}, // group 0 always included when start {0,0}
	}
	for _, c := range cases {
		assert.Equal(t, c.wantCatalog, locationInFetchRange(catalog, c.start, c.end), c.name)
	}

	// A non-catalog object (e.g. {5,0}) is only in range when explicitly requested.
	assert.False(t, locationInFetchRange(loc(5, 0), loc(0, 0), loc(0, 1)), "object {5,0} not in catalog range")
	assert.True(t, locationInFetchRange(loc(5, 0), loc(5, 0), loc(5, 1)), "object {5,0} in {5,0}-{5,1}")
}
