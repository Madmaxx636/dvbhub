#!/usr/bin/env bash
# dvbhub driver & firmware installer. Runs on the HOST (not inside Docker), as root.
#
#   sudo bash install-drivers.sh --detect            show tuners, drivers and missing firmware
#   sudo bash install-drivers.sh --firmware          distro firmware + LibreELEC DVB firmware collection
#   sudo bash install-drivers.sh --replug            reset USB tuners (like unplugging and replugging them)
#   sudo bash install-drivers.sh --tools             dvbv5-scan, dvb-fe-tool and friends
#   sudo bash install-drivers.sh --tbs               build + install TBS's open-source drivers (advanced)
#   sudo bash install-drivers.sh --enable-web-trigger <dvbhub data folder>
#                                                    let dvbhub's web page / Jellyfin plugin run the above
#   sudo bash install-drivers.sh --disable-web-trigger <dvbhub data folder>
#
# Download: curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh -o install-drivers.sh
set -uo pipefail

VERSION=2
SELF_URL=https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh
INSTALLED_SELF=/usr/local/sbin/dvbhub-install-drivers
STATE_DIR=/var/lib/dvbhub-installer
LIBREELEC_URL=https://github.com/LibreELEC/dvb-firmware/archive/refs/heads/master.tar.gz
TBS_FW_URL=https://www.tbsdtv.com/download/document/linux/tbs-tuner-firmwares_v1.0.tar.bz2
TBS_SRC=/usr/local/src/dvbhub-tbs
ACTIONS="firmware replug tools tbs"

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[33mWARNING: %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

need_root() { [[ $EUID -eq 0 ]] || die "run as root: sudo bash $0 $*"; }

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

# USB devices (sysfs paths) that look like TV tuners: a DVB driver is bound
# to one of their interfaces, or they created a /dev/dvb adapter.
usb_tuners() {
  local dev drv
  for dev in /sys/bus/usb/devices/*; do
    [[ -f $dev/idVendor ]] || continue
    for drv in "$dev":*/driver; do
      [[ -e $drv ]] || continue
      if basename "$(readlink -f "$drv")" | grep -qiE 'dvb|em28xx|cx231xx|au0828|dw2102|rtl28xxu|af9035|af9015|it913x|smsusb|pctv|tbs|si21|lgdt|mxl'; then
        echo "$dev"; break
      fi
    done
  done
}

# ---------------------------------------------------------------- detect
detect() {
  say "Kernel: $(uname -r)   Distribution: $(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
  if [[ -d /sys/firmware/efi ]] && command -v mokutil >/dev/null; then
    echo "Secure Boot: $(mokutil --sb-state 2>/dev/null | head -1)"
  fi
  say "DVB adapters (/dev/dvb)"
  ls -1 /dev/dvb/adapter*/frontend* 2>/dev/null || echo "  none"
  say "USB tuners"
  local d found=0
  for d in $(usb_tuners); do
    found=1
    echo "  $(cat "$d/manufacturer" 2>/dev/null) $(cat "$d/product" 2>/dev/null) ($(cat "$d/idVendor"):$(cat "$d/idProduct")) at $(basename "$d")"
  done
  [[ $found == 1 ]] || echo "  none with a DVB driver bound"
  if command -v lspci >/dev/null; then
    say "PCI multimedia devices"
    lspci -nn | grep -Ei 'multimedia|tbs|544d:|hauppauge|dvbsky|digital devices|saa71|cx2388' || echo "  none"
  fi
  say "Firmware the kernel failed to load (recent boot)"
  dmesg 2>/dev/null | grep -iE 'firmware.*(fail|not found)|direct firmware load' | tail -15 || echo "  none (or the kernel log needs sudo to read)"
  say "Blacklisted DVB modules"
  grep -rhE '^\s*blacklist\s+(dvb|rtl28|r820|e4000|fc00|si21|em28)' /etc/modprobe.d /lib/modprobe.d /usr/lib/modprobe.d 2>/dev/null || echo "  none"
}

# ---------------------------------------------------------------- firmware
install_firmware() {
  need_root --firmware
  say "Installing your distribution's firmware package"
  pkg_refresh
  case "$(distro)" in
    debian)
      if ! pkg_install linux-firmware 2>/dev/null; then
        pkg_install firmware-linux-nonfree firmware-misc-nonfree ||
          warn "Debian needs the 'non-free-firmware' component in /etc/apt/sources.list for firmware packages"
      fi ;;
    fedora|arch|suse|alpine) pkg_install linux-firmware ;;
    *) warn "skipping the distribution firmware package on an unknown distribution" ;;
  esac

  say "Adding DVB firmware from the LibreELEC collection (existing files are never replaced)"
  command -v tar >/dev/null || pkg_install tar
  local tmp; tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  fetch "$LIBREELEC_URL" "$tmp/fw.tar.gz" || die "download failed: $LIBREELEC_URL"
  tar -xzf "$tmp/fw.tar.gz" -C "$tmp" || die "could not unpack the firmware archive"
  local src; src=$(find "$tmp" -maxdepth 2 -type d -name firmware | head -1)
  [[ -n "$src" ]] || die "no firmware folder in the archive"
  mkdir -p "$STATE_DIR"
  local added=0 f rel
  while IFS= read -r -d '' f; do
    rel=${f#"$src"/}
    if [[ ! -e /lib/firmware/$rel && ! -e /lib/firmware/$rel.xz && ! -e /lib/firmware/$rel.zst ]]; then
      install -D -m 0644 "$f" "/lib/firmware/$rel"
      echo "/lib/firmware/$rel" >> "$STATE_DIR/libreelec-added.txt"
      added=$((added+1))
    fi
  done < <(find "$src" -type f -print0)
  echo "Added $added firmware file(s)."
  say "Done"
  echo "Drivers load firmware when the tuner starts: reset USB tuners (--replug, or the button in dvbhub),"
  echo "or restart the computer for PCIe cards."
}

