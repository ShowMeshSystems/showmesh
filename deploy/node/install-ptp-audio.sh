#!/usr/bin/env bash
# Provisions a ShowMesh audio node to discipline its sound card from PTP:
# installs linuxptp and a PipeWire system-wide graph, points ptp4l at the
# named interface's PHC (or software timestamping when it has none), and
# makes a PHC-clocked PipeWire node.driver the graph driver above the ALSA
# sink, so the sink becomes a rate-matched follower of the PTP domain
# instead of free-running on its own crystal.
#
# RES-019 sections 4.5, 5.3, and 7.2 (Track I, seam I4, candidate A).
#
# ptp4l here runs as its OWN systemd service, independent of the ShowMesh
# agent (RES-019's "External linuxptp" provider, section 5.3): the PHC
# stays disciplined, and PipeWire stays clocked from it, whether or not
# the agent process is running. Configure the agent's node.clock as
# provider "external" against this unit's read-only socket
# (/var/run/ptp/ptp4lro, linuxptp's own default) -- never "managed": RES-019
# section 5.3 is explicit that exactly one component owns ptp4l on an
# interface, and this script's unit is that component. See
# deploy/node/PTP-AUDIO.md.
#
# Usage: install-ptp-audio.sh <interface> <domain> [role] [audio-card-match]
#   interface        the network interface ptp4l runs on (e.g. eno2)
#   domain           the PTP domain number this node participates in (0-127)
#   role             follower (default), grandmaster, or auto:
#                follower    clientOnly 1, priority1 255 (worst legal
#                            value; see the ROLE case below for why this
#                            is not optional). This node never becomes
#                            master no matter what else is on the wire --
#                            the Day-0 show network case, which already
#                            has a grandmaster.
#                grandmaster clientOnly 0, priority1 248 (linuxptp's own
#                            default, matching FPP's, RES-019 section
#                            5.3). This node runs full BMCA and, alone on
#                            the wire, elects itself master -- the dev
#                            network case, one node with nothing else
#                            speaking PTP. priority1 248 is deliberately
#                            worse than the 128 professional gear
#                            typically declares, so real gear introduced
#                            later still wins BMCA and demotes this node
#                            to follower automatically.
#                auto        clientOnly 0, no priority1 override (plain
#                            linuxptp default of 128). BMCA decides with
#                            no thumb on the scale either way.
#   audio-card-match a substring/glob fragment of the sound card's ALSA
#                    device name (default: M4). Pins that card onto
#                    WirePlumber's pro-audio profile and puts its ALSA
#                    output node in the same PipeWire node.group as
#                    showmesh-ptp-driver -- both required for driver
#                    election to actually land on this driver rather than
#                    the card's own ALSA node (proven on real node
#                    hardware, see deploy/node/PTP-AUDIO.md). Any card
#                    WirePlumber's UCM logic splits by profile has this
#                    same problem, not just the M4; change this argument
#                    for a different card instead of editing the
#                    WirePlumber rule template.
#
# Idempotent: safe to re-run with the same or different arguments. Every
# file this script writes is fully derived from its arguments, so each
# run simply regenerates them; nothing here is operator-edited state the
# way deploy/node/install.sh's agent.env is.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_DIR="$SCRIPT_DIR/ptp-audio"

# shellcheck source=deploy/node/ptp-audio/ptp-group.conf
. "$TEMPLATE_DIR/ptp-group.conf"

PTP_GROUP=showmesh
PIPEWIRE_USER=pipewire
PIPEWIRE_GROUP=pipewire
PIPEWIRE_STATE_DIR=/var/lib/pipewire
PTP4L_CONF=/etc/showmesh/ptp4l.conf
PTP4L_UNIT_DEST=/etc/systemd/system/ptp4l-showmesh.service
UDEV_RULE_DEST=/etc/udev/rules.d/99-showmesh-ptp.rules
PIPEWIRE_CONFD=/etc/pipewire/pipewire.conf.d
PIPEWIRE_CLOCK_CONF="$PIPEWIRE_CONFD/10-showmesh-ptp-clock.conf"
WIREPLUMBER_CONFD=/etc/wireplumber/wireplumber.conf.d
PIPEWIRE_UNIT_DEST=/etc/systemd/system/pipewire-showmesh.service
WIREPLUMBER_UNIT_DEST=/etc/systemd/system/wireplumber-showmesh.service
PTP_RO_SOCKET=/var/run/ptp/ptp4lro

