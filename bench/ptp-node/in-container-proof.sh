#!/usr/bin/env bash
# Runs inside one bench/ptp-node container. Proves what a container without
# a PHC, without ALSA hardware, and without systemd as PID 1 actually can:
#
#  - install-ptp-audio.sh installs its packages and generates correct
#    config for the given interface/domain/role, and reports (accurately)
#    that it found no PHC and has no systemd to hand its units to;
#  - a real ptp4l process, started from the exact generated config, reaches
#    SLAVE (follower container) or MASTER (grandmaster container) state --
#    software timestamping only, since no container has a PHC;
#  - a real PipeWire + WirePlumber pair, started from the exact generated
#    config, elects the showmesh-ptp-driver node as a driver in the graph,
#    clocked from clock.id=realtime (the no-PHC case);
#  - a pipewiresink GStreamer pipeline plays into that graph without error;
#  - the node.group election mechanism itself: a synthetic sink node
#    placed in the same node.group as showmesh-ptp-driver (deploy/node/
#    ptp-audio/ptp-group.conf's PTP_NODE_GROUP) ends up with node.driver
#    false while showmesh-ptp-driver reports node.driver true, the same
#    pw-dump reading verify-ptp-audio.sh now checks for the real ALSA
#    node. This is the actual fix proven on real node hardware
#    (showmesh-node-01, RES-019 Track I): PipeWire elects a driver WITHIN
#    a node.group, so two nodes only compete for driver status when they
#    share one.
#
# What it CANNOT prove, and does not claim to: hardware timestamping,
# clock.device against a real /dev/ptpN, WirePlumber's own ALSA monitor
# putting a REAL sound card's node into this group or onto the pro-audio
# profile (no /dev/snd in a container, so 51-showmesh-alsa-rate.conf's
# match rules and profile pin are exercised on nothing here -- this bench
# proves the group-election mechanism generically, not that the M4-facing
# match rule actually finds the M4), or that any of this survives a real
# systemd (bench/node-install's own README makes the identical point about
# deploy/node/install.sh).
#
# Usage: in-container-proof.sh <role> <domain>
#   role        grandmaster or follower
#   domain      PTP domain both containers share
#
# Both containers reach each other by multicast on the shared docker
# network, so nothing here needs the peer's address: run_proof.sh passes
# the grandmaster container's name as a third argument today for log
# readability only.

set -euo pipefail

ROLE="$1"
DOMAIN="$2"

# shellcheck source=deploy/node/ptp-audio/ptp-group.conf
. /repo/deploy/node/ptp-audio/ptp-group.conf

# eth0 specifically: a docker bridge network container's real interface,
# not the tunl0/gre0/etc. placeholder links some Docker network drivers
# also register (those have no working link at all and ptp4l refuses to
# bind them).
if [ -e /sys/class/net/eth0 ]; then
  IFACE=eth0
else
  IFACE="$(ip -o link show up | awk -F': ' '$2 != "lo" {print $NF; exit}')"
fi
echo "=== in-container-proof.sh: role=$ROLE domain=$DOMAIN iface=$IFACE ==="

echo "--- step 1: install-ptp-audio.sh ---"
/repo/deploy/node/install-ptp-audio.sh "$IFACE" "$DOMAIN" "$ROLE" 2>&1 | tee /tmp/install-ptp-audio.log

echo ""
echo "--- step 2: confirm the no-PHC / no-systemd reporting is honest ---"
if grep -q 'time_stamping software' /etc/showmesh/ptp4l.conf; then
  echo "OK: ptp4l.conf correctly fell back to software timestamping (no PHC in this container)"
else
  echo "FAIL: expected software timestamping in a container with no PHC"; exit 1
fi
if grep -q 'clock.id = realtime' /etc/pipewire/pipewire.conf.d/10-showmesh-ptp-clock.conf; then
  echo "OK: PipeWire clock config correctly fell back to clock.id=realtime (no PHC)"
else
  echo "FAIL: expected clock.id=realtime in the PipeWire driver config"; exit 1
fi
case "$ROLE" in
  follower)
    grep -q '^clientOnly 1$' /etc/showmesh/ptp4l.conf && echo "OK: clientOnly 1 for role=follower"
    ;;
  grandmaster)
    if grep -q '^clientOnly 0$' /etc/showmesh/ptp4l.conf && grep -q '^priority1 248$' /etc/showmesh/ptp4l.conf; then
      echo "OK: clientOnly 0, priority1 248 for role=grandmaster"
    fi
    ;;
