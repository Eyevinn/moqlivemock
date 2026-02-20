package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
)

const (
	appName = "mlmpub"
)

var usg = `%s acts as a MoQ server and publisher using MSF/CMSF to send
mocked live video and audio tracks, synchronized with wall-clock time.
It is intended to be a test-bed for MoQ and MSF/CMSF.

The qlog logs are currently massive, and written to

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
	audioSampleBatch int
	videoSampleBatch int
	fingerprintPort  int
	subsWvttLangs    string
	subsStppLangs    string
	cencKey          string
	iv               string
	kid              string
	scheme           string
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
	fs.IntVar(&opts.audioSampleBatch, "audiobatch", 2, "Nr audio samples per MoQ object/CMAF chunk")
	fs.IntVar(&opts.videoSampleBatch, "videobatch", 1, "Nr video samples per MoQ object/CMAF chunk")
	fs.IntVar(&opts.fingerprintPort, "fingerprintport", 0, "Port for HTTP fingerprint server (0 to disable)")
	fs.StringVar(&opts.subsWvttLangs, "subswvtt", "sv", "Comma-separated WVTT subtitle languages (e.g. 'en,sv')")
	fs.StringVar(&opts.subsStppLangs, "subsstpp", "en", "Comma-separated STPP subtitle languages (e.g. 'en,sv')")
	fs.StringVar(&opts.kid, "kid", "", "key id for CENC encryption (32 hex or 24 base64 chars)")
	fs.StringVar(&opts.iv, "iv", "", "IV for CENC encryption (16 or 32 hex chars)")
	fs.StringVar(&opts.cencKey, "cenckey", "", "Key for CENC encryption (32 hex or 24 base64 chars)."+
		"If no key is specified the key id will be used as the key.")
	fs.StringVar(&opts.scheme, "scheme", "cenc", "Scheme for CENC encryption."+
		"Either \"cenc\" or \"cbcs\". Default is cenc")
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

func runServer(opts *options) error {
	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}
	tlsConfig, err := generateTLSConfigWithCertAndKey(opts.certFile, opts.keyFile)
	if err != nil {
		slog.Warn("failed to generate TLS config from cert file and key, generating in memory certs", "error", err)
		tlsConfig, err = generateTLSConfig()
		if err != nil {
			slog.Error("failed to generate in-memory TLS config", "error", err)
			return err
		}
	}
	cencInfo, err := internal.ParseCENCflags(opts.scheme, opts.kid, opts.cencKey, opts.iv)
	if err != nil {
		return err
	}
	asset, err := internal.LoadAssetWithCENCInfo(opts.asset, opts.audioSampleBatch, opts.videoSampleBatch, cencInfo)
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

	catalog, err := asset.GenCMAFCatalogEntry(time.Now().UnixMilli())
	if err != nil {
		return err
	}

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
	h := &moqHandler{
		addr:            opts.addr,
		tlsConfig:       tlsConfig,
		namespace:       []string{internal.Namespace},
		asset:           asset,
		catalog:         catalog,
		logfh:           logfh,
		fingerprintPort: opts.fingerprintPort,
	}

	return h.runServer(context.TODO())
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

func generateTLSConfigWithCertAndKey(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"moq-00", "h3"},
	}, nil
}

// Setup a bare-bones TLS config for the server
// Generates a certificate that meets WebTransport fingerprint requirements
func generateTLSConfig() (*tls.Config, error) {
	// Generate ECDSA key (required for WebTransport fingerprints)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	// Create certificate template with WebTransport-compatible settings
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		Issuer: pkix.Name{
			CommonName: "localhost", // Explicitly set issuer = subject for self-signed
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(14 * 24 * time.Hour), // 14 days max for WebTransport
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // Self-signed CA
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:              []string{"localhost", "127.0.0.1"}, // Include IP as DNS too
	}

	// Create self-signed certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	// Encode key and certificate to PEM
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	// Parse the generated certificate to get fingerprint
	parsedCert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err == nil {
		fingerprint := sha256.Sum256(parsedCert.Raw)
		slog.Info("Generated WebTransport-compatible certificate",
			"algorithm", "ECDSA",
			"validity_days", 14,
			"self_signed", true,
			"fingerprint", hex.EncodeToString(fingerprint[:]),
			"subject", parsedCert.Subject.String(),
			"issuer", parsedCert.Issuer.String())
	} else {
		slog.Info("Generated WebTransport-compatible certificate",
			"algorithm", "ECDSA",
			"validity_days", 14,
			"self_signed", true)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"moq-00", "h3"},
	}, nil
}