if [ "$(id -u)" -ne 0 ]; then
  echo "install-ptp-audio.sh: must be run as root" >&2
  exit 1
fi

if [ $# -lt 2 ] || [ $# -gt 4 ]; then
  echo "usage: $0 <interface> <domain> [follower|grandmaster|auto] [audio-card-match]" >&2
  exit 2
fi
IFACE="$1"
DOMAIN="$2"
ROLE="${3:-follower}"
AUDIO_CARD_MATCH="${4:-M4}"

case "$DOMAIN" in
  ''|*[!0-9]*) echo "install-ptp-audio.sh: domain must be a non-negative integer, got '$DOMAIN'" >&2; exit 2 ;;
esac
if [ "$DOMAIN" -gt 127 ]; then
  echo "install-ptp-audio.sh: domain must be 0-127 per IEEE 1588, got '$DOMAIN'" >&2
  exit 2
fi

case "$ROLE" in
  follower)
    # priority1 255 (worst legal value) is deliberate, not just clientOnly:
    # measured in this repository's own bench (bench/ptp-node), a
    # clientOnly port left at ptp4l's plain default priority1 (128) beats
    # a grandmaster-role node's 248 on BMCA's priority1 comparison, so the
    # follower's own defaultDS looks preferable and ptp4l gets stuck
    # logging "master state recommended in slave only mode: defaultDS.
    # priority1 probably misconfigured" instead of ever reaching SLAVE.
    # 255 guarantees this port never outranks any other clock on the
    # domain, which is the entire point of clientOnly.
    CLIENT_ONLY=1
    PRIORITY1_LINE="priority1 255"
    ;;
  grandmaster)
    CLIENT_ONLY=0
    PRIORITY1_LINE="priority1 248"
    ;;
  auto)
    CLIENT_ONLY=0
    PRIORITY1_LINE=""
    ;;
  *)
    echo "install-ptp-audio.sh: role must be follower, grandmaster, or auto, got '$ROLE'" >&2
    exit 2
    ;;
esac

if [ ! -e "/sys/class/net/$IFACE" ]; then
  echo "install-ptp-audio.sh: no interface named '$IFACE' (checked /sys/class/net/$IFACE)" >&2
  exit 1
fi

echo "install-ptp-audio.sh: provisioning PTP-disciplined PipeWire audio for interface=$IFACE domain=$DOMAIN role=$ROLE audio-card-match=$AUDIO_CARD_MATCH node.group=$PTP_NODE_GROUP"

# --- Platform note: Debian family assumed, warn otherwise rather than refuse ---
# Unlike deploy/node/install.sh, this script is not limited to Debian 13: it
# is meant to run unchanged on the Raspberry Pi 3B+ target too (Debian-based
# Raspberry Pi OS). It refuses only when apt-get itself is missing.
if ! command -v apt-get >/dev/null 2>&1; then
  echo "install-ptp-audio.sh: apt-get not found; this installer only supports Debian-family hosts" >&2
  exit 1
fi

# --- Packages ---
echo "install-ptp-audio.sh: installing linuxptp, pipewire, pipewire-audio, wireplumber, gstreamer1.0-pipewire"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
  linuxptp pipewire pipewire-audio wireplumber gstreamer1.0-pipewire

# --- PHC discovery, without ethtool ---
# ethtool -T reads this exact same information from the driver; the kernel
# also exposes it directly for any interface whose driver registers a PHC,
# at /sys/class/net/<iface>/device/ptp/ptpN. Reading sysfs directly means
# this installer needs no ethtool dependency (the target node does not
# have it installed, per the provisioning brief).
PHC_SYSFS_DIR="/sys/class/net/$IFACE/device/ptp"
PHC_INDEX=""
if [ -d "$PHC_SYSFS_DIR" ]; then
  for entry in "$PHC_SYSFS_DIR"/ptp*; do
    [ -e "$entry" ] || continue
    candidate="$(basename "$entry")"
    PHC_INDEX="${candidate#ptp}"
    break
  done
