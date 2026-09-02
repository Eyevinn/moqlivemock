#!/bin/bash
# entrypoint-relay.sh - wrapper for mlmrel in the moq-interop-runner
# Translates the runner's MOQT_* environment variables to mlmrel flags.
#
# Expected environment:
#   MOQT_ROLE     - Role to run: relay (required, only relay supported)
#   MOQT_PORT     - UDP port to listen on (default: 4443)
#   MOQT_CERT     - TLS certificate path (default: /certs/cert.pem)
#   MOQT_KEY      - TLS private key path (default: /certs/priv.key)
#   MOQT_MLOG_DIR - Directory for mlog/qlog files (default: /mlog)
#
# Expected mounts:
#   /certs/cert.pem - TLS certificate
#   /certs/priv.key - TLS private key
#
# Exit codes:
#   0   - Clean shutdown (SIGTERM)
#   1   - Configuration error
#   127 - Unsupported role

set -euo pipefail

ROLE="${MOQT_ROLE:-relay}"
PORT="${MOQT_PORT:-4443}"
CERT="${MOQT_CERT:-/certs/cert.pem}"
KEY="${MOQT_KEY:-/certs/priv.key}"
MLOG_DIR="${MOQT_MLOG_DIR:-/mlog}"

case "$ROLE" in
  relay)
    echo "Starting mlmrel on port $PORT"
    echo "  Cert: $CERT"
    echo "  Key:  $KEY"
    echo "  Mlog: $MLOG_DIR"

    if [ ! -f "$CERT" ]; then
      echo "ERROR: Certificate not found at $CERT" >&2
      echo "Make sure /certs is mounted with cert.pem and priv.key" >&2
      exit 1
    fi
    if [ ! -f "$KEY" ]; then
      echo "ERROR: Private key not found at $KEY" >&2
      exit 1
    fi

    # The runner bind-mounts the mlog directory from the host and runs the
    # container as uid 1000, so on a host where the directory belongs to
    # another user it is read-only for us. A log destination must not keep
    # the relay from starting: fall back to stderr, which docker captures.
    if [ -w "$MLOG_DIR" ]; then
      QLOG="$MLOG_DIR/mlmrel_server.mlog"
    else
      echo "WARNING: $MLOG_DIR is not writable; qlog goes to stderr" >&2
      QLOG="-"
    fi

    # -pending-wait lets subscribe-before-announce succeed: the SUBSCRIBE
    # arrives before the publisher's PUBLISH_NAMESPACE and is held briefly
    # instead of being rejected outright. -upstream-timeout keeps a publisher
    # that never answers the forwarded SUBSCRIBE from taking the subscriber
    # down with it: that case gives the whole exchange 3.5 s and accepts a
    # REQUEST_ERROR, so the answer has to come well inside that.
    exec mlmrel \
      -addr "0.0.0.0:$PORT" \
      -cert "$CERT" \
      -key "$KEY" \
      -pending-wait 1s \
      -upstream-timeout 1s \
      -qlog "$QLOG"
    ;;

  *)
    echo "Role '$ROLE' not supported by mlmrel" >&2
    echo "Supported roles: relay" >&2
    exit 127
    ;;
esac
