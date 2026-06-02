#!/bin/bash
#
# Run mlmpub with the HTTP side server enabled (serves /fingerprint
# and /clearkey) and ClearKey/ECCP encryption configured.
#
# When the -cert/-key files cannot be loaded, mlmpub auto-generates
# an in-memory ECDSA P-256 certificate that satisfies the
# WebTransport fingerprint requirements (Chrome, Firefox, Safari).
# The certificate's SHA-256 fingerprint is logged at startup and
# served at http://localhost:${SIDE_PORT}/fingerprint for clients
# that pin against the fingerprint instead of the system trust store.
#
# ClearKey is enabled with the standard test KID/IV from the
# moqlivemock README, using the cbcs scheme. The browser-side player
# fetches the key from http://localhost:${SIDE_PORT}/clearkey
# (the laurl fallback in mlmpub).
#
# Run from workspace root:
#   ./moqlivemock/cmd/mlmpub/run_mlmpub_fingerprint.sh [extra mlmpub flags]
#
# Environment overrides:
#   ASSET, ADDR, SIDE_PORT, KID, IV, SCHEME, SLOG_LEVEL, MOQ_LOG_LEVEL

set -eu

export SLOG_LEVEL=${SLOG_LEVEL:-DEBUG}
export MOQ_LOG_LEVEL=${MOQ_LOG_LEVEL:-DEBUG}

ASSET=${ASSET:-moqlivemock/assets/test10s}
SIDE_PORT=${SIDE_PORT:-8081}
ADDR=${ADDR:-0.0.0.0:4443}
KID=${KID:-39112233445566778899aabbccddeeff}
IV=${IV:-41112233445566778899aabbccddeeff}
SCHEME=${SCHEME:-cbcs}

# Force the in-memory ECDSA fallback by pointing at non-existent
# files: any mkcert RSA cert lying around in cwd would otherwise be
# loaded and fail the WebTransport fingerprint constraint.
go run github.com/Eyevinn/moqlivemock/cmd/mlmpub \
  -asset "$ASSET" \
  -addr "$ADDR" \
  -sideport "$SIDE_PORT" \
  -kid "$KID" \
  -iv "$IV" \
  -scheme "$SCHEME" \
  -cert /nonexistent.pem -key /nonexistent.pem \
  "$@"