fi

if [ -n "$PHC_INDEX" ]; then
  PHC_DEV="/dev/ptp$PHC_INDEX"
  TIMESTAMPING=hardware
  echo "install-ptp-audio.sh: found PHC $PHC_DEV for $IFACE ($PHC_SYSFS_DIR/ptp$PHC_INDEX); ptp4l will use hardware timestamping"
else
  PHC_DEV=""
  TIMESTAMPING=software
  echo "install-ptp-audio.sh: no PHC found for $IFACE (no $PHC_SYSFS_DIR, or it has no ptpN entry); ptp4l will use software timestamping. This is expected on a Raspberry Pi 3B+ (LAN7515 USB NIC, RES-019 section 4.6) and on any virtual/container interface. Software timestamping disciplines CLOCK_REALTIME (there is no PHC to discipline instead, RES-019 section 5.1), so PipeWire's node.driver is pointed at clock.id=realtime rather than a PHC device (see the PipeWire config section below): this node is still PTP-disciplined end to end, just through the system clock instead of a hardware clock."
fi

# --- PHC-less node: exactly one writer on CLOCK_REALTIME ---
# ptp4l in software-timestamping mode disciplines CLOCK_REALTIME directly
# (there is no PHC to discipline instead). An NTP client left running on
# the same node also disciplines CLOCK_REALTIME, and neither daemon knows
# the other exists: measured on a real Raspberry Pi 3B+, systemd-timesyncd
# and ptp4l fought over the same clock at the same time. A node WITH a PHC
# does not have this problem and keeps its NTP client running: there,
# ptp4l only ever touches the PHC, and NTP keeps the system clock (which
# phc2sys reads from, below) honest.
if [ -z "$PHC_DEV" ]; then
  NTP_DISABLED=""
  for svc in systemd-timesyncd chrony chronyd ntp ntpd; do
    if systemctl is-active --quiet "$svc.service" 2>/dev/null; then
      echo "install-ptp-audio.sh: $svc.service is disciplining CLOCK_REALTIME on a PHC-less node; ptp4l is about to do the same. Turning $svc.service off so exactly one thing owns this node's system clock."
      if [ "$svc" = "systemd-timesyncd" ]; then
        timedatectl set-ntp false 2>/dev/null || true
      else
        systemctl disable --now "$svc.service" >/dev/null 2>&1 || true
      fi
      if systemctl is-active --quiet "$svc.service" 2>/dev/null; then
        echo "install-ptp-audio.sh: could not stop $svc.service; refusing to leave two writers on CLOCK_REALTIME (ptp4l and $svc.service). Stop it by hand and re-run this script." >&2
        exit 1
      fi
      NTP_DISABLED="$NTP_DISABLED $svc.service"
    fi
  done
  if [ -n "$NTP_DISABLED" ]; then
    echo "install-ptp-audio.sh: stopped and disabled$NTP_DISABLED; ptp4l now owns this node's system clock (CLOCK_REALTIME). See deploy/node/PTP-AUDIO.md to put NTP back."
  else
    echo "install-ptp-audio.sh: no active NTP client found on this PHC-less node; ptp4l will be the only thing disciplining CLOCK_REALTIME"
  fi
fi

# --- ptp4l config and systemd unit ---
mkdir -p "$(dirname "$PTP4L_CONF")"
{
  echo "# Generated by install-ptp-audio.sh for interface=$IFACE domain=$DOMAIN role=$ROLE."
  echo "# Regenerated on every run of that script; do not hand-edit."
  echo "[global]"
  echo "domainNumber $DOMAIN"
  echo "clientOnly $CLIENT_ONLY"
  if [ -n "$PRIORITY1_LINE" ]; then
    echo "$PRIORITY1_LINE"
  fi
  echo "time_stamping $TIMESTAMPING"
  echo "network_transport UDPv4"
  echo "uds_address /var/run/ptp4l"
  echo "uds_ro_address $PTP_RO_SOCKET"
  echo "logging_level 6"
  echo "summary_interval 0"
} > "$PTP4L_CONF"
chmod 0644 "$PTP4L_CONF"