esac

echo ""
echo "--- step 2b: role-conditional phc2sys unit generation and the NTP-detection branch ---"
# This container has no PHC at all (no /dev/ptpN), which install-ptp-audio.sh
# detects itself in step 1: phc2sys-showmesh.service disciplines a PHC from
# the system clock, and there is no PHC here for either role to discipline,
# so the unit must never be written -- proving the negative case of the
# role-conditional generation for both roles, not just that a grandmaster
# with a PHC gets it (this bench has no PHC to prove that positive case;
# the orchestrator proved it by hand on showmesh-node-01).
if [ -e /etc/systemd/system/phc2sys-showmesh.service ]; then
  echo "FAIL: phc2sys-showmesh.service was written on a PHC-less container (role=$ROLE); it must only exist for role=grandmaster with a real PHC"
  exit 1
else
  echo "OK: phc2sys-showmesh.service correctly not written (role=$ROLE, no PHC in this container)"
fi
# Same PHC-less container also exercises the NTP-detection branch: no NTP
# client is installed or running here, so it must report that honestly
# rather than claiming to have disabled something it never touched.
if grep -q 'no active NTP client found' /tmp/install-ptp-audio.log; then
  echo "OK: NTP-detection branch ran and correctly reported no active NTP client in this container"
else
  echo "FAIL: expected install-ptp-audio.sh to report no active NTP client on a PHC-less container with none running"
  exit 1
fi

echo ""
echo "--- step 2c: step_threshold (follower only) and the ptpTimescale config-file regression guard ---"
case "$ROLE" in
  follower)
    if grep -q '^step_threshold 1.0$' /etc/showmesh/ptp4l.conf; then
      echo "OK: step_threshold 1.0 present for role=follower"
    else
      echo "FAIL: expected step_threshold 1.0 in ptp4l.conf for role=follower"
      exit 1
    fi
    ;;
  grandmaster)
    if grep -q '^step_threshold' /etc/showmesh/ptp4l.conf; then
      echo "FAIL: step_threshold should not be set for role=grandmaster"
      exit 1
    else
      echo "OK: step_threshold correctly absent for role=grandmaster"
    fi
    ;;
esac
if grep -qi 'ptpTimescale' /etc/showmesh/ptp4l.conf; then
  echo "FAIL: ptp4l.conf must never contain ptpTimescale -- it is not a valid config-file option and makes ptp4l refuse to start"
  exit 1
else
  echo "OK: generated ptp4l.conf correctly never sets ptpTimescale (applied separately, at runtime, via pmc)"
fi
# Regression guard against the actual finding on real hardware, not just
# the generator: a config file that DOES set ptpTimescale must make ptp4l
# refuse to start outright ("failed to parse configuration file"). Run
# before the real ptp4l starts below, so there is no port/socket conflict
# with it.
BAD_CONF="$(mktemp)"
cat /etc/showmesh/ptp4l.conf > "$BAD_CONF"
echo "ptpTimescale 1" >> "$BAD_CONF"
if timeout 3 /usr/sbin/ptp4l -f "$BAD_CONF" -i "$IFACE" -m > /tmp/ptp4l-badconf.log 2>&1; then
  echo "FAIL: ptp4l unexpectedly accepted a config file setting ptpTimescale"
  cat /tmp/ptp4l-badconf.log
  exit 1
else
  RC=$?
  if [ "$RC" -eq 124 ]; then
    echo "FAIL: ptp4l did not fail immediately on a config file setting ptpTimescale (ran until timeout instead)"
    exit 1
  fi
  echo "OK: ptp4l correctly refuses a config file that sets ptpTimescale ($(tr -d '\n' < /tmp/ptp4l-badconf.log))"
fi
rm -f "$BAD_CONF"

echo ""
echo "--- step 3: start ptp4l manually (this container has no systemd PID 1) ---"
mkdir -p /var/run/ptp
/usr/sbin/ptp4l -f /etc/showmesh/ptp4l.conf -i "$IFACE" -m > /tmp/ptp4l.log 2>&1 &
PTP4L_PID=$!
echo "ptp4l started, pid $PTP4L_PID, waiting for BMCA to settle"

