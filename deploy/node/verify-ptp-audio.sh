#!/usr/bin/env bash
# Answers, in one pass, whether install-ptp-audio.sh's PTP-disciplined
# PipeWire audio graph is actually working on this node: is ptp4l locked
# and to which grandmaster (pmc), is PipeWire's graph driven by the PTP
# node.driver (pw-dump), is the ALSA sink a follower being rate-matched to
# it, and -- the load-bearing reading -- does a real GStreamer playback
# pipeline show any alsasink skew-slaving lines at all. With this working
# there should be none, because alsasink is no longer in the signal path
# (pipewiresink is); RES-019 section 7.1 is the reason that check exists.
#
# Read-only. Never installs, starts, stops, or reconfigures anything.
#
# Usage: verify-ptp-audio.sh [--play <seconds>]
#   --play <seconds>  also run a short GST_DEBUG=audiobasesink:6 test
#                      pipeline through pipewiresink and report whether any
#                      skew-slaving lines appear (default: 8, 0 disables).

set -u

PLAY_SECONDS=8
while [ $# -gt 0 ]; do
  case "$1" in
    --play) PLAY_SECONDS="${2:-8}"; shift 2 ;;
    *) echo "verify-ptp-audio.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done

PTP_RO_SOCKET=/var/run/ptp/ptp4lro
PTP4L_CONF=/etc/showmesh/ptp4l.conf

# pmc's requests carry a domain number (default 0) that must match the
# target ptp4l's own domainNumber, or ptp4l silently drops the request --
# no error, no response, nothing (this cost real debugging time in
# bench/ptp-node: a request against a domain=44 ptp4l with no -d flag
# just hung). Read the domain this node was actually installed with
# rather than assuming 0.
PTP_DOMAIN=0
if [ -r "$PTP4L_CONF" ]; then
  CONF_DOMAIN="$(awk '$1 == "domainNumber" {print $NF; exit}' "$PTP4L_CONF")"
  if [ -n "$CONF_DOMAIN" ]; then
    PTP_DOMAIN="$CONF_DOMAIN"
  fi
fi

PASS=0
FAIL=0
ok()   { echo "OK: $1"; PASS=$((PASS + 1)); }
bad()  { echo "FAIL: $1" >&2; FAIL=$((FAIL + 1)); }
info() { echo "INFO: $1"; }

echo "verify-ptp-audio.sh: checking the PTP-disciplined PipeWire audio path"
echo ""

# --- 1. ptp4l: running, locked, and to whom ---
echo "--- ptp4l ---"
if ! systemctl is-active --quiet ptp4l-showmesh.service 2>/dev/null; then
  bad "ptp4l-showmesh.service is not active (systemctl status ptp4l-showmesh.service)"
elif ! command -v pmc >/dev/null 2>&1; then
  bad "pmc not found (part of the linuxptp package; install-ptp-audio.sh should have installed it)"
elif [ ! -S "$PTP_RO_SOCKET" ]; then
  bad "ptp4l-showmesh.service is active but its management socket $PTP_RO_SOCKET does not exist"
else
  PORT_DATA="$(pmc -u -b 0 -d "$PTP_DOMAIN" -s "$PTP_RO_SOCKET" 'GET PORT_DATA_SET' 2>/dev/null)"
  PORT_STATE="$(echo "$PORT_DATA" | awk -F'[[:space:]]+' '/portState/{print $NF; exit}')"
  TIME_STATUS="$(pmc -u -b 0 -d "$PTP_DOMAIN" -s "$PTP_RO_SOCKET" 'GET TIME_STATUS_NP' 2>/dev/null)"
  GM_IDENTITY="$(echo "$TIME_STATUS" | awk -F'[[:space:]]+' '/gmIdentity/{print $NF; exit}')"
  MASTER_OFFSET="$(echo "$TIME_STATUS" | awk -F'[[:space:]]+' '/master_offset/{print $NF; exit}')"

  case "$PORT_STATE" in
    SLAVE)
      ok "ptp4l port state SLAVE, following grandmaster ${GM_IDENTITY:-unknown}, master_offset=${MASTER_OFFSET:-unknown}ns"
      ;;
    MASTER)
      ok "ptp4l port state MASTER -- this node is the domain's current active clock (expected when it holds the grandmaster or auto role and nothing better is on the wire; a follower-role node should never report this)"
      ;;
    LISTENING)
      bad "ptp4l port state LISTENING -- not yet locked to any grandmaster (BMCA still settling, or nothing else is speaking PTP on this domain/interface). Not a hard failure if this node is freshly started; re-run in a few seconds."
      ;;
    "")
      bad "pmc got no response from $PTP_RO_SOCKET (ptp4l may still be starting, or the domain in its config does not match anything on the wire -- a mismatched domain gets no response at all, not an error)"
      ;;
    *)
      bad "ptp4l port state $PORT_STATE (not locked)"
      ;;
  esac
