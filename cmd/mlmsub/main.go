package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/moqtransport/quicmoq"
	"github.com/mengelbart/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

const (
	appName             = "mlmsub"
	defaultQlogFileName = "mlmsub.log"
)

var usg = `%s acts as a MoQ client and subscriber for WARP.
Should first subscribe to catalog. When receiving a catalog, it should choose one video and 
one audio track and subscribe to these.

When receiving the media, it can write out to concatenated CMAF tracks but also multiplex
the tracks into a single CMAF file. By muxing the tracks and choosing muxout to "-" (stdout),
it is possible to pipe the stream to ffplay get synchronized playback of video and audio.

mlmsub -muxout - | ffplay - 

Usage of %s:
`

type options struct {
	addr         string
	trackname    string
	duration     int
	endAfter     int
	switchTracks bool
	muxout       string
	videoOut     string
	audioOut     string
	qlogfile     string
	videoname    string
	audioname    string
	loglevel     string
	version      bool
	testNew      bool
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.addr, "addr", "localhost:8080", "connect address (use https:// for WebTransport)")
	fs.StringVar(&opts.trackname, "trackname", "video_400kbps", "Track to subscribe to")
	fs.BoolVar(&opts.version, "version", false, fmt.Sprintf("Get %s version", appName))
	fs.IntVar(&opts.duration, "duration", 0, "Duration of session in seconds (0 means unlimited)")
	fs.IntVar(&opts.endAfter, "end-after", 0,
		"Send SUBSCRIBE_UPDATE to end subscriptions after X groups from first group (0 means no limit)")
	fs.BoolVar(&opts.switchTracks, "switch-tracks", false,
		"Start with video+audio, then switch video tracks (400kbps→600kbps→900kbps), then audio tracks (monotonic→scale)")
	fs.StringVar(&opts.muxout, "muxout", "", "Output file for mux or stdout (-)")
	fs.StringVar(&opts.videoOut, "videoout", "", "Output file for video or stdout (-)")
	fs.StringVar(&opts.audioOut, "audioout", "", "Output file for audio or stdout (-)")
	fs.StringVar(&opts.qlogfile, "qlog", defaultQlogFileName, "qlog file to write to. Use '-' for stderr")
	fs.StringVar(&opts.videoname, "videoname", "", "Substring to match for selecting video track (default use first)")
	fs.StringVar(&opts.audioname, "audioname", "", "Substring to match for selecting audio track (default use first)")
	fs.StringVar(&opts.loglevel, "loglevel", "info", "Log level: debug, info, warning, error")
	fs.BoolVar(&opts.testNew, "test-new", false, "Test new refactored architecture (experimental)")

	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
	// Parse command line arguments first to get the log level
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, os.Args)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "Error parsing options: %v\n", err)
		}
		os.Exit(1)
	}

	if err := runWithOptions(opts); err != nil {
		slog.Error("error running application", "error", err)
		os.Exit(1)
	}
}

func runWithOptions(opts *options) error {
	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: internal.ParseLogLevel(opts.loglevel),
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if opts.duration > 0 {
		tctx, tcancel := context.WithTimeout(ctx, time.Duration(opts.duration)*time.Second)
		defer tcancel()
		ctx = tctx
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintf(os.Stderr, "\nReceived signal, cancelling...\n")
		cancel()
	}()

	return runClient(ctx, opts)
}

func runClient(ctx context.Context, opts *options) error {
	var logfh io.Writer
	if opts.qlogfile == "-" {
		logfh = os.Stderr
	} else {
		fh, err := os.OpenFile(defaultQlogFileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			slog.Error("failed to open log file", "error", err)
		}
		logfh = fh
		defer fh.Close()
	}

	// Automatically use WebTransport if address starts with https://
	useWebTransport := strings.HasPrefix(opts.addr, "https://")

	h := &moqHandler{
		quic:         !useWebTransport,
		addr:         opts.addr,
		namespace:    []string{internal.Namespace},
		logfh:        logfh,
		videoname:    opts.videoname,
		audioname:    opts.audioname,
		endAfter:     opts.endAfter,
		switchTracks: opts.switchTracks,
	}

	outs := make(map[string]io.Writer)

	outNames := map[string]string{
		"mux":   opts.muxout,
		"video": opts.videoOut,
		"audio": opts.audioOut,
	}

	for name, out := range outNames {
		switch out {
		case "-":
			outs[name] = os.Stdout
		case "":
			outs[name] = nil
		default:
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			outs[name] = f
			defer f.Close()
		}
	}

	// Use new architecture if test flag is set
	if opts.testNew {
		return runNewArchitectureTest(ctx, useWebTransport, opts.addr, outs)
	}
	
	return h.runClient(ctx, useWebTransport, outs)
}

func dialQUIC(ctx context.Context, addr string) (moqtransport.Connection, error) {
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		return nil, err
	}
	return quicmoq.NewClient(conn), nil
}

func dialWebTransport(ctx context.Context, addr string) (moqtransport.Connection, error) {
	dialer := webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	_, session, err := dialer.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	return webtransportmoq.NewClient(session), nil
}

// runNewArchitectureTest tests the new refactored architecture
func runNewArchitectureTest(ctx context.Context, useWebTransport bool, addr string, outs map[string]io.Writer) error {
	slog.Info("testing new refactored architecture")
	
	// Establish connection
	var conn moqtransport.Connection
	var err error
	if useWebTransport {
		conn, err = dialWebTransport(ctx, addr)
	} else {
		conn, err = dialQUIC(ctx, addr)
	}
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}
	defer func() {
		err := conn.CloseWithError(0, "session completed")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
	}()
	
	// Create MoQ session and transport
	session := moqtransport.NewSession(conn.Protocol(), conn.Perspective(), initialMaxRequestID)
	
	// Create a minimal handler for announcements
	handler := moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r moqtransport.Message) {
		switch r.Method() {
		case moqtransport.MessageAnnounce:
			// Accept all announcements
			err := w.Accept()
			if err != nil {
				slog.Error("failed to accept announcement", "error", err)
			}
		default:
			slog.Debug("received message", "method", r.Method())
		}
	})
	
	transport := &moqtransport.Transport{
		Conn:    conn,
		Handler: handler,
		Session: session,
	}
	
	// Initialize the transport
	err = transport.Run()
	if err != nil {
		return fmt.Errorf("failed to initialize MoQ transport: %w", err)
	}
	
	// Create simple client with new architecture
	namespace := []string{internal.Namespace}
	client := NewSimpleClient(namespace, outs["mux"], outs["video"], outs["audio"])
	
	// Run simple playback test
	return client.RunSimplePlayback(ctx, session)
}
