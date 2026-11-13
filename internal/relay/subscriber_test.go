package relay

import (
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

// A subscriber that resyncs at a group start takes nothing more from earlier
// groups: their objects can still arrive on other streams after the new
// group's first object, and would reach it as a partial group.
func TestSubscriberResyncDropsEarlierGroups(t *testing.T) {
	s := newSubscriber(nil, 8)
	s.behind, s.behindGroup = true, 1

	s.enqueue(&moqtransport.Object{GroupID: 1, ObjectID: 3})
	s.enqueue(&moqtransport.Object{GroupID: 2, ObjectID: 0})
	s.enqueue(&moqtransport.Object{GroupID: 1, ObjectID: 4})
	s.enqueue(&moqtransport.Object{GroupID: 1, SubgroupID: 0, EndsSubgroup: true})
	s.enqueue(&moqtransport.Object{GroupID: 2, ObjectID: 1})

	var got []string
	for len(s.queue) > 0 {
		item := <-s.queue
		if item.resync {
			got = append(got, "resync")
			continue
		}
		got = append(got, moqtransport.Location{Group: item.obj.GroupID, Object: item.obj.ObjectID}.String())
	}
	want := []string{
		"resync",
		moqtransport.Location{Group: 2, Object: 0}.String(),
		moqtransport.Location{Group: 2, Object: 1}.String(),
	}
	require.Equal(t, want, got)
}