fi
echo ""

# --- 2. PipeWire: is the graph driven by the PTP driver ---
echo "--- PipeWire graph driver ---"
export PIPEWIRE_RUNTIME_DIR=/run/pipewire
export XDG_RUNTIME_DIR=/run/pipewire
if ! systemctl is-active --quiet pipewire-showmesh.service 2>/dev/null; then
  bad "pipewire-showmesh.service is not active"
elif ! command -v pw-dump >/dev/null 2>&1; then
  bad "pw-dump not found (part of the pipewire package)"
else
  DUMP="$(pw-dump 2>/dev/null)"
  if [ -z "$DUMP" ]; then
    bad "pw-dump returned nothing; PipeWire may not have finished starting, or this shell cannot reach /run/pipewire (check the socket's permissions against the user running this check)"
  else
    DRIVER_NODE="$(echo "$DUMP" | python3 -c '
import json, sys
try:
    objs = json.load(sys.stdin)
except Exception:
    sys.exit(1)
for o in objs:
    props = o.get("info", {}).get("props", {})
    if props.get("node.name") == "showmesh-ptp-driver":
        print(props.get("clock.device", props.get("clock.id", "unknown-clock")))
        sys.exit(0)
sys.exit(1)
' 2>/dev/null)"
    if [ -n "$DRIVER_NODE" ]; then
      ok "PipeWire node.driver \"showmesh-ptp-driver\" present, clocked from $DRIVER_NODE"
    else
      bad "no PipeWire node named showmesh-ptp-driver found in the graph (10-showmesh-ptp-clock.conf may not be loaded -- check /etc/pipewire/pipewire.conf.d/)"
    fi

    ALSA_DRIVING="$(echo "$DUMP" | python3 -c '
import json, sys
try:
    objs = json.load(sys.stdin)
except Exception:
    sys.exit(1)
for o in objs:
    props = o.get("info", {}).get("props", {})
    name = props.get("node.name", "") or ""
    if "alsa" in name.lower() or "M4" in name:
        driver = props.get("node.driver", None)
        group = props.get("node.group", "")
        print(f"{name}|{driver}|{group}")
' 2>/dev/null)"
    if [ -n "$ALSA_DRIVING" ]; then
      echo "$ALSA_DRIVING" | while IFS='|' read -r name driver group; do
        info "ALSA node \"$name\" node.driver=$driver node.group=$group"
      done
      ok "at least one ALSA node is present in the graph (node.driver=false means it is a follower, not its own driver -- confirm above)"
    else
      bad "no ALSA node found in the graph (the M4 sink may not have been created yet, or WirePlumber's ALSA monitor is disabled)"
    fi
  fi
fi
echo ""

# --- 3. The load-bearing reading: does alsasink ever slave-skew ---
echo "--- Sink slaving behavior (the reading that actually matters) ---"
if [ "$PLAY_SECONDS" -le 0 ]; then
  info "skipped (--play 0 or default overridden); this is the check that actually proves the rate lock is doing anything, run it before trusting the graph-driver checks above"
elif ! command -v gst-launch-1.0 >/dev/null 2>&1; then
  bad "gst-launch-1.0 not found (part of gstreamer1.0-tools)"
else
  LOG="$(mktemp)"
  GST_DEBUG=audiobasesink:6 timeout "$((PLAY_SECONDS + 2))" \
    gst-launch-1.0 audiotestsrc num-buffers=$((PLAY_SECONDS * 50)) ! audioconvert ! pipewiresink \
    > "$LOG" 2>&1
  RC=$?
  if [ "$RC" -ne 0 ] && [ "$RC" -ne 124 ]; then
    bad "gst-launch-1.0 through pipewiresink exited $RC; see $LOG"
  fi
  SLEW_LINES="$(grep -c -E 'slave|skew|resync' "$LOG" || true)"
  if [ "$SLEW_LINES" -eq 0 ]; then
    ok "no alsasink skew/slaving lines in ${PLAY_SECONDS}s of GST_DEBUG=audiobasesink:6 output through pipewiresink -- consistent with alsasink no longer being in the signal path (pipewiresink is) and the ALSA sink being rate-matched by PipeWire's own DLL instead of alsasink's slave-method"
  else
    bad "$SLEW_LINES skew/slaving line(s) found in GST_DEBUG output ($LOG) -- alsasink's own slaving should not be active when the pipeline plays through pipewiresink; this usually means the pipeline under test is not actually the one described in this repository's PR (a raw alsasink pipeline, or a fallback path)"
  fi
  info "full debug log: $LOG"
fi
echo ""

echo "verify-ptp-audio.sh: $PASS check(s) passed, $FAIL check(s) failed"
[ "$FAIL" -eq 0 ]
