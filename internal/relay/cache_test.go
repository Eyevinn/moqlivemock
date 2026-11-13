package relay

import (
	"sync/atomic"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

func payloadObject(group, object uint64, size int) *moqtransport.Object {
	return &moqtransport.Object{GroupID: group, ObjectID: object, Payload: make([]byte, size)}
}

// cost is what an object of the given payload size counts against a budget.
func cost(size int) int64 { return int64(objectOverhead + size) }

// A group that never ends is the case of #172: one publisher completing
// subgroup after subgroup in one group. Its cache must stop at the track
// budget, after first giving up the track's older groups.
func TestGroupCacheTrackBudget(t *testing.T) {
	var total atomic.Int64
	c := newGroupCache(3, 10*cost(100), 0, &total)

	for i := range uint64(4) {
		require.False(t, c.add(payloadObject(1, i, 100)))
	}
	for i := range uint64(6) {
		require.False(t, c.add(payloadObject(2, i, 100)))
	}
	require.Equal(t, 10*cost(100), c.bytes)

	// Group 2 grows past the budget: group 1 goes first.
	require.False(t, c.add(payloadObject(2, 6, 100)))
	require.NotContains(t, c.groups, uint64(1))
	require.Equal(t, 7*cost(100), c.bytes)

	// With nothing older left, group 2 itself is dropped, once.
	for i := uint64(7); i < 10; i++ {
		require.False(t, c.add(payloadObject(2, i, 100)))
	}
	require.True(t, c.add(payloadObject(2, 10, 100)))
	require.False(t, c.add(payloadObject(2, 11, 100)))
	require.Zero(t, c.bytes)
	require.Zero(t, total.Load())

	// The next group caches as usual.
	require.False(t, c.add(payloadObject(3, 0, 100)))
	require.Equal(t, cost(100), c.bytes)
	require.Equal(t, cost(100), total.Load())
}

// Empty objects cost something too, so a flood of end-of-subgroup markers
// cannot grow a group past the budget either.
func TestGroupCacheMarkersCount(t *testing.T) {
	var total atomic.Int64
	c := newGroupCache(3, 1000*objectOverhead, 0, &total)
	dropped := false
	for i := range uint64(2000) {
		marker := &moqtransport.Object{GroupID: 1, SubgroupID: i, EndsSubgroup: true}
		dropped = c.add(marker) || dropped
		require.LessOrEqual(t, c.bytes, int64(1000*objectOverhead))
	}
	require.True(t, dropped)
}

// The relay-wide budget is shared: a track that has filled it leaves no room
// for another track's new group, and gives its bytes back when it ends.
func TestGroupCacheTotalBudget(t *testing.T) {
	var total atomic.Int64
	a := newGroupCache(3, 0, 10*cost(100), &total)
	b := newGroupCache(3, 0, 10*cost(100), &total)

	for i := range uint64(10) {
		require.False(t, a.add(payloadObject(1, i, 100)))
	}
	require.True(t, b.add(payloadObject(1, 0, 100)), "no room left for b")
	require.Equal(t, 10*cost(100), total.Load())

	a.releaseBudget()
	require.Zero(t, total.Load())
	require.False(t, b.add(payloadObject(2, 0, 100)))
	require.Equal(t, cost(100), total.Load())
}

// Evicting a group by count gives its bytes back, and an object for a group
// older than all those kept is not counted at all.
func TestGroupCacheCountEvictionReleasesBytes(t *testing.T) {
	var total atomic.Int64
	c := newGroupCache(2, 0, 0, &total)
	for g := uint64(1); g <= 3; g++ {
		require.False(t, c.add(payloadObject(g, 0, 100)))
	}
	require.Equal(t, 2*cost(100), c.bytes)
	require.Equal(t, 2*cost(100), total.Load())

	require.False(t, c.add(payloadObject(1, 1, 100)))
	require.Equal(t, 2*cost(100), c.bytes)
	require.Equal(t, []uint64{2, 3}, c.order)
}

// A dropped group is served from cache neither to a late joiner, who is told
// to skip it, nor to a FETCH.
func TestGroupCacheDroppedGroupIsNotServed(t *testing.T) {
	var total atomic.Int64
	c := newGroupCache(3, 2*cost(100), 0, &total)
	require.False(t, c.add(payloadObject(1, 0, 100)))

	objects, _, skipping := c.joinPoint()
	require.False(t, skipping)
	require.Len(t, objects, 1)

	require.False(t, c.add(payloadObject(2, 0, 100)))
	require.True(t, c.add(payloadObject(2, 1, 200)))

	objects, skip, skipping := c.joinPoint()
	require.True(t, skipping)
	require.Equal(t, uint64(2), skip)
	require.Empty(t, objects)

	_, covered := c.collectRange(moqtransport.LocationRange{
		Start: moqtransport.Location{Group: 2, Object: 0},
		End:   moqtransport.Location{Group: 2, Object: 0},
	})
	require.False(t, covered)
}