# ---------------------------------------------------------------- replug
replug() {
  need_root --replug
  local d n=0
  for d in $(usb_tuners); do
    echo "Resetting $(cat "$d/product" 2>/dev/null || basename "$d") ($(basename "$d"))"
    echo 0 > "$d/authorized" && sleep 1 && echo 1 > "$d/authorized" && n=$((n+1))
  done
  if [[ $n == 0 ]]; then
    echo "No USB tuner with a DVB driver found. Unplug and replug it by hand, or restart the computer."
    return 0
  fi
  sleep 4
  echo "Reset $n USB tuner(s). They reappear in dvbhub within a few seconds."
  ls -1 /dev/dvb/adapter*/frontend* 2>/dev/null || echo "No /dev/dvb adapters yet; if this stays empty, restart the computer."
}

# ---------------------------------------------------------------- tools
install_tools() {
  need_root --tools
  pkg_refresh
  case "$(distro)" in
    debian) pkg_install dvb-tools ;;
    *) pkg_install v4l-utils ;;
  esac
  echo "Installed: try  dvb-fe-tool  and  dvbv5-scan"
}

# ---------------------------------------------------------------- tbs
install_tbs() {
  need_root --tbs
  if [[ -d /sys/firmware/efi ]] && command -v mokutil >/dev/null && mokutil --sb-state 2>/dev/null | grep -qi enabled; then
    [[ "${FORCE:-0}" == 1 ]] || die "Secure Boot is on: self-built TBS modules won't load. Turn Secure Boot off (or sign the modules) and run again, or pass --force."
  fi
  say "Installing build tools"
  pkg_refresh
  local k; k=$(uname -r)
  case "$(distro)" in
    debian) pkg_install build-essential patchutils libproc-processtable-perl git wget bzip2 ca-certificates "linux-headers-$k" ;;
    fedora) pkg_install gcc make patchutils perl-Proc-ProcessTable perl-Digest-SHA git wget bzip2 "kernel-devel-$k" ;;
    arch)   pkg_install base-devel git wget perl-proc-processtable linux-headers ;;
    *) die "the automatic TBS build supports Debian/Ubuntu, Fedora and Arch only" ;;
  esac
  [[ -d /lib/modules/$k/build ]] || die "kernel headers for $k are not installed"

  say "Fetching TBS driver sources"
  rm -rf "$TBS_SRC"; mkdir -p "$TBS_SRC"; cd "$TBS_SRC" || die "cannot enter $TBS_SRC"
  git clone --depth=1 https://github.com/tbsdtv/media_build.git || die "cloning media_build failed"
  git clone --depth=1 https://github.com/tbsdtv/linux_media.git -b latest ./media || die "cloning linux_media failed"

  say "Building (15-40 minutes)"
  cd media_build || die "no media_build folder"
  make dir DIR=../media && make allyesconfig && make -j"$(nproc)" || die "the TBS driver build failed (see above)"
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
  echo "NOTE: these modules replace the kernel's media drivers and must be rebuilt after every kernel update (run --tbs again)."
  echo "Restart the computer to load them."
}

