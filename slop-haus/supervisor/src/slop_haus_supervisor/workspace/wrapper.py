from __future__ import annotations

import os
import shlex
import tempfile
from collections.abc import Mapping
from pathlib import Path

MARKER = "# slop-haus command: written by `slop-haus-supervisor init` and `update`"

# One wrapper serves every tenant on the host: it finds an installed app to run, and the
# supervisor itself then chooses the tenant's root (root.find_root). `uv` is looked for where
# the unit samples in service/ look, since a login shell's PATH is not always there.
_TEMPLATE = """\
#!/usr/bin/env bash
{marker}
set -euo pipefail
export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$PATH"
if ! command -v uv > /dev/null 2>&1; then
  echo "slop-haus: uv was not found; install it from https://docs.astral.sh/uv/" >&2
  exit 1
fi

app=""
dir=${{SLOP_HAUS_HOME:-}}
if [ -n "$dir" ] && [ -d "$dir/app" ]; then
  app=$dir/app
fi
# Only a root under ~/slop-haus is taken from the current directory: any parent holding a
# supervisor.toml and an app/ would let a checkout's files choose the code that runs.
if [ -z "$app" ]; then
  case "$PWD/" in
    "$HOME"/slop-haus/*/*)
      here=${{PWD#"$HOME"/slop-haus/}}
      if [ -d "$HOME/slop-haus/${{here%%/*}}/app" ]; then
        app=$HOME/slop-haus/${{here%%/*}}/app
      fi
      ;;
  esac
fi
if [ -z "$app" ] && [ -d {anchor} ]; then
  app={anchor}
fi
if [ -z "$app" ]; then
  for candidate in "$HOME"/slop-haus/*/app; do
    if [ -d "$candidate" ]; then
      app=$candidate
      break
    fi
  done
fi
if [ -z "$app" ]; then
  echo "slop-haus: no supervisor is set up on this host; run the setup script first" >&2
  exit 1
fi
exec uv run --frozen --no-dev --project "$app" slop-haus-supervisor "$@"
"""


def render(anchor_app: Path) -> str:
    return _TEMPLATE.format(marker=MARKER, anchor=shlex.quote(str(anchor_app)))


def _on_path(directory: Path, env: Mapping[str, str]) -> bool:
    return str(directory) in env.get("PATH", "").split(os.pathsep)


def install(home: Path, root: Path, env: Mapping[str, str]) -> list[str]:
    """Write the `slop-haus` command, returning what to tell the person.

    ~/.local/bin/slop-haus, unless a file of someone else's is there; then <root>/bin/slop-haus.
    """
    script = render(root / "app")
    target = home / ".local" / "bin" / "slop-haus"
    if target.exists() and MARKER not in target.read_text(errors="replace"):
        target = root / "bin" / "slop-haus"
    target.parent.mkdir(parents=True, exist_ok=True)
    # Renamed into place, so an interrupted write never leaves a truncated command.
    fd, tmp = tempfile.mkstemp(dir=target.parent, prefix=".slop-haus-")
    with os.fdopen(fd, "w") as out:
        out.write(script)
    os.chmod(tmp, 0o755)
    os.replace(tmp, target)
    notes = [f"installed the slop-haus command: {target}"]
    if not _on_path(target.parent, env):
        notes.append(f"{target.parent} is not on your PATH; add it with:\n\n  export PATH=\"{target.parent}:$PATH\"")
    return notes
