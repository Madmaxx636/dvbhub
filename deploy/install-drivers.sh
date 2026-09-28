#!/usr/bin/env bash
# dvbhub driver & firmware installer. Runs on the HOST (not in Docker), as root.
#
#   sudo ./install-drivers.sh --detect              # show tuners, drivers, missing firmware
#   sudo ./install-drivers.sh --firmware            # distro firmware + LibreELEC DVB firmware collection
#   sudo ./install-drivers.sh --tbs                 # build + install TBS open-source drivers (advanced)
#   sudo ./install-drivers.sh --tools               # dvbv5-scan, dvb-fe-tool etc.
#   sudo ./install-drivers.sh --enable-web-trigger /var/lib/dvbhub   # let dvbhub's UI/Jellyfin plugin run the above
#   sudo ./install-drivers.sh --disable-web-trigger /var/lib/dvbhub
#
# One-liner:  curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh | sudo bash -s -- --firmware
set -uo pipefail

VERSION=1
SELF_URL=https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh
INSTALLED_SELF=/usr/local/sbin/dvbhub-install-drivers
STATE_DIR=/var/lib/dvbhub-installer
LIBREELEC_URL=https://github.com/LibreELEC/dvb-firmware/archive/refs/heads/master.tar.gz
TBS_FW_URL=https://www.tbsdtv.com/download/document/linux/tbs-tuner-firmwares_v1.0.tar.bz2
TBS_SRC=/usr/local/src/dvbhub-tbs

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[33mWARNING: %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

need_root() { [[ $EUID -eq 0 ]] || die "run as root (sudo)"; }

distro() {
  . /etc/os-release 2>/dev/null || true
  case " ${ID:-} ${ID_LIKE:-} " in
    *" ubuntu "*|*" debian "*|*" raspbian "*) echo debian ;;
    *" fedora "*|*" rhel "*|*" centos "*) echo fedora ;;
    *" arch "*) echo arch ;;
    *" opensuse"*|*" suse "*) echo suse ;;
    *" alpine "*) echo alpine ;;
    *) echo unknown ;;
  esac
}

pkg_install() {
  case "$(distro)" in
    debian) DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@" ;;
    fedora) dnf install -y "$@" ;;
    arch)   pacman -S --needed --noconfirm "$@" ;;
    suse)   zypper --non-interactive install "$@" ;;
    alpine) apk add "$@" ;;
    *) warn "unknown distribution: install these packages yourself: $*"; return 1 ;;
  esac
}

pkg_refresh() {
  case "$(distro)" in
    debian) apt-get update -qq ;;
    arch)   pacman -Sy --noconfirm >/dev/null ;;
  esac
}

fetch() { # url dest
  if command -v curl >/dev/null; then curl -fsSL --retry 3 -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
}

# ---------------------------------------------------------------- detect
detect() {
  say "Kernel: $(uname -r)   Distribution: $(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
  if [[ -d /sys/firmware/efi ]] && command -v mokutil >/dev/null; then
    echo "Secure Boot: $(mokutil --sb-state 2>/dev/null | head -1)"
  fi
  say "DVB adapters (/dev/dvb)"
  ls -1 /dev/dvb/adapter*/frontend* 2>/dev/null || echo "  none"
  say "Candidate tuner devices"
  if command -v lsusb >/dev/null; then
    lsusb | grep -Ei '2040:|734c:|0572:|1f4d:|0ccd:|2013:|2304:|048d:|15f4:|1b80:|07ca:|185b:|0413:|0fe9:|187f:|0bda:283[28]|eb1a:|dvb|tv tuner|wintv|hauppauge|tbs' || true
  fi
  if command -v lspci >/dev/null; then
    lspci -nn | grep -Ei 'multimedia|tbs|544d:|hauppauge|dvbsky|digital devices|saa71|cx2388' || true
  fi
  say "Loaded DVB modules and firmware they reference"
  local missing=0
  for m in $(awk '{print $1}' /proc/modules | grep -E '^(dvb|si21|m88|mxl|tda|stv|cx2|em28|lgdt|af90|it913|rtl28|mn88|cxd28|ts2020|smipcie|ddbridge|tbs|saa71|au08|dw2102|dib|drx|smsusb|mantis|ngene)'); do
    for fw in $(modinfo -F firmware "$m" 2>/dev/null); do
      if ls /lib/firmware/"$fw"{,.xz,.zst} >/dev/null 2>&1; then
        echo "  ok       $fw ($m)"
      else
        echo "  MISSING  $fw ($m)"; missing=$((missing+1))
      fi
    done
  done
  [[ $missing -gt 0 ]] && echo "  -> run: sudo $0 --firmware"
  say "Recent kernel messages about firmware/DVB"
  dmesg 2>/dev/null | grep -iE 'firmware|dvb|frontend' | tail -15 || echo "  (dmesg not readable)"
  say "Blacklisted DVB modules"
  grep -rhE '^\s*blacklist\s+(dvb|rtl28|r820|e4000|fc00|si21|em28)' /etc/modprobe.d /lib/modprobe.d /usr/lib/modprobe.d 2>/dev/null || echo "  none"
}

