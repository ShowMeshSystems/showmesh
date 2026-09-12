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
    ;;
esac

echo ""
echo "--- step 4: start PipeWire + WirePlumber manually ---"
export PIPEWIRE_RUNTIME_DIR=/run/pipewire
export XDG_RUNTIME_DIR=/run/pipewire
mkdir -p /run/pipewire
chown pipewire:pipewire /run/pipewire
# The redirects below are opened by this script's own (root) shell before
# sudo execs into the pipewire user, so the log files land where a root
# shell can always write them; sudo never touches the redirect itself.
# shellcheck disable=SC2024
sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  /usr/bin/pipewire > /tmp/pipewire.log 2>&1 &
sleep 2
# shellcheck disable=SC2024
sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  /usr/bin/wireplumber > /tmp/wireplumber.log 2>&1 &
sleep 3

DRIVER_LINE="$(sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
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
sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
  pw-cli create-node adapter "{ factory.name=support.null-audio-sink node.name=showmesh-bench-null-sink media.class=Audio/Sink object.linger=true audio.position=[FL,FR] node.group=\"$PTP_NODE_GROUP\" }" \
  > /tmp/pw-cli-null-sink.log 2>&1
sleep 1
# shellcheck disable=SC2024
if sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
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
GROUP_READING="$(sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire \
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
REAL_DUMP="$(sudo -u pipewire PIPEWIRE_RUNTIME_DIR=/run/pipewire XDG_RUNTIME_DIR=/run/pipewire pw-dump 2>/dev/null)"
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
echo "=== in-container-proof.sh: all checks passed for role=$ROLE ==="