sed -e "s|@IFACE@|$IFACE|g" -e "s|@CONF@|$PTP4L_CONF|g" \
  "$TEMPLATE_DIR/ptp4l-showmesh.service.template" > "$PTP4L_UNIT_DEST"
chmod 0644 "$PTP4L_UNIT_DEST"
echo "install-ptp-audio.sh: wrote $PTP4L_CONF and $PTP4L_UNIT_DEST"

# --- phc2sys: grandmaster-role node disciplines its own PHC from the
#     system clock, so it never announces an arbitrary free-run epoch ---
#
# Only for role=grandmaster, and only when this node has a PHC. A
# follower-role node's PHC belongs to the domain -- it must never run
# phc2sys in this direction. A PHC-less node has nothing for phc2sys to
# discipline here; its own CLOCK_REALTIME is already the thing ptp4l
# disciplines (see the NTP-ownership check above).
#
# Measured on showmesh-node-01: an undisciplined PHC read 17.7 seconds
# behind the host's own NTP-disciplined system clock, and a PHC-less
# follower on the same domain slewed its own wall clock 54.7 seconds away
# from real time following it (the class of failure FPP removed phc2sys
# for). Reported here, before phc2sys is enabled, so an operator sees the
# size of the correction before it happens: correcting an 18-second PHC
# error while a show is already running would be an 18-second clock step
# mid-show, which is exactly why the generated unit is ordered Before=
# ptp4l-showmesh.service rather than left to converge on its own.
PHC2SYS_UNIT_DEST=/etc/systemd/system/phc2sys-showmesh.service
if [ "$ROLE" = "grandmaster" ] && [ -n "$PHC_DEV" ]; then
  if command -v phc_ctl >/dev/null 2>&1; then
    PHC_GET="$(phc_ctl "$PHC_DEV" get 2>&1 || true)"
    PHC_EPOCH="$(echo "$PHC_GET" | grep -oE '[0-9]+\.[0-9]+' | head -1)"
    SYS_EPOCH="$(date +%s.%N)"
    if [ -n "$PHC_EPOCH" ]; then
      PHC_OFFSET="$(awk -v a="$PHC_EPOCH" -v b="$SYS_EPOCH" 'BEGIN{printf "%.3f", (a-b)}')"
      echo "install-ptp-audio.sh: $PHC_DEV reads ${PHC_OFFSET}s relative to this host's system clock before phc2sys corrects it (phc_ctl output: $PHC_GET)"
    else
      echo "install-ptp-audio.sh: could not parse a timestamp out of 'phc_ctl $PHC_DEV get' output; the PHC-to-system offset before correction is unknown, not zero (raw output: $PHC_GET)"
    fi
  else
    echo "install-ptp-audio.sh: phc_ctl not found (part of linuxptp); the PHC-to-system offset before phc2sys corrects it is unknown, not zero"
  fi

  sed -e "s|@PHC_DEV@|$PHC_DEV|g" \
    "$TEMPLATE_DIR/phc2sys-showmesh.service.template" > "$PHC2SYS_UNIT_DEST"
  chmod 0644 "$PHC2SYS_UNIT_DEST"
  # ptp4l-showmesh.service's own template has no phc2sys dependency (a
  # follower-role or PHC-less node must never pull this unit in), so the
  # ordering is added here, only for the case that needs it.
  if ! grep -q phc2sys-showmesh.service "$PTP4L_UNIT_DEST"; then
    awk '/^Wants=network-online.target$/ { print; print "After=phc2sys-showmesh.service"; print "Wants=phc2sys-showmesh.service"; next } { print }' \
      "$PTP4L_UNIT_DEST" > "$PTP4L_UNIT_DEST.tmp" && mv "$PTP4L_UNIT_DEST.tmp" "$PTP4L_UNIT_DEST"
  fi
  echo "install-ptp-audio.sh: wrote $PHC2SYS_UNIT_DEST (phc2sys -s CLOCK_REALTIME -c $PHC_DEV -O 0), ordered before $PTP4L_UNIT_DEST"
