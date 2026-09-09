#!/usr/bin/env bash
# check_dco.sh — enforce the Developer Certificate of Origin (DCO 1.1).
#
# Motivra requires a `Signed-off-by:` trailer on every content commit
# (CONTRIBUTING.md "License and Developer Certificate of Origin (DCO)";
# ADR-0006). This script is the enforcement arm: it fails when any commit
# in the verified range lacks a sign-off whose name and email match the
# commit author (the DCO 1.1 requirement).
#
# Usage:
#   bash scripts/check_dco.sh [BASE] [HEAD]
#
#   BASE   commit-ish to verify from (exclusive). Defaults to
#          "$DCO_BASE", then "$BASE_SHA" (set in CI), then empty.
#   HEAD   commit-ish to verify up to (inclusive). Defaults to HEAD.
#
#   Locally:        bash scripts/check_dco.sh origin/main
#   Pull requests:  bash scripts/check_dco.sh "$BASE_SHA" HEAD
#
# Range semantics:
#   - Merge commits (2+ parents) are skipped: a merge adds no new content
#     authorship; every content commit inside the merged branch must carry
#     its own sign-off.
#   - Commits whose committer is GitHub's web identity (`GitHub
#     <noreply@github.com>`) are skipped, matching the behaviour of the
#     canonical GitHub DCO app for UI-created commits.
#   - An empty, all-zero, or unknown BASE (e.g. the very first push) means
#     "no committed range to verify" and passes with a notice.
#
# Exit codes: 0 = all verified commits signed; 1 = at least one violation
# (or git error); 2 = usage error.

set -euo pipefail

BASE="${1:-${DCO_BASE:-${BASE_SHA:-}}}"
HEAD="${2:-HEAD}"

if [ -z "$HEAD" ]; then
    echo "usage: $0 [BASE] [HEAD]" >&2
    exit 2
fi

if ! git rev-parse --verify -q "$HEAD" >/dev/null; then
    echo "DCO check: HEAD '$HEAD' is not a valid commit" >&2
    exit 2
fi

# Resolve or validate the base.
if [ -n "$BASE" ]; then
    if ! git rev-parse --verify -q "$BASE" >/dev/null; then
        BASE=""
    fi
fi

if [ -z "$BASE" ]; then
    echo "DCO check: no base commit resolved — nothing to verify (pass)."
    exit 0
fi

if [ "$(git rev-parse "$BASE")" = "$(git rev-parse "$HEAD")" ]; then
    echo "DCO check: empty range (base == head) — nothing to verify (pass)."
    exit 0
fi

ZERO=0000000000000000000000000000000000000000
if [ "$BASE" = "$ZERO" ]; then
    echo "DCO check: zero base (initial push) — nothing to verify (pass)."
    exit 0
fi

violations=0
checked=0
skipped_merge=0
skipped_webflow=0

# Read "%H%x00%P%x00%an%x00%ae%x00%cn%x00%ce" per commit in the range.
while IFS=$'\t' read -r sha parents aname aemail cname cemail; do
    nparents=$(awk '{print NF}' <<< "$parents")
    if [ "$nparents" -gt 1 ]; then
        skipped_merge=$((skipped_merge + 1))
        continue
    fi
    if [ "$cname" = "GitHub" ] && [ "$cemail" = "noreply@github.com" ]; then
        skipped_webflow=$((skipped_webflow + 1))
        continue
    fi
    checked=$((checked + 1))
    msg=$(git log -1 --format=%B "$sha")
    signoff=$(printf '%s\n' "$msg" | grep -E "^Signed-off-by: " | tail -n 1 || true)
    if [ -z "$signoff" ]; then
        echo "MISSING sign-off: $sha $aname <$aemail> — $(git log -1 --format=%s "$sha")"
        violations=$((violations + 1))
        continue
    fi
    sname="${signoff#Signed-off-by: }"
    sname="${sname% <*}"
    semail=""
    case "$signoff" in
        *'<'*'>'*) semail="${signoff##*<}"; semail="${semail%>*}" ;;
    esac
    if [ "$sname" != "$aname" ] || [ "$semail" != "$aemail" ]; then
        echo "MISMATCHED sign-off: $sha author=$aname <$aemail> signoff=$sname <$semail>"
        violations=$((violations + 1))
    fi
done < <(git log --format='%H%x09%P%x09%an%x09%ae%x09%cn%x09%ce' "$BASE..$HEAD")

echo "DCO check: $BASE..$HEAD — $checked commit(s) verified, $skipped_merge merge(s) skipped, $skipped_webflow web-flow commit(s) skipped."

if [ "$violations" -gt 0 ]; then
    echo "DCO check: FAILED — $violations commit(s) violate the DCO 1.1 policy." >&2
    echo "Fix: rebase and re-commit with 'git commit --amend --no-edit -s' (or 'git rebase --exec \"git commit --amend --no-edit -s\" $BASE')." >&2
    exit 1
fi

echo "DCO check: OK."
