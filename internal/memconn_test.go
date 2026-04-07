package internal_test

// memconn provides an in-memory implementation of moqtransport.Connection
// for deterministic integration testing without QUIC networking.
//
// Architecture:
//
//	memConnPair() creates two memConn instances (server + client) wired together.
//	Streams opened on one side are delivered to the other via buffered channels.
//	Each stream uses asyncPipe for data transfer — an asynchronous, buffered pipe
//	where writes never block and reads block until data arrives.
//
// Why not io.Pipe?
//
//	io.Pipe is synchronous: a Write blocks until a Read consumes the data. In MoQ,
//	both sides of a session read and write control messages concurrently on the same
//	bidirectional stream. With io.Pipe, if both sides write before either reads,
//	they deadlock. asyncPipe buffers writes internally, avoiding this.
//
// Why not QUIC?
//
//	Real QUIC connections (even on localhost) introduce non-determinism from UDP
//	transport timing, causing intermittent test failures in CI. The in-memory
//	connection removes all network I/O, making tests deterministic and compatible
//	with testing/synctest's fake clock.
//
// Cleanup:
//
//	CloseWithError closes all tracked pipes (unblocking goroutines stuck in
//	asyncPipe.Read) and cancels both the local and peer contexts. This is required
//	by synctest, which panics if the test bubble exits with blocked goroutines.

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/Eyevinn/moqtransport"
)

// asyncPipe is an asynchronous in-memory pipe. Writes buffer internally and
// never block. Reads block until data is available or the pipe is closed.
type asyncPipe struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	err    error
	ready  chan struct{} // signalled (non-blocking send) on Write or Close
}

func newAsyncPipe() *asyncPipe {
	return &asyncPipe{ready: make(chan struct{}, 1)}
}

func (p *asyncPipe) signal() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *asyncPipe) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := p.buf.Write(data)
	p.signal()
	return n, err
}

func (p *asyncPipe) Read(data []byte) (int, error) {
	for {
		p.mu.Lock()
		if p.buf.Len() > 0 {
			n, err := p.buf.Read(data)
			p.mu.Unlock()
			return n, err
		}
		if p.closed {
			err := p.err
			p.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		p.mu.Unlock()
		// Block until signalled. synctest treats this as a durable block,
		// allowing fake time to advance when all goroutines are waiting.
		<-p.ready
	}
}

func (p *asyncPipe) Close() error {
	return p.CloseWithError(nil)
}

func (p *asyncPipe) CloseWithError(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.err = err
	p.signal()
	return nil
}

// memConnPair creates a pair of in-memory connections (server, client)
// wired together. Streams opened on one side appear on the other's
// Accept calls. Both connections share contexts so that closing either
// side cancels both.
func memConnPair() (*memConn, *memConn) {
	serverCtx, serverCancel := context.WithCancel(context.Background())
	clientCtx, clientCancel := context.WithCancel(context.Background())

	server := &memConn{
		perspective: moqtransport.PerspectiveServer,
		ctx:         serverCtx,
		cancel:      serverCancel,
		cancelPeer:  clientCancel,
		biAccept:    make(chan moqtransport.Stream, 16),
		uniAccept:   make(chan moqtransport.ReceiveStream, 16),
	}
	client := &memConn{
		perspective: moqtransport.PerspectiveClient,
		ctx:         clientCtx,
		cancel:      clientCancel,
		cancelPeer:  serverCancel,
		biAccept:    make(chan moqtransport.Stream, 16),
		uniAccept:   make(chan moqtransport.ReceiveStream, 16),
	}

	server.peer = client
	client.peer = server

	return server, client
}

// memConn implements moqtransport.Connection using in-memory pipes.
type memConn struct {
	perspective moqtransport.Perspective
	ctx         context.Context
	cancel      context.CancelFunc
	cancelPeer  context.CancelFunc
	peer        *memConn
	biAccept    chan moqtransport.Stream       // peer-opened bidirectional streams
	uniAccept   chan moqtransport.ReceiveStream // peer-opened unidirectional streams
	streamID    atomic.Uint64

	mu     sync.Mutex
	pipes  []*asyncPipe // tracked for cleanup on close
	closed bool
}

func (c *memConn) AcceptStream(ctx context.Context) (moqtransport.Stream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case s := <-c.biAccept:
		return s, nil
	}
}