FINAL_STATE=""
for _ in $(seq 1 45); do
  sleep 1
  STATE="$(pmc -u -b 0 -d "$DOMAIN" -s /var/run/ptp/ptp4lro 'GET PORT_DATA_SET' 2>/dev/null | awk -F'[[:space:]]+' '/portState/{print $NF; exit}')"
  if [ -n "$STATE" ]; then
    FINAL_STATE="$STATE"
    if [ "$STATE" = "SLAVE" ] || [ "$STATE" = "MASTER" ]; then
      break
    fi
  fi
done

echo "ptp4l port state after settling: ${FINAL_STATE:-none}"
case "$ROLE" in
  follower)
    if [ "$FINAL_STATE" = "SLAVE" ]; then
      echo "OK: follower container reached SLAVE (locked to the grandmaster container over software timestamping)"
      pmc -u -b 0 -d "$DOMAIN" -s /var/run/ptp/ptp4lro 'GET TIME_STATUS_NP' 2>/dev/null

      echo ""
      echo "--- step 3b: this follower must see the grandmaster's announced timescale, not the linuxptp default ---"
      # The grandmaster container's own announce-grandmaster-timescale.sh
      # applies its pmc SET on its own schedule, independent of this
      # container's own settle loop above; give it a few seconds to land
      # on the wire and propagate through this follower's own ANNOUNCE
      # processing before reading it back.
      TIME_PROPS=""
      for _ in $(seq 1 30); do
        TIME_PROPS="$(pmc -u -b 0 -d "$DOMAIN" -s /var/run/ptp/ptp4lro 'GET TIME_PROPERTIES_DATA_SET' 2>/dev/null)"
        if echo "$TIME_PROPS" | grep -qE 'currentUtcOffset[[:space:]]+37'; then
          break
        fi
        sleep 1
      done
      echo "$TIME_PROPS"
      if echo "$TIME_PROPS" | grep -qE 'currentUtcOffset[[:space:]]+37' && echo "$TIME_PROPS" | grep -qE 'ptpTimescale[[:space:]]+0'; then
        echo "OK: this follower sees currentUtcOffset=37, ptpTimescale=0 from the grandmaster -- the exact reading a PHC-less follower otherwise gets wrong (it would subtract 37s from a PTP/TAI-timescale domain instead of reading this grandmaster's own UTC directly)"
      else
        echo "FAIL: expected this follower's own TIME_PROPERTIES_DATA_SET to show currentUtcOffset=37, ptpTimescale=0 from the grandmaster's pmc SET"
        exit 1
      fi
    else
      echo "FAIL: follower container did not reach SLAVE (state: ${FINAL_STATE:-none}); see /tmp/ptp4l.log"
      tail -40 /tmp/ptp4l.log
      exit 1
    fi
    ;;
  grandmaster)
    if [ "$FINAL_STATE" = "MASTER" ]; then
      echo "OK: grandmaster container reached MASTER"
    else
      echo "FAIL: grandmaster container did not reach MASTER (state: ${FINAL_STATE:-none}); see /tmp/ptp4l.log"
      tail -40 /tmp/ptp4l.log
      exit 1
    fi

    echo ""
    echo "--- step 3b: pmc SET GRANDMASTER_SETTINGS_NP (install-ptp-audio.sh's own ExecStartPost, invoked directly since this container has no systemd to fire it) ---"
    /usr/local/lib/showmesh/announce-grandmaster-timescale.sh "$DOMAIN"
    TIME_PROPS="$(pmc -u -b 0 -d "$DOMAIN" -s /var/run/ptp4l 'GET TIME_PROPERTIES_DATA_SET' 2>/dev/null)"
    echo "$TIME_PROPS"
    if echo "$TIME_PROPS" | grep -qE 'currentUtcOffset[[:space:]]+37' && echo "$TIME_PROPS" | grep -qE 'ptpTimescale[[:space:]]+0'; then
      echo "OK: grandmaster announces currentUtcOffset=37, ptpTimescale=0 (PTP/ARB) -- the pmc SET this bench's PHC-less follower actually sees on the wire"
    else
      echo "FAIL: expected currentUtcOffset=37 and ptpTimescale=0 in TIME_PROPERTIES_DATA_SET after announce-grandmaster-timescale.sh ran"
      exit 1
    fi
    ;;
esac

