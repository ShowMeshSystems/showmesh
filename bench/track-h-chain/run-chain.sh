#!/usr/bin/env bash
#
# Runs Track H seam H7's multi-sequence case against already-running binaries
# and prints the evidence each component produced, in the order it happened.
#
# This does not stand the stack up. Read README.md first: it lists what has to
# exist before this script says anything meaningful, and several of those
# preconditions are not obvious.
#
# Environment:
#   ADMIN_TOKEN    required, a principal holding config:write and cuecatalog:deploy
#   INSTANCE_UUID  required, the FPP instance UUID the plugin reports
#   NODE           default dev-node-01
#   PLAYLIST_ID    default lane14-fpp-main, the show.playlist object id
#   FPP_PLAYLIST   default "Lane14 Main", the playlist name on FPP
#   COORD          default http://localhost:8080
#   FPP            default http://localhost:8090
#   ASSET_DIR      default $HOME/showmesh-dev-node/assets
#   OUT_DIR        default ./captures
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

: "${ADMIN_TOKEN:?set ADMIN_TOKEN}"
: "${INSTANCE_UUID:?set INSTANCE_UUID}"
NODE="${NODE:-dev-node-01}"
PLAYLIST_ID="${PLAYLIST_ID:-lane14-fpp-main}"
FPP_PLAYLIST="${FPP_PLAYLIST:-Lane14 Main}"
COORD="${COORD:-http://localhost:8080}"
FPP="${FPP:-http://localhost:8090}"
ASSET_DIR="${ASSET_DIR:-$HOME/showmesh-dev-node/assets}"
OUT_DIR="${OUT_DIR:-$HERE/captures}"
RUN_SECONDS="${RUN_SECONDS:-90}"

mkdir -p "$OUT_DIR"
api() { curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$@"; }
step() { printf '\n=== %s ===\n' "$1"; }
fppIdle() {
  curl -fsS "$FPP/api/system/status" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["status_name"])'
}

step "1. the coordinator holds the definition the plugin posted"
api "$COORD/api/v1/integrations/fpp/playlist-definitions" \
  | python3 -c 'import json,sys
for d in json.load(sys.stdin)["definitions"]:
    print(d["playlistName"], d["playlistHash"], "entries", d["entryCount"], "captured", d["capturedAt"])'

step "2. readiness before the run"
api "$COORD/api/v1/integrations/fpp/playlists/$PLAYLIST_ID/readiness" \
  | python3 -c 'import json,sys
d=json.load(sys.stdin)
print("ready:", d.get("ready"), "| failingCondition:", d.get("failingCondition"),
      "| reason:", d.get("reason"), "| warning:", d.get("warning"))'

step "3. deploy the resolved cue catalog to $NODE"
# Nothing deploys a catalog on its own. A node holding none, or holding a
# revision older than the one the coordinator is authorizing against, refuses
# every activation. This is the step whose absence looks like a code bug.
api -X POST -H 'Content-Type: application/json' -d '{}' \
  "$COORD/api/v1/nodes/$NODE/cue-catalog/deploy" \
  | python3 -c 'import json,sys
c=json.load(sys.stdin)["command"]
print("outcome:", c["outcome"], "| acknowledgedRevision:", c.get("acknowledgedRevision"),
      "| show:", c.get("show"), "| generation:", c.get("generation"))'

step "4. wait for FPP to be idle, then run the playlist and capture everything"
while [ "$(fppIdle)" != "idle" ]; do sleep 3; done

"$HERE/capture-observations.sh"     "$ADMIN_TOKEN" "$OUT_DIR/observations.jsonl"  "$RUN_SECONDS" "$COORD" &
obs=$!
"$HERE/capture-reconciliation.sh"   "$ADMIN_TOKEN" "$OUT_DIR/reconciliation.txt"  "$RUN_SECONDS" "$INSTANCE_UUID" "$COORD" &
rec=$!
"$HERE/capture-node-assignment.sh"  "$OUT_DIR/node-assignment.txt"                "$RUN_SECONDS" "$ASSET_DIR" &
asg=$!

sleep 1
auditFrom=$(api "$COORD/api/v1/audit?order=desc&limit=1" \
  | python3 -c 'import json,sys; e=json.load(sys.stdin)["entries"]; print(e[0]["id"] if e else 0)')

curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "[\"$FPP_PLAYLIST\"]" "$FPP/api/command/Start%20Playlist" >/dev/null
printf 'started %s at %s\n' "$FPP_PLAYLIST" "$(date -u +%FT%TZ)"

wait "$obs" "$rec" "$asg"

step "5. what the plugin reported, from the coordinator's own read route"
python3 - "$OUT_DIR/observations.jsonl" <<'PY'
import json, sys
for line in open(sys.argv[1]):
    ts, _, body = line.partition(' ')
    for o in json.loads(body):
        print(ts, 'seq', o.get('sequence'), o.get('action'),
              'section=%s' % o.get('section'), 'position=%s' % o.get('position'),
              'sequenceFilename=%s' % o.get('sequenceFilename'),
              'unavailable=%s' % o.get('unavailable'))
PY

step "6. what the coordinator resolved each observation to"
cat "$OUT_DIR/reconciliation.txt"

step "7. what the coordinator dispatched, from its own audit log"
api "$COORD/api/v1/audit?order=desc&limit=200" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
for e in reversed(d['entries']):
    if e['id'] <= $auditFrom or e['action'] != 'cue.activate' or e['kind'] != 'outcome':
        continue
    p = e['params']
    print(e['id'], e['timestamp'], e['target'], e['outcome'] or '-',
          e.get('outcomeReason') or '-', 'entry=' + p.get('entryId', ''),
          'cue=' + p.get('cueId', ''))
"

step "8. what the node itself recorded rendering"
cat "$OUT_DIR/node-assignment.txt"

step "read this before believing the result"
cat <<'NOTE'
Step 8 is the only node-side statement of which file was rendered, and it is
read off the node's own disk rather than any coordinator route, because the
render report carries no filename and no content hash. Every appliedAt there
should post-date the dispatch in step 7 that caused it.

A container closes software behavior only. Real FPP hardware, a real render
node, real NDI output and a real wall are separate evidence and none of them
are exercised here.
NOTE
