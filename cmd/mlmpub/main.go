package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"

	"github.com/Eyevinn/moqlivemock/internal"
)

const (
	appName = "mlmpub"
)

var usg = `%s acts as a MoQ server and publisher using WARP to send
mocked live video and audio tracks, synchronized with wall-clock time.
It is intended to be a test-bed for MoQ and WARP.

The qlog logs are currently massive, and written to 

Usage of %s:
`

const (
	defaultQlogFileName = "mlmpub.log"
)

type options struct {
	certFile string
	keyFile  string
	addr     string
	asset    string
	qlogfile string
	version  bool
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.certFile, "cert", "localhost.pem", "TLS certificate file (only used for server)")
	fs.StringVar(&opts.keyFile, "key", "localhost-key.pem", "TLS key file (only used for server)")
	fs.StringVar(&opts.addr, "addr", "localhost:8080", "listen or connect address")
	fs.StringVar(&opts.asset, "asset", "../../content", "Asset to serve")
	fs.StringVar(&opts.qlogfile, "qlog", defaultQlogFileName, "qlog file to write to. Use '-' for stderr")
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
	asset, err := internal.LoadAsset(opts.asset)
	if err != nil {
		return err
	}
	catalog, err := asset.GenCMAFCatalogEntry()
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
		addr:      opts.addr,
		tlsConfig: tlsConfig,
		namespace: []string{internal.Namespace},
		asset:     asset,
		catalog:   catalog,
		logfh:     logfh,
	}

	return h.runServer(context.TODO())
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
func generateTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"moq-00", "h3"},
	}, nil
}