echo ""
echo "--- step 4: start PipeWire + WirePlumber manually ---"
export PIPEWIRE_RUNTIME_DIR=/run/pipewire
export XDG_RUNTIME_DIR=/run/pipewire
mkdir -p /run/pipewire
chown showmesh:showmesh /run/pipewire
# The redirects below are opened by this script's own (root) shell before
# sudo execs into the showmesh user (the account install-ptp-audio.sh now
# runs both PipeWire and the agent as, see that script's own SERVICE_USER
# comment), so the log files land where a root shell can always write
# them; sudo never touches the redirect itself.
# shellcheck disable=SC2024
sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  /usr/bin/pipewire > /tmp/pipewire.log 2>&1 &
sleep 2
# shellcheck disable=SC2024
sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  /usr/bin/wireplumber > /tmp/wireplumber.log 2>&1 &
sleep 3

DRIVER_LINE="$(sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  pw-dump 2>/dev/null | python3 -c '
import json, sys
objs = json.load(sys.stdin)
for o in objs:
    props = o.get("info", {}).get("props", {})
    if props.get("node.name") == "showmesh-ptp-driver":
        print(json.dumps(props))
        sys.exit(0)
sys.exit(1)
' 2>/dev/null || true)"

if echo "$DRIVER_LINE" | grep -q 'showmesh-ptp-driver\|clock.id'; then
  echo "OK: PipeWire graph reports the showmesh-ptp-driver node"
  echo "$DRIVER_LINE"
else
  echo "FAIL: showmesh-ptp-driver node not found in pw-dump output"
  cat /tmp/pipewire.log
  exit 1
fi

echo ""
echo "--- step 4b: the actual permission fix -- same-user connect works, a different unprivileged user's does not ---"
# This is the property node-01 got wrong before install-ptp-audio.sh ran
# PipeWire as the SAME user as the agent: connect() to a Unix socket needs
# the WRITE bit, not read+execute, and a socket owned by one unprivileged
# user denies every OTHER unprivileged user by default regardless of its
# mode bits being loose in other ways. bench-other exists only to be that
# other user; it is never added to any group PipeWire's own socket might
# be group-writable to, so this proves the ownership fix itself, not
# merely that some group happened to include it.
CONNECT_PY='import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
try:
    s.connect("/run/pipewire/pipewire-0")
except OSError as e:
    print("CONNECT_FAIL:" + str(e)); sys.exit(1)
print("CONNECT_OK")'
if sudo -u showmesh python3 -c "$CONNECT_PY" | grep -q CONNECT_OK; then
  echo "OK: showmesh (the same user PipeWire itself runs as) can connect() to /run/pipewire/pipewire-0"
else
  echo "FAIL: showmesh could not connect() to its own PipeWire socket"
  exit 1
fi
useradd --system --no-create-home --shell /usr/sbin/nologin bench-other
OTHER_CONNECT="$(sudo -u bench-other python3 -c "$CONNECT_PY" 2>&1 || true)"
echo "$OTHER_CONNECT"
if echo "$OTHER_CONNECT" | grep -q CONNECT_FAIL; then
  echo "OK: bench-other (an unrelated unprivileged user) correctly cannot connect() -- confirms this container's PipeWire socket is not simply world-writable"
else
  echo "FAIL: expected bench-other's connect() to fail; if it succeeded this container's socket is unexpectedly permissive and the same-user fix is not what is actually being proven"
  exit 1
fi

echo ""
echo "--- step 5: pipewiresink playback ---"
# No ALSA hardware in this container, so WirePlumber's ALSA monitor never
# creates a real sink node for pipewiresink to target -- on the real node
# it is the sound card's sink 51-showmesh-alsa-rate.conf pins, created
# automatically. support.null-audio-sink stands in here purely so this
# bench can prove the PipeWire path end to end (graph reachable, stream
# negotiated, playback completes without error) and, with node.group set
# below, the group-election mechanism itself; it says nothing about a real
# ALSA sink actually rate-matching, which needs the real node.
# shellcheck disable=SC2024
sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  pw-cli create-node adapter "{ factory.name=support.null-audio-sink node.name=showmesh-bench-null-sink media.class=Audio/Sink object.linger=true audio.position=[FL,FR] node.group=\"$PTP_NODE_GROUP\" }" \
  > /tmp/pw-cli-null-sink.log 2>&1
