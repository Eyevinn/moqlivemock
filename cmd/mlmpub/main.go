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
	"github.com/Eyevinn/moqlivemock/internal/cc608"
	"github.com/Eyevinn/moqlivemock/internal/pub"
	"github.com/Eyevinn/moqlivemock/internal/qlogfilter"
)

const (
	appName = "mlmpub"
)

var usg = `%s acts as a MoQ server and publisher using MSF/CMSF to send
mocked live video and audio tracks, synchronized with wall-clock time.
It is intended to be a test-bed for MoQ and MSF/CMSF.

A qlog is always written -- there is no way to turn it off -- to the file
named by -qlog, or to stderr with -qlog -. Object payloads are truncated in
it, so it records what was sent without being a copy of the media.

Usage of %s:
`

const (
	defaultQlogFileName = "mlmpub.log"
)

type options struct {
	certFile         string
	keyFile          string
	addr             string
	asset            string
	qlogfile         string
	qlogEvents       string
	loglevel         string
	audioSampleBatch int
	videoSampleBatch int
	sidePort         int
	subsWvttLangs    string
	subsStppLangs    string
	cencKey          string
	iv               string
	kid              string
	scheme           string
	laURL            string
	drmConfigPath    string
	cc608            bool
	cc608Mode        string
	version          bool
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.certFile, "cert", "cert.pem", "TLS certificate file (only used for server)")
	fs.StringVar(&opts.keyFile, "key", "key.pem", "TLS key file (only used for server)")
	fs.StringVar(&opts.addr, "addr", "0.0.0.0:4443", "listen or connect address")
	fs.StringVar(&opts.asset, "asset", "../../assets/test10s", "Asset to serve")
	fs.StringVar(&opts.qlogfile, "qlog", defaultQlogFileName, "qlog file to write to. Use '-' for stderr")
	fs.StringVar(&opts.qlogEvents, "qlog-events", "all",
		fmt.Sprintf("qlog event classes to write: all, or a comma-separated subset of %s",
			strings.Join(qlogfilter.ClassNames(), ",")))
	fs.StringVar(&opts.loglevel, "loglevel", "info", "Log level: debug, info, warning, error")
	fs.IntVar(&opts.audioSampleBatch, "audiobatch", 1, "Nr audio samples per MoQ object/CMAF chunk")
	fs.IntVar(&opts.videoSampleBatch, "videobatch", 1, "Nr video samples per MoQ object/CMAF chunk")
	fs.IntVar(&opts.sidePort, "sideport", 0, "Port for HTTP side server serving /fingerprint and /clearkey (0 to disable)")
	fs.StringVar(&opts.subsWvttLangs, "subswvtt", "sv", "Comma-separated WVTT subtitle languages (e.g. 'en,sv')")
	fs.StringVar(&opts.subsStppLangs, "subsstpp", "en", "Comma-separated STPP subtitle languages (e.g. 'en,sv')")
	fs.StringVar(&opts.kid, "kid", "", "key id for CENC encryption (32 hex or 24 base64 chars)")
	fs.StringVar(&opts.iv, "iv", "", "IV for CENC encryption (16 or 32 hex chars)")
	fs.StringVar(&opts.cencKey, "cenckey", "", "Key for CENC encryption (32 hex or 24 base64 chars),"+
		"if no key is specified the key id will be used as the key.")
	fs.StringVar(&opts.scheme, "scheme", "cbcs", "Scheme for CENC encryption,"+
		"either \"cenc\" or \"cbcs\"")
	fs.StringVar(&opts.laURL, "laurl", "", "ClearKey/ECCP license acquisition URL announced in catalog."+
		" Falls back to http://localhost:{sideport}/clearkey if not set.")
	fs.StringVar(&opts.drmConfigPath, "drmpath", "", "path to a drm config file")
	fs.BoolVar(&opts.cc608, "cc608", false,
		"inject auto-generated CTA-608 CC1 captions into AVC, HEVC and AV1 video")
	fs.StringVar(&opts.cc608Mode, "cc608mode", "",
		fmt.Sprintf("CTA-608 caption delivery mode: %s (default %s; \"roll-up\" means roll-up2)."+
			" Requires -cc608", strings.Join(cc608.ModeNames(), ", "), cc608.ModePaintOn))
	fs.BoolVar(&opts.version, "version", false, fmt.Sprintf("Get %s version", appName))
	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
	// Initialize slog to log to stderr
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	if err := run(os.Args); err != nil {
		slog.Error("error running application", "error", err)
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
	return runServer(opts)
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

func runServer(opts *options) error {
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
	tlsConfig, err := internal.ServerTLSConfig(opts.certFile, opts.keyFile)
	if err != nil {
		slog.Error("failed to generate TLS config", "error", err)
		return err
	}
	// Parse commercial DRM config (CPIX)
	var drm *internal.DRMInfo
	if opts.drmConfigPath != "" {
		drm, err = internal.ConfigureDRMFromFile(opts.drmConfigPath)
		if err != nil {
			return err
		}
	}

	// Parse ClearKey/ECCP config (explicit key flags)
	laURL := opts.laURL
	if laURL == "" && opts.sidePort > 0 {
		laURL = fmt.Sprintf("http://localhost:%d/clearkey", opts.sidePort)
	}
	var eccp *internal.DRMInfo
	eccp, err = internal.ParseCENCflags(opts.scheme, opts.kid, opts.cencKey, opts.iv, laURL)
	if err != nil {
		return err
	}

	asset, err := internal.LoadAssetWithProtection(opts.asset, opts.audioSampleBatch, opts.videoSampleBatch, drm, eccp)
	if err != nil {
		return err
	}

	slog.Info("loaded asset", "path", opts.asset, "audioSampleBatch", opts.audioSampleBatch,
		"videoSampleBatch", opts.videoSampleBatch)

	// Parse subtitle languages and add tracks
	wvttLangs := parseLanguages(opts.subsWvttLangs)
	stppLangs := parseLanguages(opts.subsStppLangs)
	err = asset.AddSubtitleTracks(wvttLangs, stppLangs)
	if err != nil {
		return err
	}
	slog.Info("added subtitle tracks", "wvtt", wvttLangs, "stpp", stppLangs)

	// Enable in-band CTA-608 caption injection on video tracks when requested.
	// A nil generator (the default) is a complete no-op; the codec gate is
	// applied per track at serve time.
	ccMode, err := cc608.ParseMode(opts.cc608Mode)
	if err != nil {
		return err
	}
	if opts.cc608Mode != "" && !opts.cc608 {
		return fmt.Errorf("-cc608mode %s requires -cc608", opts.cc608Mode)
	}
	if opts.cc608 {
		asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true, Mode: ccMode}))
		slog.Info("enabled CTA-608 caption injection (CC1) on AVC, HEVC and AV1 video tracks",
			"mode", ccMode)
	}

	now := time.Now().UnixMilli()
	var namespaces []pub.NamespaceEntry

	// Always create the LOC/MSF namespace (AVC + AAC/Opus, clear only)
	locCatalog, err := asset.GenLOCCatalogEntry(now)
	if err != nil {
		return err
	}
	if len(locCatalog.Tracks) > 0 {
		namespaces = append(namespaces, pub.NamespaceEntry{
			Namespace: []string{"msf/clear"},
			Catalog:   locCatalog,
			Packaging: "loc",
		})
	}

	// Add moq-mi namespace (catalogless; fixed track names video0/audio0)
	// if the asset has compatible clear AVC video and AAC-LC / Opus audio.
	if mmTracks, mmErr := pub.BuildMoqMITrackMap(asset); mmErr != nil {
		slog.Info("skipping moq-mi namespace", "reason", mmErr)
	} else {
		namespaces = append(namespaces, pub.NamespaceEntry{
			Namespace:   []string{"moq-mi/clear"},
			Packaging:   "moqmi",
			MoqMITracks: mmTracks,
		})
	}

	// CMSF namespaces carry a unified catalog that lists each rendition in
	// both CMAF and LOCMAF (v0.2) packaging, sharing init data via initRef.
	// The serve path picks the encoding per track (pub.PublishTrack), so the
	// NamespaceEntry.Packaging is informational only here.

	// Always create the clear namespace
	clearCatalog, err := asset.GenCMAFCatalogEntry("cmsf/clear", internal.ProtectionNone, now)
	if err != nil {
		return err
	}
	namespaces = append(namespaces, pub.NamespaceEntry{
		Namespace: []string{"cmsf/clear"}, Catalog: clearCatalog, Packaging: "cmaf",
	})

	// Add commercial DRM namespace if configured
	if drm != nil {
		drmCatalog, err := asset.GenCMAFCatalogEntry(fmt.Sprintf("cmsf/drm-%s", opts.scheme),
			internal.ProtectionDRM, now)
		if err != nil {
			return err
		}
		namespaces = append(namespaces, pub.NamespaceEntry{
			Namespace: []string{fmt.Sprintf("cmsf/drm-%s", opts.scheme)},
			Catalog:   drmCatalog,
			Packaging: "cmaf",
		})
	}

	// Add ClearKey/ECCP namespace if configured
	if eccp != nil {
		eccpCatalog, err := asset.GenCMAFCatalogEntry(fmt.Sprintf("cmsf/eccp-%s", opts.scheme),
			internal.ProtectionECCP, now)
		if err != nil {
			return err
		}
		namespaces = append(namespaces, pub.NamespaceEntry{
			Namespace: []string{fmt.Sprintf("cmsf/eccp-%s", opts.scheme)},
			Catalog:   eccpCatalog,
			Packaging: "cmaf",
		})
	}

	for _, ns := range namespaces {
		tracks := 0
		if ns.Catalog != nil {
			tracks = len(ns.Catalog.Tracks)
		}
		slog.Info("configured namespace", "namespace", ns.Namespace,
			"packaging", ns.Packaging, "tracks", tracks,
			"moqmiTracks", len(ns.MoqMITracks))
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
	logfh = qlogfilter.New(logfh, keep)
	h := &pub.Handler{
		Namespaces: namespaces,
		Asset:      asset,
		Logfh:      logfh,
	}

	s := &server{
		addr:      opts.addr,
		tlsConfig: tlsConfig,
		handler:   h,
		sidePort:  opts.sidePort,
		eccp:      eccp,
	}

	return s.runServer(ctx)
}

// parseLanguages parses a comma-separated string of language codes.
// Returns an empty slice if the input is empty.
func parseLanguages(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	langs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			langs = append(langs, p)
		}
	}
	return langs
}
