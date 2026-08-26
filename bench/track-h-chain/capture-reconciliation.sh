#!/usr/bin/env bash
#
# Records the coordinator's reconciliation verdict for one FPP instance as it
# changes during a run: which playlist entry and Cue an observation resolved
# to, or the named reason it did not.
#
# usage: capture-reconciliation.sh <admin-token> <output.txt> [seconds] [instance-uuid] [coordinator-url]
set -uo pipefail

TOKEN="${1:?admin token}"
OUT="${2:?output path}"
SECONDS_TO_RUN="${3:-75}"
INSTANCE="${4:?FPP instance UUID}"
COORD="${5:-http://localhost:8080}"

: > "$OUT"
last=""
end=$(( $(date +%s) + SECONDS_TO_RUN ))
while [ "$(date +%s)" -lt "$end" ]; do
  cur=$(curl -s -H "Authorization: Bearer $TOKEN" \
    "$COORD/api/v1/integrations/fpp/playlist-entry-observations/$INSTANCE/reconciliation" \
    | python3 -c 'import json,sys
d=json.load(sys.stdin)
print(d.get("outcome"), "|", d.get("reason"), "| show=", d.get("show"), "| entry=", d.get("entryId"), "| cue=", d.get("cueId"), "| pos=", d.get("observedPosition"))' 2>/dev/null)
  if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
    printf '%s %s\n' "$(date -u +%FT%TZ)" "$cur" >> "$OUT"
    last="$cur"
  fi
  sleep 0.5
done
