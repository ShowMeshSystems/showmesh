#!/usr/bin/env bash
#
# Records every distinct playlist-entry observation the coordinator accepts
# during a run. The coordinator stores only the latest observation per FPP
# instance, so an entry that has already advanced is otherwise unrecoverable:
# without this, a two-entry run leaves one observation on file and no way to
# tell whether the first one ever arrived.
#
# usage: capture-observations.sh <admin-token> <output.jsonl> [seconds] [coordinator-url]
set -uo pipefail

TOKEN="${1:?admin token}"
OUT="${2:?output path}"
SECONDS_TO_RUN="${3:-75}"
COORD="${4:-http://localhost:8080}"

: > "$OUT"
last=""
end=$(( $(date +%s) + SECONDS_TO_RUN ))
while [ "$(date +%s)" -lt "$end" ]; do
  body=$(curl -s -H "Authorization: Bearer $TOKEN" \
    "$COORD/api/v1/integrations/fpp/playlist-entry-observations" \
    | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin).get("observations",[])))' 2>/dev/null)
  if [ -n "$body" ] && [ "$body" != "$last" ]; then
    printf '%s %s\n' "$(date -u +%FT%TZ)" "$body" >> "$OUT"
    last="$body"
  fi
  sleep 0.1
done
