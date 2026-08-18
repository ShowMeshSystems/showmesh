#!/bin/bash
BASE=http://localhost:8091
OUT=$1
DURATION=$2
INTERVAL=$3
END=$(( $(date +%s) + DURATION ))
: > "$OUT"
while [ "$(date +%s)" -lt "$END" ]; do
  TS=$(date +%s.%N)
  BODY=$(curl -sS -m3 "$BASE/api/fppd/status")
  echo "{\"probe_ts\":$TS,\"body\":$BODY}" >> "$OUT"
  if [ "$INTERVAL" != "0" ]; then
    sleep "$INTERVAL"
  fi
done
