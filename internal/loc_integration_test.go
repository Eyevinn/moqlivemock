package internal_test

import (
	"io"
	"testing"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/pub"
	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LOC Object Property IDs (draft-ietf-moq-loc-05 §2.3).
const (
	locPropVideoFrameMarking = 0x09
	locPropTimestamp         = 0x10
)

// TestLOCObjectProperties reads one whole group from LOC tracks of each codec
// and checks the Object Properties mlmpub puts on them: a Timestamp on every
// Object, and on video a Video Frame Marking whose I bit is set on the key frame
// only. The test assets have a one-second GOP and a LOC group is one second, so
// the key frame is Object 0.
func TestLOCObjectProperties(t *testing.T) {
	asset, err := internal.LoadAsset(testAssetDir, 1, 1)
	require.NoError(t, err)
	catalog, err := asset.GenLOCCatalogEntry(time.Now().UnixMilli())
	require.NoError(t, err)
	namespace := []string{"mlm", "msf", "clear"}

	cases := []struct {
		track string
		video bool
	}{
		{"video_400kbps_avc", true},
		{"video_400kbps_hevc", true},
		{"video_400kbps_av1", true},
		{"audio_monotonic_128kbps_aac", false},
		{"audio_monotonic_128kbps_opus", false},
	}

	forEachDraft(t, func(t *testing.T, alpn string) {
		sConn, cConn := testconn.PairALPN(alpn)
		ph := &pub.Handler{
			Namespaces: []pub.NamespaceEntry{
				{Namespace: namespace, Catalog: catalog, Packaging: "loc"},
			},
			Asset: asset,
			Logfh: io.Discard,
		}
		go ph.Handle(t.Context(), sConn)

		session := &moqtransport.Session{Implementation: "mlm-test-subscriber"}
		require.NoError(t, session.Run(t.Context(), cConn))

		for _, tc := range cases {
			ct := asset.GetTrackByName(tc.track)
			require.NotNil(t, ct, tc.track)
			rt, err := session.Subscribe(t.Context(), namespace, tc.track)
			require.NoError(t, err, tc.track)

			first, err := rt.ReadObject(t.Context())
			require.NoError(t, err, tc.track)
			require.Zero(t, first.ObjectID, "%s: the first group is read from its start", tc.track)
			startNr, endNr := internal.CalcLOCGroupRange(ct, first.GroupID, internal.MoqGroupDurMS)
			objects := []*moqtransport.Object{first}
			for range endNr - startNr - 1 {
				o, err := rt.ReadObject(t.Context())
				require.NoError(t, err, tc.track)
				objects = append(objects, o)
			}

			for _, o := range objects {
				require.Equal(t, first.GroupID, o.GroupID, tc.track)
				_, ok := o.Properties.Get(locPropTimestamp)
				assert.True(t, ok, "%s object %d: Timestamp", tc.track, o.ObjectID)
				marking, ok := o.Properties.Get(locPropVideoFrameMarking)
				if !tc.video {
					assert.False(t, ok, "%s object %d: no Video Frame Marking on audio",
						tc.track, o.ObjectID)
					continue
				}
				want := []byte{0xC0} // S=1 E=1 I=0 D=0
				if o.ObjectID == 0 {
					want = []byte{0xE0} // S=1 E=1 I=1 D=0
				}
				assert.Equal(t, want, marking.ValueBytes, "%s object %d: Video Frame Marking",
					tc.track, o.ObjectID)
			}
			require.NoError(t, rt.Close(), tc.track)
		}

		shutdown(sConn, cConn)
	})
}
