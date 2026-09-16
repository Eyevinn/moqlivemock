package internal_test

import (
	"testing"
	"testing/synctest"

	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

// interopNamespace is what moq-interop-runner addresses, and the one namespace
// mlmpub never puts a publisher prefix in front of.
var interopNamespace = []string{"moq-test", "interop"}

// TestPublisherAnswersNamespaceDiscovery: mlmpub answers SUBSCRIBE_NAMESPACE
// with exactly the namespaces under the requested prefix.
//
// Section 8.4 matches a namespace prefix one field at a time, which is the
// whole reason the namespaces are tuples: ("mlm") has to cover the content
// namespaces and miss the unprefixed interop one, and "ml" has to match
// nothing at all rather than being treated as a string prefix of "mlm".
func TestPublisherAnswersNamespaceDiscovery(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	for _, c := range []struct {
		name   string
		prefix []string
		want   [][]string
	}{
		{"publisher prefix", []string{"mlm"}, [][]string{testNamespace}},
		{"whole namespace", testNamespace, [][]string{testNamespace}},
		{"interop is not prefixed", []string{"moq-test"}, [][]string{interopNamespace}},
		{"empty prefix asks for all", nil, [][]string{testNamespace, interopNamespace}},
		{"unknown prefix", []string{"nope"}, nil},
		{"a field is matched whole, not as a string prefix", []string{"ml"}, nil},
		{"more fields than the namespace has", append(append([]string{}, testNamespace...), "extra"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ph := newPubHandler(asset, catalog)
				sConn, cConn := testconn.Pair()
				go ph.Handle(t.Context(), sConn)

				session := &moqtransport.Session{Implementation: "mlm-test-subscriber"}
				require.NoError(t, session.Run(t.Context(), cConn))

				nsSub, err := session.SubscribeNamespace(t.Context(), c.prefix)
				require.NoError(t, err)

				got := make([][]string, 0, len(c.want))
				for range c.want {
					ev := <-nsSub.Namespaces()
					require.True(t, ev.Available)
					got = append(got, ev.Namespace)
				}
				require.ElementsMatch(t, c.want, got)

				// And nothing beyond what the prefix covers.
				synctest.Wait()
				require.Empty(t, nsSub.Namespaces())

				shutdown(sConn, cConn)
			})
		})
	}
}

// TestPublisherAnnouncesNothingUnprompted: a session that sends no
// SUBSCRIBE_NAMESPACE is told nothing. mlmpub used to open a
// PUBLISH_NAMESPACE toward everything that connected, which told a subscriber
// about namespaces it had not asked for and made a prefix filter pointless.
func TestPublisherAnnouncesNothingUnprompted(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		ph := newPubHandler(asset, catalog)
		sConn, cConn := testconn.Pair()
		go ph.Handle(t.Context(), sConn)

		announced := make(chan []string, 4)
		session := &moqtransport.Session{
			Implementation: "mlm-test-listener",
			PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
				func(r *moqtransport.PublishNamespaceRequest) {
					if err := r.Accept(); err != nil {
						return
					}
					announced <- r.Namespace()
				}),
		}
		require.NoError(t, session.Run(t.Context(), cConn))

		synctest.Wait()
		require.Empty(t, announced)

		shutdown(sConn, cConn)
	})
}