# ---------------------------------------------------------------- firmware
install_firmware() {
  need_root
  say "Installing distribution firmware packages"
  pkg_refresh
  case "$(distro)" in
    debian)
      if ! pkg_install linux-firmware 2>/dev/null; then
        pkg_install firmware-linux-nonfree firmware-misc-nonfree ||
          warn "Debian needs the 'non-free-firmware' component in /etc/apt/sources.list for firmware packages"
      fi ;;
    fedora|arch|suse|alpine) pkg_install linux-firmware ;;
    *) warn "skipping distro firmware package on unknown distribution" ;;
  esac

  say "Adding DVB firmware from the LibreELEC collection (existing files are never overwritten)"
  command -v tar >/dev/null || pkg_install tar
  local tmp; tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  fetch "$LIBREELEC_URL" "$tmp/fw.tar.gz" || die "download failed: $LIBREELEC_URL"
  tar -xzf "$tmp/fw.tar.gz" -C "$tmp" || die "could not unpack firmware archive"
  local src; src=$(find "$tmp" -maxdepth 2 -type d -name firmware | head -1)
  [[ -n "$src" ]] || die "firmware directory not found in archive"
  mkdir -p "$STATE_DIR"
  local added=0
  while IFS= read -r -d '' f; do
    local rel=${f#"$src"/}
    if [[ ! -e /lib/firmware/$rel && ! -e /lib/firmware/$rel.xz && ! -e /lib/firmware/$rel.zst ]]; then
      install -D -m 0644 "$f" "/lib/firmware/$rel"
      echo "/lib/firmware/$rel" >> "$STATE_DIR/libreelec-added.txt"
      added=$((added+1))
    fi
  done < <(find "$src" -type f -print0)
  echo "Added $added firmware file(s)."
  reload_hint
}

reload_hint() {
  say "Done"
  echo "Firmware is loaded when the driver starts: unplug and replug USB tuners, or reboot for PCIe cards."
  echo "Then check again with: sudo $0 --detect   (or the Hardware page in dvbhub)"
  REBOOT=1
}

# ---------------------------------------------------------------- tbs
install_tbs() {
  need_root
  if [[ -d /sys/firmware/efi ]] && command -v mokutil >/dev/null && mokutil --sb-state 2>/dev/null | grep -qi enabled; then
    [[ "${FORCE:-0}" == 1 ]] || die "Secure Boot is enabled: self-built TBS modules won't load. Disable Secure Boot (or sign the modules) and re-run, or pass --force."
  fi
  say "Installing build dependencies"
  pkg_refresh
  local k; k=$(uname -r)
  case "$(distro)" in
    debian) pkg_install build-essential patchutils libproc-processtable-perl git wget bzip2 ca-certificates "linux-headers-$k" ;;
    fedora) pkg_install gcc make patchutils perl-Proc-ProcessTable perl-Digest-SHA git wget bzip2 "kernel-devel-$k" ;;
    arch)   pkg_install base-devel git wget perl-proc-processtable linux-headers ;;
    *) die "automatic TBS build is supported on Debian/Ubuntu, Fedora and Arch only" ;;
  esac
  [[ -d /lib/modules/$k/build ]] || die "kernel headers for $k are not installed"

  say "Fetching TBS driver sources"
  rm -rf "$TBS_SRC"; mkdir -p "$TBS_SRC"; cd "$TBS_SRC" || die "cannot enter $TBS_SRC"
  git clone --depth=1 https://github.com/tbsdtv/media_build.git || die "clone media_build failed"
  git clone --depth=1 https://github.com/tbsdtv/linux_media.git -b latest ./media || die "clone linux_media failed"

  say "Building (this takes 15-40 minutes)"
  cd media_build || die "no media_build"
  make dir DIR=../media && make allyesconfig && make -j"$(nproc)" || die "TBS driver build failed (see output above)"
  say "Installing modules"
  make install || die "make install failed"

  say "Installing TBS tuner firmware"
  local tmp; tmp=$(mktemp -d)
  if fetch "$TBS_FW_URL" "$tmp/tbs-fw.tar.bz2"; then
    tar -xjf "$tmp/tbs-fw.tar.bz2" -C /lib/firmware/ && echo "TBS firmware installed."
  else
    warn "could not download TBS firmware from $TBS_FW_URL (see https://github.com/tbsdtv/linux_media/wiki)"
  fi
  rm -rf "$tmp"
  echo "NOTE: these modules replace the kernel's media drivers and must be rebuilt after every kernel update (re-run --tbs)."
  reload_hint
}