func (c *memConn) AcceptUniStream(ctx context.Context) (moqtransport.ReceiveStream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case s := <-c.uniAccept:
		return s, nil
	}
}

func (c *memConn) OpenStream() (moqtransport.Stream, error) {
	return c.OpenStreamSync(context.Background())
}

// OpenStreamSync creates a bidirectional stream with two async pipes (one per
// direction) and delivers the remote end to the peer's biAccept channel.
func (c *memConn) OpenStreamSync(ctx context.Context) (moqtransport.Stream, error) {
	id := c.streamID.Add(1)
	pipeAtoB := newAsyncPipe() // local writes, remote reads
	pipeBtoA := newAsyncPipe() // remote writes, local reads

	// Both sides track both pipes so either side's Close tears them down.
	c.trackPipe(pipeAtoB)
	c.trackPipe(pipeBtoA)
	c.peer.trackPipe(pipeAtoB)
	c.peer.trackPipe(pipeBtoA)

	local := &memStream{id: id, r: pipeBtoA, w: pipeAtoB}
	remote := &memStream{id: id, r: pipeAtoB, w: pipeBtoA}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case c.peer.biAccept <- remote:
		return local, nil
	}
}

func (c *memConn) OpenUniStream() (moqtransport.SendStream, error) {
	return c.OpenUniStreamSync(context.Background())
}

// OpenUniStreamSync creates a unidirectional stream with a single async pipe
// and delivers the receive end to the peer's uniAccept channel.
func (c *memConn) OpenUniStreamSync(ctx context.Context) (moqtransport.SendStream, error) {
	id := c.streamID.Add(1)
	pipe := newAsyncPipe()

	c.trackPipe(pipe)
	c.peer.trackPipe(pipe)

	local := &memSendStream{id: id, w: pipe}
	remote := &memReceiveStream{id: id, r: pipe}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case c.peer.uniAccept <- remote:
		return local, nil
	}
}

// SendDatagram is a no-op; MoQ does not use datagrams in these tests.
func (c *memConn) SendDatagram([]byte) error {
	return nil
}

// ReceiveDatagram blocks until the context is cancelled. Returning an error
// immediately would kill the MoQ session's errgroup, so we block instead.
func (c *memConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *memConn) trackPipe(p *asyncPipe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pipes = append(c.pipes, p)
}

// CloseWithError closes all tracked pipes (unblocking readers), then cancels
// both the local and peer contexts so all goroutines can exit cleanly.
func (c *memConn) CloseWithError(uint64, string) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		for _, p := range c.pipes {
			_ = p.CloseWithError(io.EOF)
		}
	}
	c.mu.Unlock()
	c.cancel()
	c.cancelPeer()
	return nil
}

func (c *memConn) Context() context.Context {
	return c.ctx
}

func (c *memConn) Protocol() moqtransport.Protocol {
	return moqtransport.ProtocolQUIC
}

func (c *memConn) Perspective() moqtransport.Perspective {
	return c.perspective
}

// memStream implements moqtransport.Stream (bidirectional).
type memStream struct {
	id uint64
	r  *asyncPipe // read from peer
	w  *asyncPipe // write to peer
}

func (s *memStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *memStream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *memStream) Close() error                { return s.w.Close() }
func (s *memStream) Stop(uint32)                  { _ = s.r.CloseWithError(io.EOF) }
func (s *memStream) Reset(uint32)                 { _ = s.w.CloseWithError(io.ErrClosedPipe) }
func (s *memStream) StreamID() uint64             { return s.id }

// memSendStream implements moqtransport.SendStream (write-only).
type memSendStream struct {
	id uint64
	w  *asyncPipe
}

func (s *memSendStream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *memSendStream) Close() error                { return s.w.Close() }
func (s *memSendStream) Reset(uint32)                 { _ = s.w.CloseWithError(io.ErrClosedPipe) }
func (s *memSendStream) StreamID() uint64             { return s.id }

// memReceiveStream implements moqtransport.ReceiveStream (read-only).
type memReceiveStream struct {
	id uint64
	r  *asyncPipe
}

func (s *memReceiveStream) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *memReceiveStream) Stop(uint32)                 { _ = s.r.CloseWithError(io.EOF) }
func (s *memReceiveStream) StreamID() uint64            { return s.id }
