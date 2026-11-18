#!/usr/bin/env bash
#
# The one ratchet every linter here feeds: compares a tool's findings with a
# committed baseline, so existing debt can be paid down but never grown.
#
#   <findings> | ratchet.sh [--allowlist FILE] [--regen CMD] BASELINE
#   <findings> | ratchet.sh --prune      BASELINE   drop fixed entries only
#   <findings> | ratchet.sh --rebaseline BASELINE   rewrite from the findings
#
# A finding is one line naming what is wrong and where, without line numbers,
# so edits elsewhere in a file don't churn the baseline: "<file> <rule>", or
# "<file> <rule> <symbol>" where the tool names a stable symbol. The same line
# may occur more than once: the baseline is a multiset, so it allows exactly
# as many of a finding as it lists.
#
# The check fails both ways:
#   - a finding the baseline (and allow-list) doesn't cover is new debt;
#   - an entry no finding matches is stale: the debt was paid and the
#     baseline must shrink with it, or the same debt could creep back in.
#     It also means a tool that silently found nothing cannot pass.
#
# The allow-list, when given, holds "<finding> #<ticket>" lines: a finding
# that is deliberate for now and whose ticket removes it. Its entries go
# stale like the baseline's.
#
# --prune never adds an entry, so it is the one to run after fixing debt.
# --rebaseline accepts every current finding as debt; it is for a tool
# upgrade or a rule change, and the commit that runs it says why.
#
# Blank lines and lines starting with '#' in the baseline are ignored; a
# leading block of them is kept when the file is rewritten.
set -euo pipefail
export LC_ALL=C
shopt -s inherit_errexit

usage() {
  echo "usage: ratchet.sh [--prune|--rebaseline] [--allowlist FILE] [--regen CMD] BASELINE < findings" >&2
  exit 2
}

mode=check allowlist="" regen=""
while (($#)); do
  case "$1" in
    --prune) mode=prune ;;
    --rebaseline) mode=rebaseline ;;
    --allowlist)
      allowlist="${2:?}"
      shift
      ;;
    --regen)
      regen="${2:?}"
      shift
      ;;
    --*) usage ;;
    *) break ;;
  esac
  shift
done
(($# == 1)) || usage
baseline="$1"

entries() { # entries FILE: the file's findings, sorted, or nothing if it is absent
  [[ -f "$1" ]] || return 0
  grep -v -e '^#' -e '^[[:space:]]*$' "$1" | sed -E 's/[[:space:]]+$//' | sort || true
}

allowed() { # allowed FILE: the allow-list's findings, or fail on a line without a ticket
  [[ -n "$1" && -f "$1" ]] || return 0
  local line
  while IFS= read -r line; do
    [[ -z "${line// /}" || "${line}" == \#* ]] && continue
    if [[ ! "${line}" =~ ^(.*[^ ])\ +#[0-9]+$ ]]; then
      echo "$1: '${line}' must read '<finding> #<ticket>'" >&2
      return 1
    fi
    echo "${BASH_REMATCH[1]}"
  done < "$1"
}

write_baseline() { # write_baseline CONTENT: replace the baseline, keeping its header
  local tmp header=""
  # No debt and no baseline yet: an absent baseline already reads as empty.
  [[ -n "$1" || -f "${baseline}" ]] || return 0
  [[ -f "${baseline}" ]] && header="$(awk '/^#|^[[:space:]]*$/ { print; next } { exit }' "${baseline}")"
  mkdir -p "$(dirname "${baseline}")"
  # Renamed into place, so an interrupted run leaves the old baseline.
  tmp="$(mktemp "${baseline}.XXXXXX")"
  {
    [[ -z "${header}" ]] || printf '%s\n' "${header}"
    [[ -z "$1" ]] || printf '%s\n' "$1"
  } > "${tmp}"
  mv "${tmp}" "${baseline}"
}

# A finding must be one line; trailing whitespace would make an entry that
# reads the same never match.
found="$(sed -E 's/[[:space:]]+$//' | grep . | sort || true)"
allow="$(allowed "${allowlist}" | sort)"
known="$({
  entries "${baseline}"
  printf '%s\n' "${allow}"
} | grep . | sort || true)"
# comm on sorted input with repeated lines treats them as a multiset.
new="$(comm -23 <(printf '%s\n' "${found}") <(printf '%s\n' "${known}") | grep . || true)"
stale="$(comm -13 <(printf '%s\n' "${found}") <(printf '%s\n' "${known}") | grep . || true)"

case "${mode}" in
  rebaseline)
    write_baseline "$(comm -23 <(printf '%s\n' "${found}") <(printf '%s\n' "${allow}") | grep . || true)"
    echo "${baseline}: $(entries "${baseline}" | wc -l | tr -d ' ') entries"
    exit 0
    ;;
  prune)
    stale_base="$(comm -13 <(printf '%s\n' "${found}") <(entries "${baseline}") | grep . || true)"
    write_baseline "$(comm -23 <(entries "${baseline}") <(printf '%s\n' "${stale_base}") | grep . || true)"
    echo "${baseline}: removed $(grep -c . <<< "${stale_base}" || true) fixed entries"
    # Pruning never accepts new debt, so it still fails on it.
    [[ -z "${new}" ]] || {
      echo "and these new findings remain:" >&2
      sed 's/^/  /' <<< "${new}" >&2
      exit 1
    }
    exit 0
    ;;
esac

status=0
if [[ -n "${new}" ]]; then
  {
    echo "New findings, not in ${baseline}:"
    sed 's/^/  /' <<< "${new}"
    echo "Fix them, or suppress one with an inline directive that gives the reason."
    echo "The baseline only shrinks: see dev-tools/lint/README.md."
  } >&2
  status=1
fi
if [[ -n "${stale}" ]]; then
  {
    echo "In ${baseline}${allowlist:+ or ${allowlist}} but no longer found:"
    sed 's/^/  /' <<< "${stale}"
    echo "The debt was paid: delete these lines${regen:+, or run: ${regen}}."
  } >&2
  status=1
fi
exit "${status}"
