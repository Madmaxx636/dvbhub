# dvbhub

A DVB tuner server that replaces Tvheadend for a Jellyfin (or Plex) setup: a single Go binary with no dependencies beyond ffmpeg.

- **Tuners:** DVB-T/T2, DVB-C and DVB-S/S2 through the Linux DVB API v5, with universal LNB and DiSEqC 1.0 support. Scan tables use the dtv-scan-tables (dvbv5) or legacy format, and new muxes are discovered from the NIT.
- **Channels:** services come from PAT/PMT/SDT, and logical channel numbers from the NIT. A channel can list backup services on other muxes or tuners, and mapping services with the same name onto one channel creates those backups automatically.
- **Signal-loss handling:** clients are never disconnected because of a signal drop. They receive PAT and null packets while the tuner is re-tuned, and a channel moves to its backup service on another tuner after 8 s.
- **Signal monitoring:** each tuner shows strength (% and dBm), SNR in dB, BER, uncorrected blocks, continuity errors, bitrate, a one-word verdict and a 5-minute history. `GET /api/signal` returns the same data as compact JSON for scripts.
- **Transcoding profiles:** passthrough, NVENC H.264/HEVC/AV1 with NVDEC decode and CUDA deinterlace/scale, or x264/x265. Each profile has CBR/VBR/CQ rate control, target, peak and VBV buffer settings, resolution, frame rate and audio settings. ffmpeg restarts automatically and falls back to CPU decode if NVDEC fails. GPU load comes from `nvidia-smi`.
- **Jellyfin integration:** HDHomeRun emulation, plus M3U and an XMLTV export. Every profile gets its own tuner URL (`/p/<profile>`), so Jellyfin can have both a passthrough tuner and an NVENC tuner.
- **EPG:** over-the-air EIT (present/following and schedule, including other muxes, plus periodic grabs from idle muxes) and XMLTV import, with automatic name matching.
- **Signal bars everywhere:** every scan records a signal reading per mux, so muxes, services and channels all show phone-style bars. Anything being received shows live bars.
- **Exclusive hold:** a tuner can be kept open by dvbhub so Tvheadend or other programs can't grab it. LNB power is switched off while idle. The UI shows which other programs have an adapter open.
- **Signal lock / antenna alignment:** hold one tuner on one mux and read the signal 4× a second. There's a large SNR readout, lock indicator, peak hold, and an optional tone whose pitch rises with quality.
- **Hardware & drivers:** detects USB/PCI tuners from any brand, whether a driver is bound, whether a DVB adapter was created, missing firmware (from each module's firmware list and the kernel log), blacklisted modules and Secure Boot. A host installer handles firmware and the TBS driver build, and can optionally be triggered from the UI or Jellyfin.
- **Jellyfin plugin:** full control from Jellyfin's dashboard, including tuners with bars, channels, scanning, alignment, transcoding, hardware and one-click Live TV setup.
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

1. **Networks:** add a network (DVB-T/C/S). Import a scan table, or add a mux by hand; scanning starts automatically.
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

## Jellyfin plugin

Control dvbhub from Jellyfin's dashboard, in any browser including a phone, with no terminal needed. The page shows tuners with signal bars, viewers you can stop, channels (rename, renumber, profile), scanning, antenna alignment, transcoding profiles and bitrates, hardware/drivers, and one-click **Add dvbhub to Live TV**. All calls go through Jellyfin, which checks for an admin login, so dvbhub itself doesn't need to be reachable from your phone.

Install:
1. Jellyfin → **Dashboard → Plugins → Repositories (Catalog ⚙) → +**. Name it `dvbhub` and set the URL to `https://github.com/Madmaxx636/dvbhub/releases/latest/download/manifest.json`.
2. **Catalog → dvbhub → Install**, then restart Jellyfin.
3. **Dashboard → TV Tuners (dvbhub) → Setup:** enter dvbhub's URL as seen from the Jellyfin server, e.g. `http://192.168.1.10:9980`, then click **Add dvbhub to Live TV**.

Builds for Jellyfin 10.10 and 10.11 are published on every `v*` tag. Jellyfin picks the right one automatically. `python3 tools/plugin-preview.py` previews the plugin page against a running dvbhub without Jellyfin.

## Drivers & firmware

The **Hardware** page (and the plugin's Hardware tab) diagnoses tuners. To install firmware on the host:

```bash
curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh | sudo bash -s -- --firmware
```

Other actions:
- `--detect`: diagnosis only.
- `--tbs`: builds TBS's open-source drivers for the running kernel. Re-run it after kernel updates. It needs Secure Boot off or module signing.
- `--tools`: installs dvbv5-scan, dvb-fe-tool and similar tools.

To run these from the web UI or Jellyfin instead of a terminal, enable the web trigger once:

```bash
curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh | sudo bash -s -- --enable-web-trigger /var/lib/dvbhub
```

For Docker, use the `./data` folder next to `docker-compose.yml` instead of `/var/lib/dvbhub`. This installs a root systemd path unit that only accepts the fixed actions `firmware`, `tbs` and `tools`, written as request files into dvbhub's data folder. dvbhub itself never runs anything as root. Undo it with `--disable-web-trigger`.

## Testing without hardware

`tools/e2e.sh` generates test streams with ffmpeg and injects NIT and EIT tables with `tools/tsinject`. It then runs the whole server on virtual file-backed tuners and covers: scan, mapping, HDHomeRun, M3U, XMLTV, passthrough, signal drop with keepalive, re-tune, failover, CBR transcoding, signal bars, alignment mode and the installer bridge (39 checks). GitHub Actions runs it on every push. `tools/demo.sh` starts a demo instance with live viewers. On a virtual tuner, the dashboard's **Drop 5 s / Drop 20 s** buttons simulate signal loss.

## Not verified yet

The hardware code paths (`internal/dvb`, exclusive hold, driver detection on real tuners), the host installer's `--firmware`/`--tbs` actions, NVENC, and the plugin inside a real Jellyfin have not been run on real hardware or a real Jellyfin server yet. The ioctl numbers and struct layouts were checked against the kernel headers, but real tuning, DiSEqC and signal statistics still need testing on the actual server. ATSC tuning is wired up, but channel names come from DVB SDT only, so ATSC PSIP names are not read.