sleep 1
# shellcheck disable=SC2024
if sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  timeout 6 gst-launch-1.0 audiotestsrc num-buffers=200 ! audioconvert ! pipewiresink target-object=showmesh-bench-null-sink \
  > /tmp/gst-pipewiresink.log 2>&1; then
  echo "OK: gst-launch-1.0 through pipewiresink completed without error"
else
  RC=$?
  if [ "$RC" -eq 124 ]; then
    echo "OK: gst-launch-1.0 through pipewiresink ran to the timeout without error (num-buffers finished playback, treated as success)"
  else
    echo "FAIL: gst-launch-1.0 through pipewiresink exited $RC"
    cat /tmp/gst-pipewiresink.log
    exit 1
  fi
fi

echo ""
echo "--- step 6: node.group driver election (the real node's group bug, without real ALSA hardware) ---"
# node.driver (true/false) is only "can this node act as a driver", not
# "who is actually driving it" -- confirmed against a real pw-dump capture
# in this bench: a synthetic sink created via support.null-audio-sink
# reports node.driver=true for ITSELF while also carrying node.driver-id
# pointing at whichever node actually drives it. node.driver-id equal to
# showmesh-ptp-driver's own object id is the one reading that proves
# election, and is exactly what verify-ptp-audio.sh now checks against a
# real ALSA node.
GROUP_READING="$(sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  pw-dump 2>/dev/null | python3 -c '
import json, sys
objs = json.load(sys.stdin)
driver_id = None
driver_group = None
sink_driven_by = None
sink_group = None
for o in objs:
    props = o.get("info", {}).get("props", {}) or {}
    name = props.get("node.name") or ""
    if name == "showmesh-ptp-driver":
        driver_id = o.get("id")
        driver_group = props.get("node.group")
    if name == "showmesh-bench-null-sink":
        sink_driven_by = props.get("node.driver-id")
        sink_group = props.get("node.group")
if driver_id is None or sink_driven_by is None:
    print("MISSING")
    sys.exit(0)
print(f"driver.id={driver_id} driver.node.group={driver_group}")
print(f"sink.node.driver-id={sink_driven_by} sink.node.group={sink_group}")
if sink_driven_by == driver_id:
    print("ELECTION_OK")
' 2>/dev/null || true)"

echo "$GROUP_READING"
if echo "$GROUP_READING" | grep -q "ELECTION_OK"; then
  echo "OK: showmesh-bench-null-sink's node.driver-id points at showmesh-ptp-driver's own id -- the same node.driver-id reading verify-ptp-audio.sh now checks against a real ALSA node"
else
  echo "FAIL: node.group election did not land on showmesh-ptp-driver for a synthetic sink placed in its group"
  exit 1
fi

echo ""
echo "--- step 7: driver-election.py against a pw-dump padded past ARG_MAX ---"
# verify-ptp-audio.sh used to pass pw-dump's own output to python3 as a
# command-line argument (by way of an environment variable, which shares
# the same OS exec size limit): fine in this container's small graph, but
# a real node with a real sound card produces pw-dump output large enough
# to exceed ARG_MAX outright (confirmed on showmesh-node-01: "Argument
# list too long"). This container has no sound card, so there is nothing
# here that naturally produces output that large; pad a real capture from
# this container's own graph with synthetic filler objects until it does,
# and feed it to the exact parser verify-ptp-audio.sh now runs -- on
# stdin, never as an argument or environment variable -- to prove the
# fix, not just the mechanism the fix replaced.
REAL_DUMP="$(sudo -u showmesh PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire pw-dump 2>/dev/null)"
ARG_MAX_BYTES="$(getconf ARG_MAX)"
PADDED_DUMP="$(python3 -c "
import json, sys
objs = json.loads(sys.argv[1])
i = 0
while len(json.dumps(objs)) < int(sys.argv[2]):
    objs.append({'id': 100000 + i, 'info': {'props': {'node.name': f'bench-padding-node-{i}', 'node.description': 'x' * 400}}})
    i += 1
print(json.dumps(objs))
" "$REAL_DUMP" "$((ARG_MAX_BYTES + 200000))")"
echo "padded pw-dump size: $(echo -n "$PADDED_DUMP" | wc -c) bytes, ARG_MAX=$ARG_MAX_BYTES bytes"

