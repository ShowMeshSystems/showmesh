#!/usr/bin/env python3
# Reads a pw-dump JSON array on stdin and reports whether the ALSA sink's
# node.driver-id actually points at showmesh-ptp-driver's own object id --
# the reading that answers "who is driving this node," not merely "does
# showmesh-ptp-driver exist in the graph." See verify-ptp-audio.sh for why
# a node's own node.driver flag does not answer that question.
#
# Argument 1: a regex matched against node.name to find the ALSA sink(s).
# Reads pw-dump's own output on stdin, never as an argument or environment
# variable: a real node's pw-dump output can run past the process argument
# and environment size limit (ARG_MAX), which fails execve with no relation
# to whether the driver election it describes is actually working.
import json
import re
import sys

alsa_re = re.compile(sys.argv[1])
try:
    objs = json.load(sys.stdin)
except Exception as e:
    print(f"pw-dump output could not be parsed as JSON: {e}", file=sys.stderr)
    sys.exit(1)

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
