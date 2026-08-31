package internal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"time"

	"github.com/Eyevinn/moqtransport"
)

// ServerTLSConfig builds the TLS config for a MoQ server (raw QUIC +
// WebTransport, so NextProtos carries both ALPN sets). It loads certFile and
// keyFile, and falls back to an in-memory self-signed certificate meeting the
// WebTransport serverCertificateHashes requirements when they cannot be read.
func ServerTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	tlsConfig, err := tlsConfigFromFiles(certFile, keyFile)
	if err != nil {
		slog.Warn("failed to generate TLS config from cert file and key, generating in memory certs", "error", err)
		return generateSelfSignedTLSConfig()
	}
	return tlsConfig, nil
}

func tlsConfigFromFiles(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   append(moqtransport.SupportedALPNs(), "h3"),
	}, nil
}

// generateSelfSignedTLSConfig sets up a bare-bones TLS config for the server.
// It generates a certificate that meets WebTransport fingerprint requirements.
func generateSelfSignedTLSConfig() (*tls.Config, error) {
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
		NextProtos:   append(moqtransport.SupportedALPNs(), "h3"),
	}, nil
}
