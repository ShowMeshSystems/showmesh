#!/usr/bin/env bash
# Answers, in one pass, whether install-ptp-audio.sh's PTP-disciplined
# PipeWire audio graph is actually working on this node: is ptp4l locked
# and to which grandmaster (pmc), does the ALSA sink actually get its
# clock FROM the PTP driver rather than merely coexisting with it in the
# graph (pw-dump node.group/node.driver, not the node's own node.driver
# flag -- see "PipeWire graph driver" below for why that flag alone lies),
# and -- the load-bearing reading -- does a real GStreamer playback
# pipeline show any alsasink skew-slaving lines at all. With this working
# there should be none, because alsasink is no longer in the signal path
# (pipewiresink is); RES-019 section 7.1 is the reason that check exists.
#
# Read-only. Never installs, starts, stops, or reconfigures anything.
#
# Usage: verify-ptp-audio.sh [--play <seconds>] [--alsa-match <pattern>]
#   --play <seconds>   also run a short GST_DEBUG=audiobasesink:6 test
#                      pipeline through pipewiresink and report whether any
#                      skew-slaving lines appear (default: 8, 0 disables).
#   --alsa-match <pattern>  regex matched against node.name to find the
#                      sound card's ALSA output node (default: alsa_output,
#                      matching any ALSA sink; narrow it only if this node
#                      has more than one).

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/node/ptp-audio/ptp-group.conf
. "$SCRIPT_DIR/ptp-audio/ptp-group.conf"

PLAY_SECONDS=8
ALSA_MATCH="alsa_output"
while [ $# -gt 0 ]; do
  case "$1" in
    --play) PLAY_SECONDS="${2:-8}"; shift 2 ;;
    --alsa-match) ALSA_MATCH="${2:-alsa_output}"; shift 2 ;;
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

# --- 2. PipeWire: does the ALSA node's driver election actually land on
#    the PTP driver, not just "is this node capable of driving" ---
#
# A node's own node.driver property (true for a node that CAN act as a
# driver, false otherwise) says nothing about which driver a follower
# node actually got assigned to: this repository's own bench found, on
# real node hardware (showmesh-node-01), a case where the ALSA sink's own
# node.driver read false while it was in fact driving its own group, with
# showmesh-ptp-driver sitting unused in a different one -- exactly the
# false-positive this check used to produce. Every node pw-dump reports
# also carries node.driver-id: the object id of the node ACTUALLY driving
# it, confirmed against a real pw-dump capture in this repository's own
# bench (a synthetic sink's node.driver-id matched showmesh-ptp-driver's
# own object id once both were placed in the same node.group). Comparing
# node.driver-id to showmesh-ptp-driver's own id is the one reading that
# actually answers "who is driving this node," not merely "could this
# node drive something."
echo "--- PipeWire driver election ---"
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
    READING="$(PW_DUMP_JSON="$DUMP" python3 - "$ALSA_MATCH" <<'PYEOF'
import json, os, re, sys

alsa_re = re.compile(sys.argv[1])
try:
    objs = json.loads(os.environ["PW_DUMP_JSON"])
except Exception:
    print("PARSE_ERROR=1")
    sys.exit(0)

driver_id = None
driver_props = None
alsa_nodes = []
for o in objs:
    props = o.get("info", {}).get("props", {}) or {}
    name = props.get("node.name") or ""
    if name == "showmesh-ptp-driver":
        driver_id = o.get("id")
        driver_props = props
    if alsa_re.search(name):
        alsa_nodes.append((o.get("id"), props))

if driver_id is None:
    print("DRIVER_FOUND=0")
else:
    print("DRIVER_FOUND=1")
    print(f"DRIVER_ID={driver_id}")
    print(f"DRIVER_GROUP={driver_props.get('node.group', '')}")
    print(f"DRIVER_CLOCK={driver_props.get('clock.device', driver_props.get('clock.id', 'unknown-clock'))}")

print(f"ALSA_COUNT={len(alsa_nodes)}")
for node_id, props in alsa_nodes:
    name = props.get("node.name", "")
    driven_by = props.get("node.driver-id")
    group = props.get("node.group", "")
    print(f"ALSA_NODE={name}|{driven_by}|{group}")