# ---------------------------------------------------------------- web trigger
# dvbhub (unprivileged, maybe in a container) writes <data>/driver/requests/<id>.req
# containing one word. A root systemd path unit runs this script for it, which only
# accepts the fixed words in $ACTIONS, and writes jobs/<id>.status and .log back.
enable_web_trigger() {
  need_root --enable-web-trigger
  local data=${1:-}
  [[ -n "$data" && -d "$data" ]] || die "usage: sudo bash $0 --enable-web-trigger <dvbhub data folder>  (Docker: the ./data folder next to docker-compose.yml; service install: /var/lib/dvbhub)"
  data=$(cd "$data" && pwd)
  command -v systemctl >/dev/null || die "systemd is required for the web trigger"

  say "Installing $INSTALLED_SELF"
  if [[ -f "${BASH_SOURCE[0]:-}" && "$(readlink -f "${BASH_SOURCE[0]}")" != "$INSTALLED_SELF" ]]; then
    install -m 0755 "${BASH_SOURCE[0]}" "$INSTALLED_SELF"
  elif [[ ! -f "${BASH_SOURCE[0]:-}" ]]; then
    fetch "$SELF_URL" "$INSTALLED_SELF.tmp" && install -m 0755 "$INSTALLED_SELF.tmp" "$INSTALLED_SELF" && rm -f "$INSTALLED_SELF.tmp" ||
      die "could not download the installer from $SELF_URL"
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
  echo "dvbhub's Tuners page (and the Jellyfin plugin) can now run: $ACTIONS."
  echo "Only these fixed actions are accepted. Turn it off with: sudo $INSTALLED_SELF --disable-web-trigger $data"
}

disable_web_trigger() {
  need_root --disable-web-trigger
  systemctl disable --now dvbhub-driver.path 2>/dev/null || true
  rm -f /etc/systemd/system/dvbhub-driver.path /etc/systemd/system/dvbhub-driver.service
  systemctl daemon-reload
  [[ -n "${1:-}" ]] && rm -f "$1/driver/enabled"
  echo "Web trigger disabled."
}

process_requests() {
  need_root --process-requests
  local data=${1:?data folder}
  shopt -s nullglob
  local req id action log status
  for req in "$data"/driver/requests/*.req; do
    id=$(basename "$req" .req)
    [[ "$id" =~ ^[0-9-]{1,40}$ ]] || { rm -f "$req"; continue; }
    action=$(head -c 32 "$req" | tr -cd 'a-z')
    rm -f "$req"
    log="$data/driver/jobs/$id.log"; status="$data/driver/jobs/$id.status"
    case " $ACTIONS " in
      *" $action "*) ;;
      *) echo "rejected unknown action" > "$log"; echo "${action:-unknown} failed" > "$status"; chmod 0644 "$log" "$status"; continue ;;
    esac
    echo "$action running" > "$status"
    chmod 0644 "$status"
    if "$INSTALLED_SELF" "--$action" > "$log" 2>&1; then
      case "$action" in
        firmware|tbs) echo "$action ok reboot" > "$status" ;;
        *) echo "$action ok" > "$status" ;;
      esac
    else
      echo "$action failed" > "$status"
    fi
    chmod 0644 "$log" "$status"
  done
}

# ---------------------------------------------------------------- main
FORCE=0
[[ " $* " == *" --force "* ]] && FORCE=1
case "${1:-}" in
  --detect)               detect ;;
  --firmware)             install_firmware ;;
  --replug)               replug ;;
  --tools)                install_tools ;;
  --tbs)                  install_tbs ;;
  --enable-web-trigger)   enable_web_trigger "${2:-}" ;;
  --disable-web-trigger)  disable_web_trigger "${2:-}" ;;
  --process-requests)     process_requests "${2:-}" ;;
  --version)              echo "dvbhub install-drivers $VERSION" ;;
  *) sed -n '2,14p' "$0" 2>/dev/null || echo "see $SELF_URL"; echo; detect ;;
esac
