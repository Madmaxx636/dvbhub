# dvbhub

A TV tuner server for Jellyfin (and Plex): one small Go program plus ffmpeg. It replaces Tvheadend for a Jellyfin setup.

- **Find channels in one click.** Pick "Antenna — USA / Canada", "Cable — USA", "Antenna — Europe" or "Antenna — Australia". dvbhub scans every channel, skips empty ones quickly and adds what it finds. ATSC channel names and numbers (like 7.1) come from the stations. Satellite and DVB-C work by importing a scan table.
- **Channels:** rename, renumber, hide, set a quality per channel, and give a channel backup services. If a station comes in on two frequencies, the weaker one becomes the backup automatically.
- **Signal bars everywhere:** frequencies, stations, channels and tuners, plus a 5-minute history and an antenna alignment tool with an optional tone.
- **TV guide** from the broadcast (ATSC PSIP and DVB EIT), plus optional XMLTV files, sent to Jellyfin as XMLTV.
- **Passthrough or transcoding:** "Original" sends the broadcast untouched with almost no CPU. "Convert MPEG-2 only" converts US antenna channels to H.264 so phones and browsers can play them. Full profiles set codec (H.264/HEVC/AV1), resolution, bitrate (VBR, CBR or constant quality), peak bitrate, buffer, audio and more. GPUs are tested on startup (NVIDIA NVENC, Intel Quick Sync, AMD/Intel VAAPI), and if one fails mid-stream dvbhub falls back to the CPU.
- **Signal loss doesn't drop viewers:** the connection is kept open while the tuner re-tunes, and dvbhub switches to a backup service on another tuner when one exists.
- **Hardware and drivers:** finds USB and PCIe tuners and their drivers, and spots firmware a driver actually failed to load. It can install firmware, reset USB tuners and build TBS drivers from the web page or Jellyfin, after a one-time setup on the host.
- **Simple, Advanced and Pro:** one set of settings; the level only changes how much is shown, so nothing set in Pro is undone in Simple.
- **Jellyfin plugin** with the same controls, inside Jellyfin's dashboard (works from a phone), plus one-click "Add to Live TV". Builds for Jellyfin 10.10, 10.11 and 12.x.
- **Light on resources:** a single process, about 20 MB of memory when idle. Passthrough uses almost no CPU.

## Install with Docker

On the server, run these one at a time.

```bash
mkdir ~/dvbhub
```

```bash
cd ~/dvbhub
```

```bash
curl -fsSLO https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/docker-compose.yml
```

```bash
docker compose up -d
```

Then open `http://<server>:9980/` in a browser.

To update later, run these from `~/dvbhub`:

```bash
docker compose pull
```

```bash
docker compose up -d
```

[docker-compose.yml](docker-compose.yml) uses host networking (so Jellyfin finds dvbhub by itself), keeps everything in `./data`, and gives dvbhub the TV tuners and Intel/AMD GPUs. **NVIDIA GPU:** install the NVIDIA Container Toolkit on the host, then uncomment the `deploy:` block at the end of the file.

## First steps

1. **Scan:** choose where your TV comes from and press **Start scan**. Found channels are added automatically.
2. **Jellyfin:** install the plugin (below) and press **Add dvbhub to Live TV**. Or add it by hand: Jellyfin **Dashboard → Live TV → Tuner Devices → + → HDHomeRun** with `http://<server>:9980`, then **TV Guide Data Providers → + → XMLTV** with `http://<server>:9980/xmltv.xml`.
3. **Streaming** (optional): pick the quality Jellyfin gets. Each quality profile is also its own Jellyfin tuner address (`http://<server>:9980/p/<profile>`), so you can have an original and a converted version side by side.

## Jellyfin plugin

1. In Jellyfin open **Dashboard → Plugins → Catalog → ⚙ (Repositories) → +**. Name it `dvbhub` and enter `https://github.com/Madmaxx636/dvbhub/releases/latest/download/manifest.json`.
2. In **Catalog**, open **dvbhub → Install**, then restart Jellyfin.
3. Open **Dashboard → TV Tuners (dvbhub)**, enter dvbhub's address as the Jellyfin server sees it (for example `http://192.168.1.10:9980`) and press **Save and connect**.

The plugin loads dvbhub's own interface through Jellyfin, so it has every control and always matches your dvbhub version. All calls go through Jellyfin's admin login, so dvbhub doesn't need to be reachable from your phone.

## Drivers and firmware

The **Tuners** page shows each tuner device, its driver, and firmware a driver failed to load. To let dvbhub install firmware for you (from the web page or Jellyfin), run this one-time setup on the host. With Docker, run it in the folder with `docker-compose.yml`:

```bash
curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh -o install-drivers.sh
```

```bash
sudo bash install-drivers.sh --enable-web-trigger ./data
```

(Without Docker, use `/var/lib/dvbhub` instead of `./data`.) This installs a root systemd unit that only accepts four fixed actions written by dvbhub: `firmware`, `replug` (reset USB tuners), `tools` and `tbs`. dvbhub itself never runs anything as root. Turn it off with `--disable-web-trigger`. You can also run the actions by hand, for example `sudo bash install-drivers.sh --firmware`.

## Install without Docker

```bash
go build -o dvbhub ./cmd/dvbhub
```

```bash
sudo deploy/install.sh ./dvbhub
```

This creates a `dvbhub` system user (in the `video` and `render` groups), installs a systemd service and keeps data in `/var/lib/dvbhub`. Install `ffmpeg` for transcoding. Set `DVBHUB_PASSWORD` to protect the web page; Jellyfin's streams and lineups stay open.

## For scripts

- `GET /api/signal`: every tuner's signal as JSON.
- `GET /api/status`: tuners, viewers, scans and guide.
- HDHomeRun: `/discover.json`, `/lineup.json`, `/auto/v<number>`. M3U: `/playlist.m3u`. Guide: `/xmltv.xml`.

## Development and tests

- `bash tools/e2e.sh` generates test streams with ffmpeg, runs dvbhub on virtual (file-backed) tuners and checks scanning, mapping, HDHomeRun, streaming, signal loss and failover, recovery, CBR transcoding, "Convert MPEG-2 only", the guide (DVB and ATSC), alignment and the installer bridge. GitHub Actions runs it on every push. `bash tools/e2e.sh keep` leaves a demo server running.
- `python3 tools/plugin-preview.py http://127.0.0.1:9980` previews the Jellyfin plugin page without Jellyfin at `http://127.0.0.1:8097/`.
- The plugin builds with `dotnet build jellyfin-plugin -p:JellyfinTfm=net10.0 -p:JellyfinVersion=12.*` (or `net9.0`/`10.11.*`, `net8.0`/`10.10.*`).

## Not verified yet

Real tuner hardware (tuning, signal statistics, DiSEqC, exclusive hold), GPU transcoding on NVIDIA and AMD, the host installer's root actions, and the plugin inside a real Jellyfin server have not been run by these tests yet. The tests use virtual tuners and the plugin preview.
