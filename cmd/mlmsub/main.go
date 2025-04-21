package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	appName = "mlmsub"
)

var usg = `%s acts as a MoQ client and subscriber for WARP.
Should first subscribe to catalog. When receiving a catalog, it should choose one video and 
one audio track and subscribe to these.

Usage of %s:
`

type options struct {
	addr         string
	webtransport bool
	trackname    string
	version      bool
	duration     int
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.addr, "addr", "localhost:8080", "listen or connect address")
	fs.BoolVar(&opts.webtransport, "webtransport", false, "Use webtransport instead of QUIC (client only)")
	fs.StringVar(&opts.trackname, "trackname", "video_400kbps", "Track to subscribe to")
	fs.BoolVar(&opts.version, "version", false, fmt.Sprintf("Get %s version", appName))
	fs.IntVar(&opts.duration, "duration", 0, "Duration of session in seconds (0 means unlimited)")
	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, args)

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}

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
	h := &moqHandler{
		quic:      !opts.webtransport,
		addr:      opts.addr,
		namespace: []string{internal.Namespace},
	}
	return h.runClient(ctx, opts.webtransport)
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