# ---------------------------------------------------------------- tools
install_tools() {
  need_root
  pkg_refresh
  case "$(distro)" in
    debian) pkg_install dvb-tools ;;
    *) pkg_install v4l-utils ;;
  esac
  echo "Installed: try  dvb-fe-tool  and  dvbv5-scan"
}

# ---------------------------------------------------------------- web trigger
enable_web_trigger() {
  need_root
  local data=${1:-}
  [[ -n "$data" && -d "$data" ]] || die "usage: $0 --enable-web-trigger <dvbhub data dir>  (e.g. /var/lib/dvbhub, or the ./data folder next to docker-compose.yml)"
  data=$(cd "$data" && pwd)
  command -v systemctl >/dev/null || die "systemd is required for the web trigger"

  say "Installing $INSTALLED_SELF"
  if [[ -f "${BASH_SOURCE[0]:-}" && "${BASH_SOURCE[0]}" != "$INSTALLED_SELF" ]]; then
    install -m 0755 "${BASH_SOURCE[0]}" "$INSTALLED_SELF"
  elif [[ ! -f "${BASH_SOURCE[0]:-}" ]]; then
    fetch "$SELF_URL" "$INSTALLED_SELF.tmp" && install -m 0755 "$INSTALLED_SELF.tmp" "$INSTALLED_SELF" && rm -f "$INSTALLED_SELF.tmp" ||
      die "could not download installer from $SELF_URL"
  fi

  local owner; owner=$(stat -c %u:%g "$data")
  mkdir -p "$data/driver/requests" "$data/driver/jobs"
  chown "$owner" "$data/driver" "$data/driver/requests"
  chmod 0775 "$data/driver/requests"
  chmod 0755 "$data/driver/jobs"
  printf '{"host":"%s","version":"%s","installed":"%s"}\n' "$(hostname)" "$VERSION" "$(date -Is)" > "$data/driver/enabled"

  cat > /etc/systemd/system/dvbhub-driver.path <<EOF
[Unit]
Description=Watch for dvbhub driver install requests

[Path]
PathExistsGlob=$data/driver/requests/*.req
Unit=dvbhub-driver.service

[Install]
WantedBy=paths.target
EOF
  cat > /etc/systemd/system/dvbhub-driver.service <<EOF
[Unit]
Description=Run dvbhub driver install requests

[Service]
Type=oneshot
ExecStart=$INSTALLED_SELF --process-requests $data
TimeoutStartSec=3h
EOF
  systemctl daemon-reload
  systemctl enable --now dvbhub-driver.path
  say "Web trigger enabled"
  echo "dvbhub's Hardware page (and the Jellyfin plugin) can now run: firmware, tbs, tools."
  echo "Only these fixed actions are accepted. Disable with: sudo $INSTALLED_SELF --disable-web-trigger $data"
}

disable_web_trigger() {
  need_root
  systemctl disable --now dvbhub-driver.path 2>/dev/null || true
  rm -f /etc/systemd/system/dvbhub-driver.path /etc/systemd/system/dvbhub-driver.service
  systemctl daemon-reload
  [[ -n "${1:-}" ]] && rm -f "$1/driver/enabled"
  echo "Web trigger disabled."
}

process_requests() {
  need_root
  local data=${1:?data dir}
  shopt -s nullglob
  for req in "$data"/driver/requests/*.req; do
    local id; id=$(basename "$req" .req)
    [[ "$id" =~ ^[0-9-]{1,40}$ ]] || { rm -f "$req"; continue; }
    local action; action=$(head -c 32 "$req" | tr -cd 'a-z')
    rm -f "$req"
    local log="$data/driver/jobs/$id.log" status="$data/driver/jobs/$id.status"
    case "$action" in
      firmware|tbs|tools) ;;
      *) echo "rejected unknown action" > "$log"; echo "${action:-unknown} failed" > "$status"; continue ;;
    esac
    echo "$action running" > "$status"
    chmod 0644 "$status"
    if "$INSTALLED_SELF" "--$action" > "$log" 2>&1; then
      if [[ "$action" == tools ]]; then echo "$action ok" > "$status"; else echo "$action ok reboot" > "$status"; fi
    else
      echo "$action failed" > "$status"
    fi
    chmod 0644 "$log" "$status"
  done
}

# ---------------------------------------------------------------- main
REBOOT=0
FORCE=0
[[ " $* " == *" --force "* ]] && FORCE=1
case "${1:-}" in
  --detect)               detect ;;
  --firmware)             install_firmware ;;
  --tbs)                  install_tbs ;;
  --tools)                install_tools ;;
  --enable-web-trigger)   enable_web_trigger "${2:-}" ;;
  --disable-web-trigger)  disable_web_trigger "${2:-}" ;;
  --process-requests)     process_requests "${2:-}" ;;
  *) sed -n '2,12p' "$0" 2>/dev/null || echo "see $SELF_URL"; echo; detect ;;
esac
