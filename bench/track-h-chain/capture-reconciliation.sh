#!/usr/bin/env bash
#
# Records the coordinator's reconciliation verdict for one FPP instance as it
# changes during a run: which playlist entry and Cue an observation resolved
# to, or the named reason it did not.
#
# usage: capture-reconciliation.sh <output.txt> [seconds] [instance-uuid] [coordinator-url]
# ADMIN_TOKEN must be set in the environment; it is never passed as an
# argument so it does not appear in `ps` output.
set -uo pipefail

TOKEN="${ADMIN_TOKEN:?set ADMIN_TOKEN}"
OUT="${1:?output path}"
SECONDS_TO_RUN="${2:-75}"
INSTANCE="${3:?FPP instance UUID}"
COORD="${4:-http://localhost:8080}"

: > "$OUT"
last=""
end=$(( $(date +%s) + SECONDS_TO_RUN ))
while [ "$(date +%s)" -lt "$end" ]; do
  # -K with process substitution keeps the token out of curl's own argv,
  # which `ps` would otherwise show to any user on the host.
  cur=$(curl -s -K <(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN") \
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
