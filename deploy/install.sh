#!/usr/bin/env bash
# Install dvbhub as a systemd service. Run as root on the TV server:
#   sudo ./install.sh [path/to/dvbhub-binary]
# Re-running upgrades the binary and keeps all configuration in /var/lib/dvbhub.
set -euo pipefail

BIN=${1:-./dvbhub}
PORT=${DVBHUB_PORT:-9980}

[[ $EUID -eq 0 ]] || { echo "run as root (sudo $0)"; exit 1; }
[[ -x "$BIN" ]] || { echo "binary not found: $BIN (build with: go build -o dvbhub ./cmd/dvbhub)"; exit 1; }

groups="video"
getent group render >/dev/null && groups="$groups,render"
if ! id dvbhub >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin --groups "$groups" dvbhub
  echo "created user dvbhub (groups: $groups)"
else
  usermod -aG "$groups" dvbhub
fi

systemctl stop dvbhub 2>/dev/null || true
install -m 0755 "$BIN" /usr/local/bin/dvbhub

cat > /etc/systemd/system/dvbhub.service <<EOF
[Unit]
Description=dvbhub TV tuner server
After=network-online.target
Wants=network-online.target

[Service]
User=dvbhub
ExecStart=/usr/local/bin/dvbhub -data /var/lib/dvbhub -listen :$PORT
StateDirectory=dvbhub
Restart=on-failure
RestartSec=3
# Uncomment to password-protect the web page (Jellyfin's streams and lineups stay open):
#Environment=DVBHUB_PASSWORD=change-me

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now dvbhub

echo
ls /dev/dvb/adapter*/frontend* 2>/dev/null | sed 's/^/tuner: /' || echo "WARNING: no DVB tuners found in /dev/dvb (driver/firmware missing?)"
command -v ffmpeg >/dev/null || echo "WARNING: ffmpeg is not installed: only the Original (passthrough) quality works (sudo apt install ffmpeg)"
command -v nvidia-smi >/dev/null && nvidia-smi --query-gpu=name --format=csv,noheader | sed 's/^/NVIDIA GPU: /'
ls /dev/dri/renderD* >/dev/null 2>&1 && echo "GPU render device found: Intel/AMD transcoding can be used"
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "dvbhub is running: http://${ip:-<server>}:$PORT/"
echo "logs: journalctl -u dvbhub -f"
