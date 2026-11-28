# shellcheck shell=bash

# The real repo: every project quality.sh can measure has one, but for the
# ones pending.txt lists, and that list only shrinks.
Describe 'every project'
  projects() { # the registry's project directories, each once
    awk '$1 !~ /^#/ && NF == 2 { print $2 }' "${QUALITY_REAL_ROOT}/dev-tools/lint/projects.txt" | sort -u
  }
  entries() { # entries FILE: a pending list's entries, sorted
    grep -v -e '^#' -e '^[[:space:]]*$' "$1" | sort
  }
  pending() { entries "${QUALITY_REAL_ROOT}/dev-tools/quality/pending.txt"; }

  without_quality_sh() { # the projects that have none, and aren't pending
    comm -23 <(projects) <(pending) | while read -r p; do
      [[ -x "${QUALITY_REAL_ROOT}/${p}/quality.sh" ]] || echo "${p}"
    done
  }
  pending_with_quality_sh() {
    pending | while read -r p; do
      [[ ! -e "${QUALITY_REAL_ROOT}/${p}/quality.sh" ]] || echo "${p}"
    done
  }
  not_using_the_lib() {
    comm -23 <(projects) <(pending) | while read -r p; do
      grep -qs 'dev-tools/quality/quality_lib.sh' "${QUALITY_REAL_ROOT}/${p}/quality.sh" || echo "${p}"
    done
  }

  It 'has a quality.sh, unless pending.txt lists it'
    When call without_quality_sh
    The output should eq ''
  End

  It 'runs quality.sh through quality_lib.sh'
    When call not_using_the_lib
    The output should eq ''
  End

  It 'leaves pending.txt once it has a quality.sh'
    When call pending_with_quality_sh
    The output should eq ''
  End

  It 'lists in pending.txt only registered projects, each once'
    stale() { comm -23 <(pending) <(projects); }
    duplicates() { pending | uniq -d; }
    When call stale
    The output should eq ''
    The result of function duplicates should eq ''
  End

  # The base the branch is compared with: QUALITY_BASE, or the merge-base
  # with main or origin/main, whichever is nearer (as lint.sh --changed).
  merge_base() {
    if [[ -n "${QUALITY_BASE:-}" ]]; then
      git -C "${QUALITY_REAL_ROOT}" rev-parse "${QUALITY_BASE}"
      return
    fi
    local ref mb best=""
    for ref in main origin/main; do
      git -C "${QUALITY_REAL_ROOT}" rev-parse -q --verify "${ref}^{commit}" > /dev/null || continue
      mb="$(git -C "${QUALITY_REAL_ROOT}" merge-base HEAD "${ref}")" || continue
      if [[ -z "${best}" ]] || git -C "${QUALITY_REAL_ROOT}" merge-base --is-ancestor "${best}" "${mb}"; then
        best="${mb}"
      fi
    done
    [[ -n "${best}" ]] && echo "${best}"
  }
  no_base() { ! merge_base > /dev/null 2>&1 && [[ "${QUALITY_REQUIRE_BASE:-}" != 1 ]]; }
  added_to_pending() { # entries the branch added; a base without the file has none
    local base was
    base="$(merge_base)" || {
      echo "no base to compare pending.txt with: set QUALITY_BASE, or fetch main"
      return
    }
    was="$(git -C "${QUALITY_REAL_ROOT}" show "${base}:dev-tools/quality/pending.txt" 2> /dev/null)" || return 0
    comm -13 <(entries /dev/stdin <<< "${was}") <(pending)
  }

  # CI fetches main for this (QUALITY_REQUIRE_BASE=1), so only a local
  # checkout without one skips it.
  It 'never adds to pending.txt: a new project gets its quality.sh'
    Skip if 'no main or origin/main to compare with' no_base
    When call added_to_pending
    The output should eq ''
  End
End
