package internal_test

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/pub"
	"github.com/Eyevinn/moqlivemock/internal/sub"
	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAssetDir = "../assets/test10s"

// syncBuffer is a thread-safe wrapper around bytes.Buffer that signals
// when data is written. The signal channel allows waiters to block
// efficiently instead of polling with time.Sleep, which is important
// for synctest compatibility.
type syncBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{} // signalled on every Write
}

func newSyncBuffer() *syncBuffer {
	return &syncBuffer{ready: make(chan struct{}, 1)}
}

func (b *syncBuffer) signal() {
	select {
	case b.ready <- struct{}{}:
	default:
	}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	b.signal()
	return n, err
}

// WaitForLen blocks until the buffer has at least n bytes.
func (b *syncBuffer) WaitForLen(n int) {
	for {
		b.mu.Lock()
		if b.buf.Len() >= n {
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		<-b.ready
	}
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func loadTestAsset(t *testing.T) (*internal.Asset, *internal.Catalog) {
	t.Helper()
	asset, err := internal.LoadAsset(testAssetDir, 2, 1)
	require.NoError(t, err)

	err = asset.AddSubtitleTracks([]string{"en"}, nil)
	require.NoError(t, err)

	catalog, err := asset.GenCMAFCatalogEntry(internal.NamespaceString(testNamespace),
		internal.ProtectionNone, time.Now().UnixMilli())
	require.NoError(t, err)

	return asset, catalog
}

// testNamespace is the tuple mlmpub announces: one field per element, so the
// tests exercise the same shape a relay does its prefix matching on.
var testNamespace = []string{"mlm", "cmsf", "clear"}

func newPubHandler(asset *internal.Asset, catalog *internal.Catalog) *pub.Handler {
	return &pub.Handler{
		Namespaces: []pub.NamespaceEntry{
			{Namespace: testNamespace, Catalog: catalog},
		},
		Asset: asset,
		Logfh: io.Discard,
	}
}

func newSubHandler(outs map[string]io.Writer) *sub.Handler {
	return &sub.Handler{
		Namespace: testNamespace,
		Outs:      outs,
		Logfh:     io.Discard,
		VideoName: "_avc",
		AudioName: "_aac",
	}
}

// shutdown closes both connections and waits for goroutines to drain.
func shutdown(sConn, cConn *testconn.Conn) {
	_ = sConn.CloseWithError(0, "")
	_ = cConn.CloseWithError(0, "")
	time.Sleep(time.Millisecond)
}

func TestCatalogExchange(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		catalogBuf := newSyncBuffer()
		sh := newSubHandler(map[string]io.Writer{"catalog": catalogBuf})
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		catalogBuf.WaitForLen(1)

		assert.Contains(t, catalogBuf.String(), "video_", "catalog should contain video tracks")
		assert.Contains(t, catalogBuf.String(), "audio_", "catalog should contain audio tracks")

		shutdown(sConn, cConn)
	})
}

// TestJoiningCatalog exercises the default catalog-retrieval path: a SUBSCRIBE
// (Largest Object) plus a relative joining FETCH (offset 0). It also verifies
// the catalog is delivered exactly once — the joining FETCH provides the
// baseline and the subscription's replayed object 0 (<= largest) is deduped.
func TestJoiningCatalog(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		catalogBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace:   testNamespace,
			Outs:        map[string]io.Writer{"catalog": catalogBuf},
			Logfh:       io.Discard,
			VideoName:   "NONE",
			AudioName:   "NONE",
			CatalogMode: "joining",
		}
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		catalogBuf.WaitForLen(1)
		// Let the background subscription-update goroutine run so it processes
		// (and skips) the publisher's replayed object 0 before we assert.
		synctest.Wait()

		out := catalogBuf.String()
		assert.Contains(t, out, "video_", "catalog should contain video tracks")
		assert.Contains(t, out, "audio_", "catalog should contain audio tracks")
		assert.Equal(t, 1, strings.Count(out, `"tracks"`),
			"catalog should be delivered exactly once (fetch baseline; subscription duplicate deduped)")

		shutdown(sConn, cConn)
	})
}

// TestSubscribeCatalogLegacy keeps coverage of the legacy subscribe-only path.
func TestSubscribeCatalogLegacy(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		catalogBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace:   testNamespace,
			Outs:        map[string]io.Writer{"catalog": catalogBuf},
			Logfh:       io.Discard,
			VideoName:   "NONE",
			AudioName:   "NONE",
			CatalogMode: "subscribe",
		}
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		catalogBuf.WaitForLen(1)
		assert.Contains(t, catalogBuf.String(), "video_", "catalog should contain video tracks")
		assert.Contains(t, catalogBuf.String(), "audio_", "catalog should contain audio tracks")

		shutdown(sConn, cConn)
	})
}

func TestFetchCatalog(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		catalogBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace: testNamespace,
			Outs:      map[string]io.Writer{"catalog": catalogBuf},
			Logfh:     io.Discard,
			VideoName: "NONE",
			AudioName: "NONE",
			UseFetch:  true,
		}
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		catalogBuf.WaitForLen(1)

		assert.Contains(t, catalogBuf.String(), "video_", "catalog should contain video tracks")
		assert.Contains(t, catalogBuf.String(), "audio_", "catalog should contain audio tracks")

		shutdown(sConn, cConn)
	})
}

