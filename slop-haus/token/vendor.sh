#!/usr/bin/env bash
# Copies slop_haus_token.py, the source of truth, to the two places that ship it: the server's
# embedded assets (go:embed can't reach outside the server module) and the supervisor package.
# Run it after every edit to the source; the byte-identity tests in server/test.sh and
# supervisor/test.sh fail until you do.
set -euo pipefail
cd "$(dirname "$0")/.."

src=token/slop_haus_token.py
for dest in server/internal/workspace/assets/py supervisor/src/slop_haus_supervisor/slophaus; do
  cp "$src" "$dest/slop_haus_token.py"
  # Only this file is generated; its neighbours are hand-written and must stay visible in review.
  printf '# Copied from token/slop_haus_token.py by token/vendor.sh.\nslop_haus_token.py linguist-generated=true\n' > "$dest/.gitattributes"
done
