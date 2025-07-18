#!/bin/bash
# update-certs.sh - Copy certificates from Caddy to moqlivemock
# This script should be run as root or with sudo

# Configuration
DOMAIN="${DOMAIN:-example.com}"
CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/${DOMAIN}"
MOQLIVE_CERT_DIR="/etc/moqlivemock"
MOQLIVE_USER="moqlivemock"
MOQLIVE_GROUP="moqlivemock"

# Log file
LOG_FILE="/var/log/moqlivemock/cert-update.log"

# Function to log messages
log_message() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "This script must be run as root or with sudo"
    exit 1
fi

# Check if source certificates exist
if [ ! -f "${CADDY_CERT_DIR}/${DOMAIN}.crt" ] || [ ! -f "${CADDY_CERT_DIR}/${DOMAIN}.key" ]; then
    log_message "ERROR: Certificate files not found for domain ${DOMAIN}"
    log_message "Looking in: ${CADDY_CERT_DIR}"
    exit 1
fi

# Create destination directory if it doesn't exist
mkdir -p "${MOQLIVE_CERT_DIR}"
mkdir -p "$(dirname "$LOG_FILE")"

# Copy certificates
log_message "Copying certificates for ${DOMAIN}..."

if cp "${CADDY_CERT_DIR}/${DOMAIN}.crt" "${MOQLIVE_CERT_DIR}/cert.pem" && \
   cp "${CADDY_CERT_DIR}/${DOMAIN}.key" "${MOQLIVE_CERT_DIR}/key.pem"; then

    # Set proper ownership and permissions
    chown ${MOQLIVE_USER}:${MOQLIVE_GROUP} "${MOQLIVE_CERT_DIR}/cert.pem" "${MOQLIVE_CERT_DIR}/key.pem"
    chmod 640 "${MOQLIVE_CERT_DIR}/cert.pem" "${MOQLIVE_CERT_DIR}/key.pem"

    log_message "SUCCESS: Certificates copied and permissions set"

    # Restart moqlivemock service if it's running
    if systemctl is-active --quiet moqlivemock; then
        log_message "Restarting moqlivemock service..."
        systemctl restart moqlivemock
        if [ $? -eq 0 ]; then
            log_message "SUCCESS: moqlivemock service restarted"
        else
            log_message "ERROR: Failed to restart moqlivemock service"
            exit 1
        fi
    else
        log_message "INFO: moqlivemock service is not running, skipping restart"
    fi
else
    log_message "ERROR: Failed to copy certificates"
    exit 1
fi

log_message "Certificate update completed successfully"
exit 0