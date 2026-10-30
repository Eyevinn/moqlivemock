package sub

import (
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

// catalogPublisher serves a catalog of one Object at {0, 0}, the way mlmpub
// does, except that its fills are left to fill: nil refuses them, and a
// handler that never opens a stream stands for a relay that ignores them. It
// always answers FETCH.
func catalogPublisher(fill moqtransport.FillHandler) *moqtransport.Session {
	catalog := []byte(`{"version":"1","tracks":[]}`)
	return &moqtransport.Session{
		Implementation: "sub-test-publisher",
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
			_, _ = r.Accept(moqtransport.WithLargestObject(moqtransport.Location{}))
		}),
		FetchHandler: moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
			if _, ok := r.Range(moqtransport.Location{}, true); !ok {
				_ = r.Reject(moqtransport.RequestErrorInvalidRange, "after the catalog")
				return
			}
			response, err := r.Accept()
			if err != nil {
				return
			}
			_ = response.WriteObject(moqtransport.Object{Payload: catalog})
			_ = response.Close()
		}),
		FillHandler: fill,
	}
}

// TestJoiningCatalogFillFallback: on draft-22 the catalog's current group
// comes in a fill, and when the fill is refused, or never comes, mlmsub
// fetches the group instead.
func TestJoiningCatalogFillFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		fill moqtransport.FillHandler
		wait time.Duration
	}{
		{"refused", nil, 0},
		{"never opened", moqtransport.FillHandlerFunc(func(r *moqtransport.FillRequest) {
			<-r.Context().Done()
		}), fillWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sConn, cConn := testconn.PairALPN("moqt-22")
				server := catalogPublisher(tc.fill)
				errs := make(chan error, 1)
				go func() { errs <- server.Run(t.Context(), sConn) }()
				h := &Handler{Logfh: io.Discard, CatalogTrack: "catalog"}
				session, err := h.startSession(t.Context(), cConn)
				require.NoError(t, err)
				require.NoError(t, <-errs)

				start := time.Now()
				require.NoError(t, h.joiningCatalog(t.Context(), session, []string{"ns"}))
				require.NotNil(t, h.catalog)
				require.Equal(t, tc.wait, time.Since(start))

				_ = session.Close(moqtransport.SessionErrorNoError, "test over")
				_ = server.Close(moqtransport.SessionErrorNoError, "test over")
			})
		})
	}
}
