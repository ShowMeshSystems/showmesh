#!/usr/bin/env bash
# Applies a grandmaster-role node's announced PTP timescale via pmc's
# runtime management protocol, never through ptp4l.conf: `ptpTimescale` is
# NOT a valid ptp4l.conf option -- putting it there makes ptp4l refuse to
# start at all ("failed to parse configuration file"), confirmed the hard
# way on real hardware. GRANDMASTER_SETTINGS_NP is a runtime SET that must
# be re-applied every time ptp4l (re)starts, which is why this script is
# install-ptp-audio.sh's ExecStartPost for ptp4l-showmesh.service on a
# grandmaster-role node, not a one-shot step taken only at install time.
#
# currentUtcOffset 37, ptpTimescale 0 (PTP/ARB, not the PTP/TAI default):
# every node in this domain then reads the SAME wall-clock number whether
# it has a PHC (this node, kept at UTC by phc2sys-showmesh.service) or
# disciplines CLOCK_REALTIME directly (a PHC-less follower). This
# grandmaster's own UTC becomes the domain's time reference, not a
# traceable TAI source -- see PTP-AUDIO.md for that tradeoff. Measured on
# real hardware (showmesh-node-01 grandmaster, a Raspberry Pi 3B+
# follower) before this existed: the default ptpTimescale 1 (PTP/TAI) put
# the two nodes' wall clocks exactly 36.7 seconds apart (TAI minus UTC),
# because a TAI-domain follower subtracts currentUtcOffset from what it
# reads. 37 is the current TAI-UTC offset; it only changes on a leap
# second, which is not tracked here -- if one occurs, this value needs an
# update, the same way any other software carrying this constant does.
#
# Usage: announce-grandmaster-timescale.sh <domain>
set -u

DOMAIN="${1:?usage: announce-grandmaster-timescale.sh <domain>}"
PMC_SOCKET=/var/run/ptp4l

for _ in $(seq 1 10); do
  if pmc -u -b 0 -d "$DOMAIN" -s "$PMC_SOCKET" \
    'SET GRANDMASTER_SETTINGS_NP clockClass 248 clockAccuracy 0xfe offsetScaledLogVariance 0xffff currentUtcOffset 37 leap61 0 leap59 0 currentUtcOffsetValid 0 ptpTimescale 0 timeTraceable 0 frequencyTraceable 0 timeSource 0xa0' \
    2>/dev/null | grep -q RESPONSE; then
    echo "announce-grandmaster-timescale.sh: applied GRANDMASTER_SETTINGS_NP (ptpTimescale=0, currentUtcOffset=37) on domain $DOMAIN"
    exit 0
  fi
  sleep 1
done

# Never fails the parent ptp4l-showmesh.service over this: ptp4l is still
# disciplining time correctly, just still announcing the default PTP/TAI
# timescale, which is the specific, narrower problem this reports instead
# of taking down PTP discipline entirely over it.
echo "announce-grandmaster-timescale.sh: could not apply GRANDMASTER_SETTINGS_NP after 10 attempts (domain $DOMAIN, socket $PMC_SOCKET); this node is likely still announcing the default PTP/TAI timescale, which puts a PHC-less follower's wall clock ~37s off this node's -- see PTP-AUDIO.md" >&2
exit 0
