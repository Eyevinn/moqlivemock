package relay

import (
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

// drain takes everything queued for s, as its writer would, and names it.
func drain(s *subscriber) []string {
	var got []string
	for len(s.queue) > 0 {
		item := <-s.queue
		s.take(item)
		if item.resync {
			got = append(got, "resync")
			continue
		}
		got = append(got, moqtransport.Location{Group: item.obj.GroupID, Object: item.obj.ObjectID}.String())
	}
	return got
}

func loc(group, object uint64) string {
	return moqtransport.Location{Group: group, Object: object}.String()
}

// A subscriber that resyncs at a group start takes nothing more from earlier
// groups: their objects can still arrive on other streams after the new
// group's first object, and would reach it as a partial group.
func TestSubscriberResyncDropsEarlierGroups(t *testing.T) {
	s := newSubscriber(nil, 8, 0)
	s.behind, s.behindGroup = true, 1

	s.enqueue(&moqtransport.Object{GroupID: 1, ObjectID: 3})
	s.enqueue(&moqtransport.Object{GroupID: 2, ObjectID: 0})
	s.enqueue(&moqtransport.Object{GroupID: 1, ObjectID: 4})
	s.enqueue(&moqtransport.Object{GroupID: 1, SubgroupID: 0, EndsSubgroup: true})
	s.enqueue(&moqtransport.Object{GroupID: 2, ObjectID: 1})

	require.Equal(t, []string{"resync", loc(2, 0), loc(2, 1)}, drain(s))
}

// The byte cap puts a subscriber behind like a full queue does, however many
// slots are left: large objects cannot pile up in a queue sized for small
// ones. It resyncs at the first group start that fits once the writer has
// caught up.
func TestSubscriberQueueBytes(t *testing.T) {
	s := newSubscriber(nil, 64, 3*cost(100))

	for i := range uint64(4) {
		s.enqueue(payloadObject(1, i, 100))
	}
	require.True(t, s.behind, "the fourth object is over the cap")
	require.Equal(t, 3*cost(100), s.queuedBytes.Load())

	// Group 2 starts while the queue is still full: no room to resync.
	s.enqueue(payloadObject(2, 0, 100))
	s.enqueue(payloadObject(2, 1, 100))
	require.True(t, s.behind)

	require.Equal(t, []string{loc(1, 0), loc(1, 1), loc(1, 2)}, drain(s))
	require.Zero(t, s.queuedBytes.Load())

	s.enqueue(payloadObject(2, 2, 100)) // not a group start
	s.enqueue(payloadObject(3, 0, 100))
	s.enqueue(payloadObject(3, 1, 100))
	require.False(t, s.behind)
	require.Equal(t, []string{"resync", loc(3, 0), loc(3, 1)}, drain(s))
	require.Zero(t, s.queuedBytes.Load())
}

// An object larger than the whole cap still reaches a subscriber whose queue
// is empty; otherwise a track of such objects could never be forwarded.
func TestSubscriberQueueTakesOversizedObject(t *testing.T) {
	s := newSubscriber(nil, 64, cost(100))

	s.enqueue(payloadObject(1, 0, 1000))
	require.False(t, s.behind)
	s.enqueue(payloadObject(1, 1, 10))
	require.True(t, s.behind, "nothing more fits behind the oversized object")

	require.Equal(t, []string{loc(1, 0)}, drain(s))
	s.enqueue(payloadObject(2, 0, 1000))
	require.Equal(t, []string{"resync", loc(2, 0)}, drain(s))
}