elif [ -e "$PHC2SYS_UNIT_DEST" ]; then
  # Re-running with a different role or a different interface: a leftover
  # phc2sys unit from a previous grandmaster-role run must not keep
  # disciplining a PHC this run no longer owns in that direction.
  systemctl disable --now phc2sys-showmesh.service >/dev/null 2>&1 || true
  rm -f "$PHC2SYS_UNIT_DEST"
  echo "install-ptp-audio.sh: removed stale $PHC2SYS_UNIT_DEST (this run is role=$ROLE${PHC_DEV:+, phc=$PHC_DEV}, not grandmaster+PHC)"
fi

# --- udev rule: showmesh group gets read access to any PHC ---
# Generic on subsystem "ptp" rather than naming ptp0 specifically, so it
# keeps working if the PHC enumerates under a different index (a second
# NIC, a reboot after a driver change). The agent (running as the
# showmesh user, deploy/node/install.sh) reads the PHC directly for the
# external clock provider's Now(); PipeWire's node.driver opens the same
# device, so the pipewire system user is also added to this group below.
if ! getent group "$PTP_GROUP" >/dev/null 2>&1; then
  groupadd --system "$PTP_GROUP"
  echo "install-ptp-audio.sh: created group $PTP_GROUP"
fi
mkdir -p "$(dirname "$UDEV_RULE_DEST")"
cp "$TEMPLATE_DIR/99-showmesh-ptp.rules" "$UDEV_RULE_DEST"
chmod 0644 "$UDEV_RULE_DEST"
if command -v udevadm >/dev/null 2>&1; then
  udevadm control --reload-rules 2>/dev/null || true
  udevadm trigger --subsystem-match=ptp 2>/dev/null || true
else
  echo "install-ptp-audio.sh: WARNING: udevadm not found; rule installed at $UDEV_RULE_DEST but not (re)applied. Expected inside a plain container; not expected on a real node." >&2
fi

# --- pipewire system user ---
if ! getent group "$PIPEWIRE_GROUP" >/dev/null 2>&1; then
  groupadd --system "$PIPEWIRE_GROUP"
  echo "install-ptp-audio.sh: created group $PIPEWIRE_GROUP"
fi
if ! getent passwd "$PIPEWIRE_USER" >/dev/null 2>&1; then
  useradd --system --gid "$PIPEWIRE_GROUP" --home-dir "$PIPEWIRE_STATE_DIR" \
    --no-create-home --shell /usr/sbin/nologin \
    --comment "ShowMesh headless PipeWire" "$PIPEWIRE_USER"
  echo "install-ptp-audio.sh: created user $PIPEWIRE_USER"
fi
usermod -aG "$PTP_GROUP" "$PIPEWIRE_USER"
usermod -aG audio "$PIPEWIRE_USER" 2>/dev/null || \
  echo "install-ptp-audio.sh: WARNING: could not add $PIPEWIRE_USER to the 'audio' group (group may not exist on this host); ALSA device access may need manual attention."
mkdir -p "$PIPEWIRE_STATE_DIR"
chown "$PIPEWIRE_USER:$PIPEWIRE_GROUP" "$PIPEWIRE_STATE_DIR"
chmod 0750 "$PIPEWIRE_STATE_DIR"

# --- PipeWire config: PHC-clocked node.driver above the ALSA sink ---
mkdir -p "$PIPEWIRE_CONFD" "$WIREPLUMBER_CONFD"
if [ -n "$PHC_DEV" ]; then
  CLOCK_PROPERTY="clock.device = \"$PHC_DEV\""