func TestVideoAudioReceive(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		videoBuf := newSyncBuffer()
		audioBuf := newSyncBuffer()
		sh := newSubHandler(map[string]io.Writer{"video": videoBuf, "audio": audioBuf})
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		videoBuf.WaitForLen(1)
		audioBuf.WaitForLen(1)

		assert.Greater(t, videoBuf.Len(), 0, "should have received video data")
		assert.Greater(t, audioBuf.Len(), 0, "should have received audio data")

		shutdown(sConn, cConn)
	})
}

// writeRecorder keeps every Write as its own chunk: mlmsub writes the init
// data and then each object with one Write each.
type writeRecorder struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *writeRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (w *writeRecorder) get() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.writes...)
}

// TestPaintSubtitleReceiveLocmaf subscribes to the LOCMAF variant of a
// paint-model subtitle track, whose catalog name carries the LOCMAF suffix.
// mlmsub expands LOCMAF back to CMAF, so each received object must carry
// exactly the samples of the CMAF chunk the publisher generated.
func TestPaintSubtitleReceiveLocmaf(t *testing.T) {
	asset, err := internal.LoadAsset(testAssetDir, 2, 1)
	require.NoError(t, err)
	require.NoError(t, asset.AddPaintSubtitleTracks(nil, []string{"en"}, true))
	catalog, err := asset.GenCMAFCatalogEntry(internal.NamespaceString(testNamespace),
		internal.ProtectionNone, time.Now().UnixMilli())
	require.NoError(t, err)
	st := asset.SubtitleTracks[0]

	synctest.Test(t, func(t *testing.T) {
		// The publisher starts at the group after the current one.
		groupNr := internal.CurrSubtitleGroupNr(uint64(time.Now().UnixMilli()), internal.MoqGroupDurMS) + 1
		want, err := internal.GenSubtitleGroup(st, groupNr, internal.MoqGroupDurMS, "cmaf")
		require.NoError(t, err)

		sConn, cConn := testconn.Pair()
		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		rec := &writeRecorder{}
		sh := &sub.Handler{
			Namespace: testNamespace,
			Outs:      map[string]io.Writer{"subs": rec},
			Logfh:     io.Discard,
			VideoName: "NONE",
			AudioName: "NONE",
			SubsName:  "subs_stpc_en_locmaf",
		}
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		for len(rec.get()) < 3 {
			time.Sleep(10 * time.Millisecond)
		}
		writes := rec.get()
		for i := 0; i < 2; i++ {
			got := fullSamplesOf(t, writes[1+i])
			exp := fullSamplesOf(t, want.MoQObjects[i])
			require.Len(t, got, len(exp))
			for j := range exp {
				assert.Equal(t, exp[j].DecodeTime, got[j].DecodeTime)
				assert.Equal(t, exp[j].Dur, got[j].Dur)
				assert.Equal(t, exp[j].Flags, got[j].Flags)
				assert.Equal(t, exp[j].Data, got[j].Data)
			}
		}
		assert.True(t, strings.HasPrefix(string(fullSamplesOf(t, writes[1])[0].Data), "<?xml"),
			"the first object is a full document")
		assert.Equal(t, "ttmn", string(fullSamplesOf(t, writes[2])[0].Data[4:8]),
			"the second object is a no-change box")

		shutdown(sConn, cConn)
	})
}

// fullSamplesOf decodes the samples of one CMAF chunk (moof+mdat).
func fullSamplesOf(t *testing.T, chunk []byte) []mp4.FullSample {
	t.Helper()
	f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(chunk))
	require.NoError(t, err)
	require.NotEmpty(t, f.Segments)
	require.NotEmpty(t, f.Segments[0].Fragments)
	fss, err := f.Segments[0].Fragments[0].GetFullSamples(nil)
	require.NoError(t, err)
	return fss
}

func TestSubtitleReceive(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		subsBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace: testNamespace,
			Outs:      map[string]io.Writer{"subs": subsBuf},
			Logfh:     io.Discard,
			VideoName: "NONE",
			AudioName: "NONE",
			SubsName:  "wvtt",
		}
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		subsBuf.WaitForLen(1)

		assert.Greater(t, subsBuf.Len(), 0, "should have received subtitle data")

		shutdown(sConn, cConn)
	})
}

func TestMuxedOutput(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := testconn.Pair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		muxBuf := newSyncBuffer()
		sh := newSubHandler(map[string]io.Writer{"mux": muxBuf})
		go func() { _ = sh.RunWithConn(t.Context(), cConn) }()

		muxBuf.WaitForLen(1000)

		shutdown(sConn, cConn)

		data := muxBuf.Bytes()
		sr := bits.NewFixedSliceReader(data)
		f, err := mp4.DecodeFileSR(sr)
		require.NoError(t, err, "muxed output should be valid MP4")
		assert.NotNil(t, f.Init, "should have init segment")
		assert.Equal(t, 2, len(f.Init.Moov.Traks), "should have 2 tracks (video + audio)")
	})
}
