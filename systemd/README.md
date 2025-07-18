# Systemd Service for MoqLiveMock

This directory contains the systemd service file for running mlmpub as a system service.

## Features

1. **Configurable variables** via environment variables:
   - `MLMPUB_ADDR` - Server address (defaults to `localhost:4443`)
   - `MLMPUB_CERT` - Certificate file path (defaults to `/etc/moqlivemock/cert.pem`)
   - `MLMPUB_KEY` - Key file path (defaults to `/etc/moqlivemock/key.pem`)
   - `MLMPUB_ASSET` - Asset/content directory path (defaults to `/var/moqlivemock/assets/test10s`)

2. **Environment file support** - You can override settings by creating `/etc/moqlivemock/moqlivemock.env`:
   ```bash
   MLMPUB_ADDR=0.0.0.0:4443
   MLMPUB_CERT=/path/to/custom/cert.pem
   MLMPUB_KEY=/path/to/custom/key.pem
   MLMPUB_ASSET=/path/to/custom/assets/test10s
   ```

3. **Security hardening** with restricted privileges and filesystem access
4. **Automatic restart** on failure with 10-second delay
5. **Journal logging** integration

## Installation

1. Build and install the mlmpub binary:
   ```bash
   cd ../cmd/mlmpub
   go build
   sudo cp mlmpub /usr/local/bin/
   ```

2. Create a system user for the service:
   ```bash
   sudo useradd -r -s /bin/false moqlivemock
   ```

3. Create the required directories:
   ```bash
   sudo mkdir -p /etc/moqlivemock
   sudo mkdir -p /var/log/moqlivemock
   sudo mkdir -p /var/moqlivemock/assets/test10s
   sudo chown moqlivemock:moqlivemock /var/log/moqlivemock
   sudo chown -R moqlivemock:moqlivemock /var/moqlivemock/assets
   ```

4. Copy your content files to the asset directory:
   ```bash
   # Copy the media files from the moqlivemock/assets/test10s directory
   # This includes the video (400/600/900 kbps) and audio (monotonic/scale) test files
   sudo cp -r ../assets/test10s/* /var/moqlivemock/assets/test10s/
   sudo chown -R moqlivemock:moqlivemock /var/moqlivemock/assets
   ```

5. Copy your certificates to the configuration directory:
   ```bash
   sudo cp cert.pem /etc/moqlivemock/
   sudo cp key.pem /etc/moqlivemock/
   sudo chown moqlivemock:moqlivemock /etc/moqlivemock/*.pem
   sudo chmod 640 /etc/moqlivemock/*.pem
   ```

6. Install the systemd service file:
   ```bash
   sudo cp moqlivemock.service /etc/systemd/system/
   sudo systemctl daemon-reload
   ```

7. Enable and start the service:
   ```bash
   sudo systemctl enable moqlivemock
   sudo systemctl start moqlivemock
   ```

## Management

Check service status:
```bash
sudo systemctl status moqlivemock
```

View logs:
```bash
sudo journalctl -u moqlivemock -f
```

Restart service:
```bash
sudo systemctl restart moqlivemock
```

Stop service:
```bash
sudo systemctl stop moqlivemock
```

## Custom Configuration

To use custom settings, create `/etc/moqlivemock/moqlivemock.env`:
```bash
# Listen on all interfaces
MLMPUB_ADDR=0.0.0.0:4443

# Custom certificate paths
MLMPUB_CERT=/opt/moqlivemock/certs/server.crt
MLMPUB_KEY=/opt/moqlivemock/certs/server.key

# Custom asset directory
MLMPUB_ASSET=/opt/moqlivemock/media
```

Then restart the service:
```bash
sudo systemctl restart moqlivemock
```

## Automatic Certificate Updates from Caddy

If you're using Caddy server to manage Let's Encrypt certificates, you can automate certificate updates using the included `update-certs.sh` script.

### Setup

1. Install the update script:
   ```bash
   sudo cp update-certs.sh /usr/local/bin/moqlivemock-update-certs
   sudo chmod +x /usr/local/bin/moqlivemock-update-certs
   ```

2. Test the script with your domain:
   ```bash
   sudo DOMAIN=moqlivemock.demo.osaas.io /usr/local/bin/moqlivemock-update-certs
   ```

3. Add a cron job to run daily at 3:30 AM:
   ```bash
   sudo crontab -e
   ```

   Add the following line:
   ```cron
   30 3 * * * DOMAIN=moqlivemock.demo.osaas.io /usr/local/bin/moqlivemock-update-certs
   ```

### Script Configuration

The script uses the following environment variable:
- `DOMAIN` - The domain name for your certificates (default: `example.com`)

The script will:
1. Copy certificates from Caddy's certificate directory
2. Set proper ownership and permissions for the moqlivemock user
3. Restart the moqlivemock service if it's running
4. Log all operations to `/var/log/moqlivemock/cert-update.log`

### Certificate Paths

Caddy stores certificates in:
```
/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/{DOMAIN}/
```

The script copies them to:
- `/etc/moqlivemock/cert.pem` (certificate)
- `/etc/moqlivemock/key.pem` (private key)

### Monitoring

Check the update logs:
```bash
sudo tail -f /var/log/moqlivemock/cert-update.log
```

### Manual Update

To manually update certificates:
```bash
sudo DOMAIN=your.domain.com /usr/local/bin/moqlivemock-update-certs
```