else
  # No PHC to open, but ptp4l in software-timestamping mode still
  # disciplines CLOCK_REALTIME (RES-019 section 5.1: "read... from the
  # clock ptp4l is disciplining when it runs with software timestamping").
  # clock.id=realtime (RES-019 section 4.5's enum) points the node.driver
  # at that same disciplined system clock, so this is still real PTP
  # discipline end to end on a PHC-less host (a Raspberry Pi 3B+), not a
  # fallback to an undisciplined monotonic clock.
  CLOCK_PROPERTY="clock.id = realtime"
fi
sed -e "s|@CLOCK_PROPERTY@|$CLOCK_PROPERTY|" -e "s|@NODE_GROUP@|$PTP_NODE_GROUP|g" \
  "$TEMPLATE_DIR/10-showmesh-ptp-clock.conf.template" > "$PIPEWIRE_CLOCK_CONF"
chmod 0644 "$PIPEWIRE_CLOCK_CONF"
sed -e "s|@NODE_GROUP@|$PTP_NODE_GROUP|g" -e "s|@CARD_MATCH@|$AUDIO_CARD_MATCH|g" \
  "$TEMPLATE_DIR/51-showmesh-alsa-rate.conf.template" > "$WIREPLUMBER_CONFD/51-showmesh-alsa-rate.conf"
chmod 0644 "$WIREPLUMBER_CONFD/51-showmesh-alsa-rate.conf"
echo "install-ptp-audio.sh: wrote $PIPEWIRE_CLOCK_CONF and $WIREPLUMBER_CONFD/51-showmesh-alsa-rate.conf"

# --- PipeWire and WirePlumber as system services ---
# Debian's pipewire and wireplumber packages ship ONLY user-session units
# (/usr/lib/systemd/user/{pipewire,wireplumber}.service): they assume a
# logged-in desktop session, which this headless node never has. Running
# `systemctl --user` requires a lingering user session (loginctl
# enable-linger) and is still keyed to a login that may never happen on a
# node whose only job is to play a show. These two units instead run
# PipeWire and WirePlumber as ordinary system services under a dedicated
# system account, sharing one runtime directory so WirePlumber's session
# manager finds the same PipeWire instance a user session would otherwise
# have found through XDG_RUNTIME_DIR.
sed -e "s|@USER@|$PIPEWIRE_USER|g" -e "s|@GROUP@|$PIPEWIRE_GROUP|g" \
  "$TEMPLATE_DIR/pipewire-showmesh.service.template" > "$PIPEWIRE_UNIT_DEST"
sed -e "s|@USER@|$PIPEWIRE_USER|g" -e "s|@GROUP@|$PIPEWIRE_GROUP|g" \
  "$TEMPLATE_DIR/wireplumber-showmesh.service.template" > "$WIREPLUMBER_UNIT_DEST"
chmod 0644 "$PIPEWIRE_UNIT_DEST" "$WIREPLUMBER_UNIT_DEST"
echo "install-ptp-audio.sh: wrote $PIPEWIRE_UNIT_DEST and $WIREPLUMBER_UNIT_DEST"

# --- Activate, or report why not ---
# A real node host runs systemd as PID 1; a container used only to prove
# this script's file/config behavior (bench/ptp-node) does not. Detect
# that up front, exactly as deploy/node/install.sh does, rather than
# letting `set -e` abort partway through the first systemctl call.
SYSTEMD_AVAILABLE=1
if ! systemctl daemon-reload 2>/tmp/showmesh-ptp-install-systemctl-err; then
  SYSTEMD_AVAILABLE=0
  echo "install-ptp-audio.sh: WARNING: systemctl daemon-reload failed ($(tr -d '\n' < /tmp/showmesh-ptp-install-systemctl-err)). This host is not running systemd as PID 1 (expected inside a plain container; not expected on a real node). Every file above is installed but nothing is enabled or started." >&2
  rm -f /tmp/showmesh-ptp-install-systemctl-err
fi

if [ "$SYSTEMD_AVAILABLE" -eq 1 ]; then
  if [ -e "$PHC2SYS_UNIT_DEST" ]; then
    systemctl enable --now phc2sys-showmesh.service >/dev/null
  fi
  systemctl enable --now ptp4l-showmesh.service >/dev/null
  systemctl enable --now pipewire-showmesh.service >/dev/null
  systemctl enable --now wireplumber-showmesh.service >/dev/null
