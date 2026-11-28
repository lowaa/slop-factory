#!/usr/bin/env bash
# Installs agy (Google's Antigravity CLI) into the tier 2 image as
# /usr/local/bin/agy. agy isn't on npm: its installer
# (https://antigravity.google/cli/install.sh) reads a release manifest, then
# downloads a tarball from Google's public bucket and checks its SHA-512.
# This does the same at a pinned release, without the installer's shell
# profile edits, and needs no sign-in.
#
#   install_agy.sh <version> <release> <sha512-amd64> <sha512-arm64>
#   install_agy.sh latest                 the manifest's current release
set -euo pipefail

MANIFESTS="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests"
BUCKET="https://storage.googleapis.com/antigravity-public/antigravity-cli"

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 dir=linux-x64 file=cli_linux_x64.tar.gz ;;
  aarch64 | arm64) arch=arm64 dir=linux-arm file=cli_linux_arm64.tar.gz ;;
  *)
    echo "install_agy.sh: unsupported architecture $(uname -m)" >&2
    exit 1
    ;;
esac

if [[ "${1:-}" == "latest" ]]; then
  manifest="$(curl -fsSL "${MANIFESTS}/linux_${arch}.json")"
  url="$(node -e 'const m = JSON.parse(process.argv[1]); console.log(m.url)' "${manifest}")"
  sha512="$(node -e 'const m = JSON.parse(process.argv[1]); console.log(m.sha512)' "${manifest}")"
else
  if (($# != 4)); then
    echo "usage: install_agy.sh <version> <release> <sha512-amd64> <sha512-arm64> | latest" >&2
    exit 2
  fi
  release="$2"
  if [[ "${release}" != "$1"-* ]]; then
    echo "install_agy.sh: release ${release} is not version $1's" >&2
    exit 1
  fi
  url="${BUCKET}/${release}/${dir}/${file}"
  if [[ "${arch}" == amd64 ]]; then sha512="$3"; else sha512="$4"; fi
fi

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
curl -fsSL -o "${work}/agy.tar.gz" "${url}"
echo "${sha512}  ${work}/agy.tar.gz" | sha512sum -c -
tar -xzf "${work}/agy.tar.gz" -C "${work}" antigravity
install -m 0755 "${work}/antigravity" /usr/local/bin/agy