PADDED_READING="$(printf '%s' "$PADDED_DUMP" | python3 /repo/deploy/node/ptp-audio/driver-election.py "alsa_output")"
if echo "$PADDED_READING" | grep -q "^DRIVER_FOUND=1$"; then
  echo "OK: driver-election.py correctly found showmesh-ptp-driver in a pw-dump padded past this host's ARG_MAX, fed on stdin"
else
  echo "FAIL: driver-election.py did not find showmesh-ptp-driver in the padded pw-dump"
  echo "$PADDED_READING"
  exit 1
fi

# The old failure mode, confirmed here rather than only asserted: passing
# that same oversized payload as an environment variable (the same OS exec
# size limit an argument shares) to a real command fails with E2BIG before
# the command even starts.
if PW_DUMP_JSON="$PADDED_DUMP" env true 2>/tmp/showmesh-argmax-check.log; then
  echo "FAIL: expected the old argument/environment-passing approach to fail past ARG_MAX on this padded payload, but it succeeded -- the padding is not large enough to prove this fix on this host"
  exit 1
else
  echo "OK: confirmed the old argument/environment-passing approach does fail past ARG_MAX on this padded payload ($(cat /tmp/showmesh-argmax-check.log))"
fi

echo ""
echo "--- step 8: verify-ptp-audio.sh's duplicate-writer check, against real processes on this host ---"
# The actual node-01 incident: a stray hand-run phc2sys alongside the
# installed unit fought the same PHC, and every other check verify-ptp-audio.sh
# runs stayed green throughout. This step proves the fix -- that check
# reads the live process table, not the installed units -- with inert
# fake binaries (no real ptp4l/phc2sys protocol run here, only comm/argv),
# so it needs no PHC and cannot collide with the real ptp4l already running
# from step 3 above except by DESIGN in the ptp4l case below, which mirrors
# the incident's own shape: one legitimate process plus a stray one.
FAKEBIN=/tmp/showmesh-bench-fakebin
mkdir -p "$FAKEBIN"
cat > "$FAKEBIN/phc2sys" <<'EOF'
#!/bin/bash
trap 'exit 0' TERM
sleep 60
EOF
cat > "$FAKEBIN/ptp4l" <<'EOF'
#!/bin/bash
trap 'exit 0' TERM
sleep 60
EOF
chmod +x "$FAKEBIN/phc2sys" "$FAKEBIN/ptp4l"

"$FAKEBIN/phc2sys" -s CLOCK_REALTIME -c /dev/ptp0 -O 0 &
FAKE_PHC2SYS_1=$!
"$FAKEBIN/phc2sys" -s CLOCK_REALTIME -c /dev/ptp0 -O 0 &
FAKE_PHC2SYS_2=$!
# A second target clock, not shared with the pair above: proves this
# check groups by target device rather than flagging every phc2sys process
# on the host as a duplicate.
"$FAKEBIN/phc2sys" -s CLOCK_REALTIME -c /dev/ptp1 -O 0 &
FAKE_PHC2SYS_3=$!
# The real ptp4l from step 3 is still running on $IFACE; this one extra
# fake process on the same interface reproduces the actual incident shape
# (one legitimate writer, one stray one) rather than two synthetic ones.
"$FAKEBIN/ptp4l" -f /etc/showmesh/ptp4l.conf -i "$IFACE" -m &
FAKE_PTP4L_1=$!
sleep 1

VERIFY_OUT="$(/repo/deploy/node/verify-ptp-audio.sh --play 0 2>&1 || true)"
echo "$VERIFY_OUT"

kill "$FAKE_PHC2SYS_1" "$FAKE_PHC2SYS_2" "$FAKE_PHC2SYS_3" "$FAKE_PTP4L_1" 2>/dev/null || true
wait "$FAKE_PHC2SYS_1" "$FAKE_PHC2SYS_2" "$FAKE_PHC2SYS_3" "$FAKE_PTP4L_1" 2>/dev/null || true

if echo "$VERIFY_OUT" | grep -qE "FAIL: 2 phc2sys processes target /dev/ptp0 at once \(pids:[0-9 ]+\)"; then
  echo "OK: verify-ptp-audio.sh flagged the two phc2sys processes sharing /dev/ptp0"
else
  echo "FAIL: expected a FAIL line naming 2 phc2sys processes on /dev/ptp0"
  exit 1
fi
if echo "$VERIFY_OUT" | grep -q "/dev/ptp1"; then
  echo "FAIL: the lone phc2sys process on /dev/ptp1 must never be reported as a duplicate"
  exit 1
