# shellcheck shell=bash
#
# Sourced first by the host-side scripts that use bash 4 and 5 features
# (mapfile, declare -A, wait -n, shopt inherit_errexit, EPOCHREALTIME).
# macOS ships bash 3.2 as /bin/bash, and without this they die halfway with
# a syntax or "invalid shell option" error that doesn't say why. Kept free
# of anything 3.2 can't parse, and exits rather than returns: it is sourced.
if ((BASH_VERSINFO[0] < 5)); then
  echo "${0##*/}: needs bash >= 5 (this is ${BASH_VERSION}); on macOS \`brew install bash\` and make sure it is first on PATH" >&2
  exit 1
fi
