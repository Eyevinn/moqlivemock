#!/bin/bash

# Generate WebTransport-compatible certificate with fingerprint
# Requirements: ECDSA, ≤14 days validity, self-signed

echo "Generating WebTransport-compatible certificate..."

# Generate ECDSA private key
openssl ecparam -genkey -name prime256v1 -out key-fp.pem

# Generate self-signed certificate valid for 14 days
openssl req -new -x509 -key key-fp.pem -out cert-fp.pem -days 14 \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:127.0.0.1,IP:127.0.0.1,IP:::1"

echo "Certificate generated: cert-fp.pem"
echo "Private key generated: key-fp.pem"
echo ""

# Verify the certificate
echo "Certificate details:"
openssl x509 -in cert-fp.pem -text -noout | grep -E "Signature Algorithm:|Not Before:|Not After:|Subject Alternative Name" -A1

echo ""
echo "Certificate fingerprint (SHA-256):"
# Get the fingerprint
openssl x509 -in cert-fp.pem -noout -fingerprint -sha256 | sed 's/://g' | cut -d'=' -f2 | tr '[:upper:]' '[:lower:]'