PYEOF
)"
    DRIVER_FOUND="$(echo "$READING" | awk -F= '/^DRIVER_FOUND=/{print $2; exit}')"
    if [ "$DRIVER_FOUND" != "1" ]; then
      bad "no PipeWire node named showmesh-ptp-driver found in the graph (10-showmesh-ptp-clock.conf may not be loaded -- check /etc/pipewire/pipewire.conf.d/)"
    else
      DRIVER_ID="$(echo "$READING" | awk -F= '/^DRIVER_ID=/{print $2; exit}')"
      DRIVER_GROUP="$(echo "$READING" | awk -F= '/^DRIVER_GROUP=/{print $2; exit}')"
      DRIVER_CLOCK="$(echo "$READING" | awk -F= '/^DRIVER_CLOCK=/{print $2; exit}')"
      ok "showmesh-ptp-driver present (id=$DRIVER_ID), node.group=$DRIVER_GROUP, clocked from $DRIVER_CLOCK"

      ALSA_COUNT="$(echo "$READING" | awk -F= '/^ALSA_COUNT=/{print $2; exit}')"
      if [ "${ALSA_COUNT:-0}" -eq 0 ]; then
        bad "no ALSA node matching '$ALSA_MATCH' found in the graph (the sound card's sink may not have been created yet, WirePlumber's ALSA monitor may be disabled, or --alsa-match needs to match this node's actual name)"
      else
        FOUND_FOLLOWER=0
        while IFS='|' read -r name driven_by group; do
          [ -n "$name" ] || continue
          info "ALSA node \"$name\" node.driver-id=$driven_by node.group=$group"
          if [ "$driven_by" = "$DRIVER_ID" ]; then
            FOUND_FOLLOWER=1
          fi
        done < <(echo "$READING" | awk -F= '/^ALSA_NODE=/{print $2}')
        if [ "$FOUND_FOLLOWER" -eq 1 ]; then
          ok "at least one ALSA node's node.driver-id points at showmesh-ptp-driver's own id ($DRIVER_ID) -- this node's driver election actually landed on showmesh-ptp-driver, not just coexists with it"
        else
          bad "no ALSA node's node.driver-id points at showmesh-ptp-driver's id ($DRIVER_ID) -- the card is being driven by something else (itself, or another node in a different group). Check the ALSA node's node.group above against 51-showmesh-alsa-rate.conf's node.name match and node.group."
        fi
      fi
    fi
  fi
fi
echo ""

# --- 2b. xrun count, best-effort via pw-top (not exposed through pw-dump) ---
echo "--- ALSA sink xrun count ---"
if ! command -v pw-top >/dev/null 2>&1; then
  info "pw-top not found; xrun count unavailable (part of the pipewire package)"
elif ! systemctl is-active --quiet pipewire-showmesh.service 2>/dev/null; then
  info "skipped: pipewire-showmesh.service is not active"
else
  # pw-top -b prints one full refresh frame per second and never exits on
  # its own; a few seconds gives at least one complete frame with its ERR
  # (ERR = xrun count, per pw-top(1)) column to read, and ends with the
  # frame's own trailing state rather than a partial first frame.
  TOP_OUTPUT="$(timeout 3 pw-top -b 2>/dev/null || true)"
  XRUN_LINE="$(echo "$TOP_OUTPUT" | awk -v pat="$ALSA_MATCH" '
    $0 ~ /^S[[:space:]]+ID/ { for (i = 1; i <= NF; i++) if ($i == "ERR") err_col = i; next }
    err_col && $0 ~ pat { print; found_col = err_col }
    END { if (found_col) exit 0; else exit 1 }
  ')"
  if [ -n "$XRUN_LINE" ]; then
    info "pw-top row for an ALSA node matching '$ALSA_MATCH': $XRUN_LINE"
  else
    info "could not parse an ERR/xrun column for an ALSA node matching '$ALSA_MATCH' out of pw-top -b output; not a hard failure, this reading is best-effort (pw-top's exact column layout is not guaranteed across versions)"
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