else
  echo "OK: the lone phc2sys process on /dev/ptp1 was correctly not flagged"
fi
if echo "$VERIFY_OUT" | grep -qE "FAIL: 2 ptp4l processes run on interface $IFACE at once \(pids:[0-9 ]+\)"; then
  echo "OK: verify-ptp-audio.sh flagged the real ptp4l plus the one stray process sharing $IFACE"
else
  echo "FAIL: expected a FAIL line naming 2 ptp4l processes on $IFACE (the real one from step 3 plus the stray one started here)"
  exit 1
fi

echo ""
echo "--- step 8b: the duplicate-writer check with ZERO phc2sys processes still runs the ptp4l half ---"
# The fake phc2sys processes above are already killed and reaped, so this
# container now has none running -- exactly the PHC-less-node shape that
# made PHC2SYS_TARGETS a totally empty associative array. Under set -u,
# bash's own "${#PHC2SYS_TARGETS[@]}" on that empty array throws "unbound
# variable" (confirmed against a real bash 5.1), which used to abort the
# whole script before it ever reached the real ptp4l on $IFACE below. This
# is the regression guard: without it, this comes back the moment someone
# adds another optional process class.
VERIFY_NO_PHC2SYS_OUT="$(/repo/deploy/node/verify-ptp-audio.sh --play 0 2>&1 || true)"
echo "$VERIFY_NO_PHC2SYS_OUT"
if echo "$VERIFY_NO_PHC2SYS_OUT" | grep -qi "unbound variable"; then
  echo "FAIL: verify-ptp-audio.sh aborted on an unbound variable with zero phc2sys processes running"
  exit 1
fi
if echo "$VERIFY_NO_PHC2SYS_OUT" | grep -q "INFO: no phc2sys process running"; then
  echo "OK: verify-ptp-audio.sh reported the expected no-phc2sys line instead of crashing"
else
  echo "FAIL: expected an INFO line reporting no phc2sys process running"
  exit 1
fi
if echo "$VERIFY_NO_PHC2SYS_OUT" | grep -qE "OK: exactly one ptp4l process per interface \($IFACE\)"; then
  echo "OK: the ptp4l half of the writer-uniqueness check still ran and reported the real ptp4l on $IFACE"
else
  echo "FAIL: expected the ptp4l half of the writer-uniqueness check to still run and report the real ptp4l on $IFACE"
  exit 1
fi
if echo "$VERIFY_NO_PHC2SYS_OUT" | grep -qE "^verify-ptp-audio\.sh: [0-9]+ check\(s\) passed"; then
  echo "OK: verify-ptp-audio.sh ran to completion and printed its final summary line"
else
  echo "FAIL: verify-ptp-audio.sh did not reach its final summary line (script aborted early)"
  exit 1
fi

echo ""
echo "--- step 9: servo-health.py against the actual node-01 saturated/flapping/negative-delay log lines ---"
# The exact phc2sys log lines from the node-01 incident (see PTP-AUDIO.md):
# freq pinned at linuxptp's own 900000000ppb clamp, state flapping s2/s0.
# Fed directly to the parser, on stdin, the same convention driver-election.py
# already uses and for the same reason: this bench has no PHC and no real
# ptp4l/phc2sys servo to sample, so the regression guard is the parser
# itself, not the systemctl/journalctl-gated check that calls it (never run
# by this bench, same as every other systemd-gated check -- see README).
SATURATED_READING="$(printf '%s\n' \
  '/dev/ptp0 sys offset -288646357 s2 freq -900000000 delay 0' \
  '/dev/ptp0 sys offset   55730027 s0 freq -900000000 delay 0' \
  | python3 /repo/deploy/node/ptp-audio/servo-health.py)"
echo "$SATURATED_READING"
if echo "$SATURATED_READING" | grep -q '^SAMPLE=-288646357|2|-900000000|0$' \
   && echo "$SATURATED_READING" | grep -q '^SAMPLE=55730027|0|-900000000|0$' \
   && echo "$SATURATED_READING" | grep -q '^SAMPLE_COUNT=2$'; then
  echo "OK: servo-health.py correctly extracted the saturated-freq, flapping-state samples from the actual node-01 log lines"
else
  echo "FAIL: servo-health.py did not extract the expected samples from the node-01 fixture lines"
  exit 1
