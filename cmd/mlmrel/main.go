package main

import (
	"context"
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
	"github.com/Eyevinn/moqlivemock/internal/qlogfilter"
	"github.com/Eyevinn/moqlivemock/internal/relay"
)

const (
	appName             = "mlmrel"
	defaultQlogFileName = "mlmrel.log"
)

var usg = `%s is a MoQ Transport relay. It accepts publisher and subscriber
sessions over raw QUIC or WebTransport (endpoint /moq) on one port, takes any
announced namespace, and forwards subscriptions and their objects from the
session that announced the namespace. One upstream subscription per track is
fanned out to any number of subscribers through a cache of recent groups;
FETCHes are served from that cache or proxied upstream. With -upstream it
also dials a publisher (e.g. mlmpub) as a client, so that it can sit between
mlmpub and mlmsub:

  mlmpub -addr 0.0.0.0:4443
  %s -addr 0.0.0.0:4444 -upstream moqt://localhost:4443
  mlmsub -addr localhost:4444 -muxout - | ffplay -

A qlog is always written -- there is no way to turn it off -- to the file
named by -qlog, or to stderr with -qlog -.

Usage of %s:
`

type options struct {
	certFile    string
	keyFile     string
	addr        string
	upstream    string
	pendingWait time.Duration
	cacheGroups int
	queueLen    int
	linger      time.Duration
	qlogfile    string
	qlogEvents  string
	loglevel    string
	version     bool
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.certFile, "cert", "cert.pem", "TLS certificate file")
	fs.StringVar(&opts.keyFile, "key", "key.pem", "TLS key file")
	fs.StringVar(&opts.addr, "addr", "0.0.0.0:4443", "listen address")
	fs.StringVar(&opts.upstream, "upstream", "",
		"upstream publisher to dial: moqt://host[:port] (QUIC) or https://host[:port][/path] (WebTransport)")
	fs.DurationVar(&opts.pendingWait, "pending-wait", 0,
		"how long a SUBSCRIBE for an unannounced namespace waits for an announcement before rejection")
	fs.IntVar(&opts.cacheGroups, "cache-groups", 3,
		"recent groups cached per track, for late joins and cache-served FETCHes")
	fs.IntVar(&opts.queueLen, "queue", 256,
		"per-subscriber object queue length; on overflow the subscriber skips to the next group boundary")
	fs.DurationVar(&opts.linger, "linger", 2*time.Second,
		"how long an upstream subscription survives its last downstream subscriber")
	fs.StringVar(&opts.qlogfile, "qlog", defaultQlogFileName, "qlog file to write to. Use '-' for stderr")
	fs.StringVar(&opts.qlogEvents, "qlog-events", "all",
		fmt.Sprintf("qlog event classes to write: all, or a comma-separated subset of %s",
			strings.Join(qlogfilter.ClassNames(), ",")))
	fs.StringVar(&opts.loglevel, "loglevel", "info", "Log level: debug, info, warning, error")
	fs.BoolVar(&opts.version, "version", false, fmt.Sprintf("Get %s version", appName))
	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
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

// parseLogLevel converts a string log level to slog.Level
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warning", "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "Unknown log level: %s, using 'info'\n", level)
		return slog.LevelInfo
	}
}

func runWithOptions(opts *options) error {
	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(opts.loglevel),
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintf(os.Stderr, "\nReceived signal, shutting down...\n")
		cancel()
	}()

	return runServer(ctx, opts)
}

func runServer(ctx context.Context, opts *options) error {
	tlsConfig, err := internal.ServerTLSConfig(opts.certFile, opts.keyFile)
	if err != nil {
		slog.Error("failed to generate TLS config", "error", err)
		return err
	}

	var logfh io.Writer
	if opts.qlogfile == "-" {
		logfh = os.Stderr
	} else {
		// The flag names the file; defaultQlogFileName is only its default.
		fh, err := os.OpenFile(opts.qlogfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("opening qlog file %q: %w", opts.qlogfile, err)
		}
		logfh = fh
		defer fh.Close()
	}
	keep, err := qlogfilter.ParseClasses(opts.qlogEvents)
	if err != nil {
		return err
	}

	h := relay.NewHandler(logfh)
	h.QlogFilter = keep
	h.PendingWait = opts.pendingWait
	h.CacheGroups = opts.cacheGroups
	h.QueueLen = opts.queueLen
	h.Linger = opts.linger
	if opts.upstream != "" {
		go runUpstream(ctx, h, opts.upstream)
	}
	err = internal.RunMoQServer(ctx, opts.addr, tlsConfig, h)
	// A signal cancels the context and takes the listener down with it; that
	// is a clean shutdown, and the interop-runner requires exit code 0 on
	// SIGTERM.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
