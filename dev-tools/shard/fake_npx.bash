# shellcheck shell=bash
# fake_npx <dir> -- puts an npx first on PATH that appends each run's
# arguments to <dir>/npx_calls, one run a line, each argument %q-quoted so
# that where one ends is unambiguous (after "<NPX_TAG>:", when that is
# set), and exits with FAIL_<project>
# (default 0) for the first --project it was given.
fake_npx() {
  local dir="$1"
  mkdir -p "${dir}/bin"
  cat > "${dir}/bin/npx" << 'EOF_NPX'
#!/usr/bin/env bash
dir="$(dirname "$0")/.."
line=""
for arg; do
  line+="${line:+ }$(printf '%q' "${arg}")"
done
echo "${NPX_TAG:+${NPX_TAG}:}${line}" >> "${dir}/npx_calls"
for arg; do
  if [[ "${arg}" == --project=* ]]; then
    fail="FAIL_${arg#--project=}"
    [[ "${fail}" =~ ^[A-Za-z0-9_]+$ ]] || exit 0
    exit "${!fail:-0}"
  fi
done
EOF_NPX
  chmod +x "${dir}/bin/npx"
  PATH="${dir}/bin:${PATH}"
}
