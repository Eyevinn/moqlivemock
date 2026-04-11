package internal_test

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/pub"
	"github.com/Eyevinn/moqlivemock/internal/sub"
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

	catalog, err := asset.GenCMAFCatalogEntry("cmsf/clear", internal.ProtectionNone,time.Now().UnixMilli())
	require.NoError(t, err)

	return asset, catalog
}

const testNamespace = "cmsf/clear"

func newPubHandler(asset *internal.Asset, catalog *internal.Catalog) *pub.Handler {
	return &pub.Handler{
		Namespaces: []pub.NamespaceEntry{
			{Namespace: []string{testNamespace}, Catalog: catalog},
		},
		Asset: asset,
		Logfh: io.Discard,
	}
}

func newSubHandler(outs map[string]io.Writer) *sub.Handler {
	return &sub.Handler{
		Namespace: []string{testNamespace},
		Outs:      outs,
		Logfh:     io.Discard,
		VideoName: "_avc",
		AudioName: "_aac",
	}
}

// shutdown closes both connections and waits for goroutines to drain.
func shutdown(sConn, cConn *memConn) {
	_ = sConn.CloseWithError(0, "")
	_ = cConn.CloseWithError(0, "")
	time.Sleep(time.Millisecond)
}

func TestCatalogExchange(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := memConnPair()

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

func TestFetchCatalog(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := memConnPair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		catalogBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace: []string{testNamespace},
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
		sConn, cConn := memConnPair()

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

func TestSubtitleReceive(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		sConn, cConn := memConnPair()

		ph := newPubHandler(asset, catalog)
		go ph.Handle(t.Context(), sConn)

		subsBuf := newSyncBuffer()
		sh := &sub.Handler{
			Namespace: []string{testNamespace},
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
		sConn, cConn := memConnPair()

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
