#!/usr/bin/env bash
# Verify the repository and GitHub state that can be checked mechanically
# before a ShowMesh pull request is called ready to merge. This does not run
# tests or perform a review; those results are reported separately in the PR.
set -euo pipefail

fail() {
  echo "pr-ready-check: $*" >&2
  exit 1
}

command -v git >/dev/null 2>&1 || fail "git is not installed"
command -v gh >/dev/null 2>&1 || fail "gh is not installed"
command -v jq >/dev/null 2>&1 || fail "jq is not installed"

root="$(git rev-parse --show-toplevel 2>/dev/null)" || fail "not inside a Git repository"
cd "$root"

branch="$(git branch --show-current)"
[ -n "$branch" ] || fail "HEAD is detached"
[ "$branch" != "main" ] || fail "refusing to check main; use a task branch"

dirty="$(git status --porcelain)"
[ -z "$dirty" ] || fail "working tree is not clean"

upstream="$(git rev-parse --abbrev-ref '@{upstream}' 2>/dev/null)" || \
  fail "branch has no upstream; push it before checking PR readiness"
head_oid="$(git rev-parse HEAD)"
expected_head="${SHOWMESH_PR_READY_EXPECTED_HEAD:-}"
if [ -n "$expected_head" ] && [ "$head_oid" != "$expected_head" ]; then
  fail "local HEAD $head_oid changed from the gated commit $expected_head"
fi
upstream_oid="$(git rev-parse '@{upstream}')"
[ "$head_oid" = "$upstream_oid" ] || \
  fail "local HEAD does not match $upstream; push the final commit first"

pr_json="$(gh pr view --json number,url,headRefOid,isDraft,mergeStateStatus,statusCheckRollup 2>/dev/null)" || \
  fail "no open pull request found for branch $branch"

pr_number="$(jq -r '.number' <<<"$pr_json")"
pr_url="$(jq -r '.url' <<<"$pr_json")"
pr_head="$(jq -r '.headRefOid' <<<"$pr_json")"
is_draft="$(jq -r '.isDraft' <<<"$pr_json")"
merge_state="$(jq -r '.mergeStateStatus' <<<"$pr_json")"

[ "$pr_head" = "$head_oid" ] || \
  fail "PR #$pr_number head $pr_head does not match local HEAD $head_oid"
[ "$is_draft" = "false" ] || fail "PR #$pr_number is still a draft"
[ "$merge_state" = "CLEAN" ] || \
  fail "PR #$pr_number merge state is $merge_state, not CLEAN"

check_count="$(jq '.statusCheckRollup | length' <<<"$pr_json")"
[ "$check_count" -gt 0 ] || fail "PR #$pr_number has no reported GitHub checks"

bad_checks="$(jq -r '
  [.statusCheckRollup[]
   | {name: (.name // .context // "unnamed check"), result: (.conclusion // .state // "PENDING")}
   | select(.result != "SUCCESS")
   | "\(.name)=\(.result)"]
  | join(", ")
' <<<"$pr_json")"
[ -z "$bad_checks" ] || fail "PR #$pr_number has checks that have not passed: $bad_checks"

echo "pr-ready-check: PASS"
echo "pull-request: #$pr_number $pr_url"
echo "commit: $head_oid"
echo "github-checks: $check_count passed"
echo "merge-state: $merge_state"