fi

# --- Summary ---
echo ""
echo "install-ptp-audio.sh: done."
echo "  Installed: linuxptp $(dpkg-query -W -f='${Version}' linuxptp 2>/dev/null || echo unknown), pipewire $(dpkg-query -W -f='${Version}' pipewire 2>/dev/null || echo unknown), wireplumber $(dpkg-query -W -f='${Version}' wireplumber 2>/dev/null || echo unknown), gstreamer1.0-pipewire $(dpkg-query -W -f='${Version}' gstreamer1.0-pipewire 2>/dev/null || echo unknown)"
echo "  PTP: interface=$IFACE domain=$DOMAIN role=$ROLE clientOnly=$CLIENT_ONLY timestamping=$TIMESTAMPING phc=${PHC_DEV:-none (clocking from CLOCK_REALTIME)}"
if [ "$SYSTEMD_AVAILABLE" -eq 1 ]; then
  if systemctl is-active --quiet ptp4l-showmesh.service; then
    # Report the actual, currently-observed port state honestly rather than
    # assuming the requested role took effect: a "grandmaster"-role node
    # alone on the wire self-elects MASTER, the same role node joined to a
    # network with a real grandmaster settles into SLAVE, and ptp4l takes a
    # few BMCA announce intervals either way -- LISTENING right after start
    # is normal, not a fault. See verify-ptp-audio.sh for the full picture
    # (lock state, grandmaster identity, PipeWire driver, sink slaving).
    PORT_STATE="$(pmc -u -b 0 -d "$DOMAIN" -s "$PTP_RO_SOCKET" 'GET PORT_DATA_SET' 2>/dev/null | awk -F'[[:space:]]+' '/portState/{print $NF; exit}')"
    case "$PORT_STATE" in
      MASTER) echo "  ptp4l-showmesh.service: active, port state MASTER -- this node is currently the domain's active clock (expected for role=grandmaster/auto with nothing better on the wire; unexpected for role=follower)." ;;
      SLAVE) echo "  ptp4l-showmesh.service: active, port state SLAVE -- following another clock on this domain. Run verify-ptp-audio.sh for its identity and offset." ;;
      "") echo "  ptp4l-showmesh.service: active, port state not yet available (pmc got no answer -- ptp4l may still be starting). Run verify-ptp-audio.sh shortly." ;;
      *) echo "  ptp4l-showmesh.service: active, port state $PORT_STATE." ;;
    esac
  else
    echo "  ptp4l-showmesh.service: NOT active (systemctl status ptp4l-showmesh.service for why)."
  fi
  echo "  PipeWire graph clock: $([ -n "$PHC_DEV" ] && echo "configured for $PHC_DEV" || echo "configured for clock.id=realtime (no PHC on $IFACE)"), node.group=$PTP_NODE_GROUP, audio-card-match=$AUDIO_CARD_MATCH. Run verify-ptp-audio.sh to confirm the running graph actually elected this driver for the card, not just that both exist."
  if [ -e "$PHC2SYS_UNIT_DEST" ]; then
    if systemctl is-active --quiet phc2sys-showmesh.service; then
      echo "  phc2sys-showmesh.service: active, disciplining $PHC_DEV from this host's system clock (role=grandmaster)."
    else
      echo "  phc2sys-showmesh.service: NOT active (systemctl status phc2sys-showmesh.service for why); this PHC is not being corrected."
    fi
  fi
else
  echo "  Services installed but not started (no systemd PID 1 on this host). Run 'systemctl daemon-reload && systemctl enable --now ptp4l-showmesh.service pipewire-showmesh.service wireplumber-showmesh.service' once this host boots under systemd."
fi
echo "  Configure the ShowMesh agent's node.clock as provider=external, interface=$IFACE, domain=$DOMAIN (see deploy/node/PTP-AUDIO.md) -- never provider=managed, which would start a second ptp4l on this interface."
