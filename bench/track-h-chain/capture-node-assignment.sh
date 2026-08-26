#!/usr/bin/env bash
#
# Records the node's OWN persisted render assignment as it changes: the FSEQ
# filename it opened, that file's content hash, and when it applied it.
#
# This is the only place a node states which file it is rendering. The
# periodic render report carries no filename and no content hash, so until
# that changes this file is the node-side evidence a content swap actually
# happened, and its appliedAt is what makes that evidence post-date the
# activation that caused it.
#
# usage: capture-node-assignment.sh <output.txt> [seconds] [agent-asset-dir]
set -uo pipefail

OUT="${1:?output path}"
SECONDS_TO_RUN="${2:-75}"
ASSET_DIR="${3:-$HOME/showmesh-dev-node/assets}"
STATE="$ASSET_DIR/.render-state/assignments.json"

: > "$OUT"
last=""
end=$(( $(date +%s) + SECONDS_TO_RUN ))
while [ "$(date +%s)" -lt "$end" ]; do
  cur=$(python3 -c "
import json
try:
    d = json.load(open('$STATE'))
except Exception:
    raise SystemExit
print(' | '.join('%s %s %s appliedAt=%s' % (
    a['surfaceId'], a['rawParams'].get('fseqFilename'),
    a['rawParams'].get('fseqContentHash'), a['appliedAt']) for a in d))
" 2>/dev/null)
  if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
    printf '%s %s\n' "$(date -u +%FT%TZ)" "$cur" >> "$OUT"
    last="$cur"
  fi
  sleep 1
done
