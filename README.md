# dvbhub

A DVB tuner server that replaces Tvheadend for a Jellyfin (or Plex) setup: a single Go binary with no dependencies beyond ffmpeg.

- **Tuners:** DVB-T/T2, DVB-C, DVB-S/S2 and ATSC through the Linux DVB API v5, with universal LNB and DiSEqC 1.0 support. Scan tables use the dtv-scan-tables (dvbv5) or legacy format, and new muxes are discovered from the NIT.
- **Channels:** services come from PAT/PMT/SDT, and logical channel numbers from the NIT. A channel can list backup services on other muxes or tuners, and mapping services with the same name onto one channel creates those backups automatically.
- **Signal-loss handling:** clients are never disconnected because of a signal drop. They receive PAT and null packets while the tuner is re-tuned, and a channel moves to its backup service on another tuner after 8 s.
- **Signal monitoring:** each tuner shows strength (% and dBm), SNR in dB, BER, uncorrected blocks, continuity errors, bitrate, a one-word verdict and a 5-minute history. `GET /api/signal` returns the same data as compact JSON for scripts.
- **Transcoding profiles:** passthrough, NVENC H.264/HEVC/AV1 with NVDEC decode and CUDA deinterlace/scale, or x264/x265. Each profile has CBR/VBR/CQ rate control, target, peak and VBV buffer settings, resolution, frame rate and audio settings. ffmpeg restarts automatically and falls back to CPU decode if NVDEC fails. GPU load comes from `nvidia-smi`.
- **Jellyfin integration:** HDHomeRun emulation, plus M3U and an XMLTV export. Every profile gets its own tuner URL (`/p/<profile>`), so Jellyfin can have both a passthrough tuner and an NVENC tuner.
- **EPG:** over-the-air EIT (present/following and schedule, including other muxes, plus periodic grabs from idle muxes) and XMLTV import, with automatic name matching.
- **Recording** is left to Jellyfin's DVR, which records through the HDHomeRun tuner.

## Build

```bash
go build -o dvbhub ./cmd/dvbhub
```

## Run

```bash
./dvbhub -data ~/.dvbhub -listen :9980
```

Open `http://<server>:9980/`, then:

1. **Networks:** add a network (DVB-T/C/S or ATSC). Import a scan table, or add a mux by hand; scanning starts automatically. For US over-the-air, pick ATSC and import `us-ATSC-center-frequencies-8VSB`; channel names and numbers (3.1, 3.2…) come from the station's PSIP virtual channel table.
2. **Services:** click **Map all**. With "Same name → failover" ticked, identical channels on different muxes become backups.
3. **Settings:** add XMLTV sources if you want them. Channels without an XMLTV id use the over-the-air EPG.

The user running dvbhub needs read/write access to `/dev/dvb/*`, which usually means membership of the `video` group.

## Install as a service

```bash
go build -o dvbhub ./cmd/dvbhub
sudo deploy/install.sh ./dvbhub
```

The script creates a `dvbhub` system user (in the `video`/`render` groups), installs the binary to `/usr/local/bin`, writes and starts a systemd unit, and keeps its data in `/var/lib/dvbhub`. Re-running it upgrades in place.

## Install with Docker

Every push to `main` is tested and then published by GitHub Actions to `ghcr.io/madmaxx636/dvbhub` (amd64 and arm64). On the server:

```bash
mkdir dvbhub && cd dvbhub
curl -fsSLO https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/docker-compose.yml
docker compose up -d
```

To update: `docker compose pull && docker compose up -d`. Tagging a release (`git tag v1.0.0 && git push --tags`) also publishes `:1.0.0` and `:1.0`, so you can pin a version.

[docker-compose.yml](docker-compose.yml) passes `/dev/dvb` through, stores its data in `./data`, uses host networking (for SSDP discovery) and reserves the NVIDIA GPU. For the GPU, install the NVIDIA Container Toolkit on the host first. If you have no NVIDIA GPU, delete the `deploy:` block. Set `TZ` to your time zone. After replugging a USB tuner, restart the container.

## Jellyfin

- **Dashboard → Live TV → Tuner Devices → Add → HD Homerun:** enter `http://<server>:9980`. For a GPU-transcoded variant, add another tuner with `http://<server>:9980/p/nvenc-h264-8m`.
- **TV Guide Data Providers → XMLTV:** `http://<server>:9980/xmltv.xml`. Channel ids are the channel numbers.
- **Recording:** use Jellyfin's own DVR. Recordings go through the same stream path, so signal drops don't end them.

## Testing without hardware

`tools/e2e.sh` generates test streams with ffmpeg and injects NIT and EIT tables with `tools/tsinject`. It then runs the whole server on virtual file-backed tuners and covers: scan, mapping, HDHomeRun, M3U, XMLTV, passthrough, signal drop with keepalive, re-tune, failover, and CBR transcoding (27 checks). `tools/demo.sh` starts a demo instance with live viewers. On a virtual tuner, the dashboard's **Drop 5 s / Drop 20 s** buttons simulate signal loss.

## Not verified yet

The hardware code paths (`internal/dvb`) and NVENC have not been run against real hardware. The ioctl numbers and struct layouts were checked against the kernel headers, but real tuning, DiSEqC and signal statistics still need testing on the actual server. ATSC channel names, numbers and the over-the-air program guide (PSIP EIT/ETT) are read from the broadcast.