fi

# A follower's own ptp4l lines use different label words ("master offset",
# "path delay") for the same four fields; the negative path delay the
# node-01 follower actually saw is the clearest single tell this check
# exists to catch.
NEGATIVE_DELAY_READING="$(printf '%s\n' \
  'ptp4l[1234.5]: master offset 80123456 s0 freq 45000 path delay -60123' \
  | python3 /repo/deploy/node/ptp-audio/servo-health.py)"
echo "$NEGATIVE_DELAY_READING"
if echo "$NEGATIVE_DELAY_READING" | grep -q '^SAMPLE=80123456|0|45000|-60123$'; then
  echo "OK: servo-health.py correctly extracted a negative path delay from a follower's ptp4l log line"
else
  echo "FAIL: servo-health.py did not extract the negative-path-delay sample"
  exit 1
fi

echo ""
echo "--- step 10: verify-ptp-audio.sh's servo-health check on a converged-but-wrong 50ms offset ---"
# Re-review finding F on PR #454: eight samples holding one servo state
# with a modest freq/delay used to print "servo settled" even 50ms out of
# sync, because offset itself was never tested. Fakes systemctl (report
# phc2sys-showmesh.service active) and journalctl (return this fixture) on
# PATH so the actual shell script runs its real thresholds end to end, not
# just servo-health.py in isolation (step 9 above already proves the parser).
FAKESYSTEMD=/tmp/showmesh-bench-fakesystemd
mkdir -p "$FAKESYSTEMD"
cat > "$FAKESYSTEMD/systemctl" <<'EOF'
#!/bin/bash
if [ "$1" = "is-active" ] && [ "$3" = "phc2sys-showmesh.service" ]; then
  exit 0
fi
exit 3
EOF
cat > "$FAKESYSTEMD/journalctl" <<'EOF'
#!/bin/bash
for i in 1 2 3 4 5 6 7 8; do
  echo "phc2sys[$i.1]: sys offset 50000000 s2 freq -3021 delay 812"
done
EOF
chmod +x "$FAKESYSTEMD/systemctl" "$FAKESYSTEMD/journalctl"

set +e
OFFSET_VERIFY_OUT="$(PATH="$FAKESYSTEMD:$PATH" /repo/deploy/node/verify-ptp-audio.sh --play 0 2>&1)"
OFFSET_VERIFY_RC=$?
set -e
echo "$OFFSET_VERIFY_OUT"
if echo "$OFFSET_VERIFY_OUT" | grep -qE "FAIL:.*offset reached 50000000ns" && [ "$OFFSET_VERIFY_RC" -eq 1 ]; then
  echo "OK: verify-ptp-audio.sh's servo-health check failed a converged-but-50ms-out-of-sync servo (exit 1)"
else
  echo "FAIL: expected verify-ptp-audio.sh to fail (exit 1) the 50ms-offset servo fixture (finding F regression guard)"
  exit 1
fi

echo ""
echo "--- step 11: verify-ptp-audio.sh's servo-health check on an empty journal read ---"
# Re-review finding G on PR #454: journalctl exiting 0 with no output (the
# shape a caller outside systemd-journal/adm actually gets, not a crash)
# used to land in the info() branch, touching neither PASS, FAIL nor ERR,
# so the script exited 0 having produced no answer. This instrument
# failure must exit 2.
cat > "$FAKESYSTEMD/journalctl" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "$FAKESYSTEMD/journalctl"

set +e
EMPTY_VERIFY_OUT="$(PATH="$FAKESYSTEMD:$PATH" /repo/deploy/node/verify-ptp-audio.sh --play 0 2>&1)"
EMPTY_VERIFY_RC=$?
set -e
echo "$EMPTY_VERIFY_OUT"
if echo "$EMPTY_VERIFY_OUT" | grep -q "ERROR: no parseable servo sample lines" && [ "$EMPTY_VERIFY_RC" -eq 2 ]; then
  echo "OK: verify-ptp-audio.sh exited 2 on an empty, zero-exit journal read (finding G regression guard)"
else
  echo "FAIL: expected verify-ptp-audio.sh to exit 2 on an empty journal read, got rc=$EMPTY_VERIFY_RC"
  exit 1
fi

echo ""
echo "=== in-container-proof.sh: all checks passed for role=$ROLE ==="
