package internal_test

import (
	"io"
	"maps"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/relay"
	"github.com/Eyevinn/moqlivemock/internal/sub"
	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

// objectUrgencies returns the distinct urgencies c gave the streams it opened,
// leaving out the control and request tiers above the Objects.
func objectUrgencies(c *testconn.Conn) []int8 {
	seen := make(map[int8]bool)
	for _, p := range c.Priorities() {
		if p.Urgency >= moqtransport.UrgencyObjectHighest {
			seen[p.Urgency] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// TestPublisherPriorities subscribes to one kind of track at a time, directly
// and through an mlmrel relay, and checks the urgency that mlmpub and the
// relay each give their data streams.
//
// The catalog is taken by SUBSCRIBE. A joining FETCH would add a stream at the
// default urgency to every case, since a FETCH response is scheduled on the
// request rather than on the objects in it.
func TestPublisherPriorities(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	const (
		catalogUrgency = moqtransport.UrgencyObjectHighest     // CatalogPriority 0
		audioUrgency   = moqtransport.UrgencyObjectHighest + 1 // AudioPriority and SubtitlePriority 64
		videoUrgency   = moqtransport.UrgencyObjectHighest + 2 // VideoPriority 128
	)
	cases := []struct {
		name               string
		video, audio, subs string // sub.Handler track selection; "NONE" selects none
		out                string
		want               []int8 // every case also carries the catalog
	}{
		{name: "catalog", video: "NONE", audio: "NONE", out: "catalog",
			want: []int8{catalogUrgency}},
		{name: "audio", video: "NONE", audio: "_aac", out: "audio",
			want: []int8{catalogUrgency, audioUrgency}},
		{name: "subtitles", video: "NONE", audio: "NONE", subs: "wvtt", out: "subs",
			want: []int8{catalogUrgency, audioUrgency}},
		{name: "video", video: "_avc", audio: "NONE", out: "video",
			want: []int8{catalogUrgency, videoUrgency}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				newSub := func(out io.Writer) *sub.Handler {
					return &sub.Handler{
						Namespace:   testNamespace,
						Outs:        map[string]io.Writer{tc.out: out},
						Logfh:       io.Discard,
						VideoName:   tc.video,
						AudioName:   tc.audio,
						SubsName:    tc.subs,
						CatalogMode: "subscribe",
					}
				}
				ph := newPubHandler(asset, catalog)

				directServer, directClient := testconn.Pair()
				go ph.Handle(t.Context(), directServer)
				directOut := newSyncBuffer()
				go func() { _ = newSub(directOut).RunWithConn(t.Context(), directClient) }()

				upServer, upClient := testconn.Pair()
				go ph.Handle(t.Context(), upServer)
				rh := relay.NewHandler(io.Discard)
				go rh.HandleUpstream(t.Context(), upClient)
				synctest.Wait() // the upstream's namespaces land in the relay's table

				downServer, downClient := testconn.Pair()
				go rh.Handle(t.Context(), downServer)
				relayOut := newSyncBuffer()
				go func() { _ = newSub(relayOut).RunWithConn(t.Context(), downClient) }()

				// The first output is the init data from the catalog, and the
				// media starts at the next group boundary. Two groups on, every
				// subscribed track has opened a stream, and a stream is given
				// its priority when it is opened.
				directOut.WaitForLen(1)
				relayOut.WaitForLen(1)
				time.Sleep(2 * internal.MoqGroupDurMS * time.Millisecond)
				synctest.Wait()

				assert.Equal(t, tc.want, objectUrgencies(directServer), "mlmpub")
				assert.Equal(t, tc.want, objectUrgencies(upServer), "mlmpub toward the relay")
				assert.Equal(t, tc.want, objectUrgencies(downServer), "relay")

				for _, c := range []*testconn.Conn{
					directServer, directClient, upServer, upClient, downServer, downClient,
				} {
					_ = c.CloseWithError(0, "")
				}
				synctest.Wait()
			})
		})
	}
}
