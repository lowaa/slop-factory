# shellcheck shell=bash
#
# Helpers the runners share (go.sh, ruff.sh, shell.sh). Sourced, not run.

# normalise SED_EXPR: rewrite each line of a tool's output with the pattern,
# failing if a line does not match it, so a change in the tool's format
# can't drop findings silently.
normalise() {
  local in out
  in="$(grep -v '^[[:space:]]*$' || true)"
  [[ -n "${in}" ]] || return 0
  out="$(sed -nE "$1p" <<< "${in}")"
  if [[ "$(grep -c . <<< "${out}" || true)" != "$(grep -c . <<< "${in}")" ]]; then
    echo "${0##*/}: unrecognised tool output:" >&2
    printf '%s\n' "${in}" >&2
    return 1
  fi
  printf '%s\n' "${out}"
}

# install_dir TMP DIR: move the freshly built TMP to DIR unless another run
# got there first, whose copy is the same and is kept. GNU `mv -T` refuses to
# move into an existing DIR, but BSD mv has no -T and nests TMP inside it, so
# that nested copy is removed after. Never fails: callers check for DIR/.built.
install_dir() {
  # Without its trailing slash, or the basename below is empty and DIR goes.
  local tmp="${1%/}"
  mv "${tmp}" "$2" 2> /dev/null || rm -rf "${tmp}"
  rm -rf "${2:?}/${tmp##*/}"
}

# lint_files DIR: the files under DIR a linter should see, relative to it:
# tracked, and untracked but not ignored (so work not yet added is linted
# too), less what .gitattributes marks linguist-generated (vendored copies,
# golden files), which is not ours to fix. Deleted files are not listed, nor
# symbolic links, whose target is linted as itself.
lint_files() {
  local f _ value
  (
    cd "$1" || exit 1
    git ls-files -z --cached --others --exclude-standard |
      git check-attr -z --stdin linguist-generated |
      while IFS= read -r -d '' f && IFS= read -r -d '' _ && IFS= read -r -d '' value; do
        [[ "${value}" == true || ! -f "${f}" || -L "${f}" ]] || printf '%s\n' "${f}"
      done
  ) | sort -u
}
