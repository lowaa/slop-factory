"""The managed asset bundle a workspace installed, as setup and update recorded it."""

from __future__ import annotations

import re
from pathlib import Path

# Where the server's setup and update write the hash (workspace.BundleHashFile, checked by contracts/paths.json).
BUNDLE_HASH_FILE = ".slop-haus/bundle-hash"
# The server drops anything longer or not hex; reading no more than this keeps a damaged file cheap.
MAX_BUNDLE_HASH = 64
_HEX = re.compile(r"[0-9a-f]+")


def read_bundle_hash(workspace: Path) -> str:
    """The hash recorded in the workspace, or "" when there is none or the file is not a hash."""
    try:
        with open(workspace / BUNDLE_HASH_FILE, encoding="ascii") as handle:
            text = handle.read(MAX_BUNDLE_HASH + 1).strip()
    except (OSError, UnicodeDecodeError):
        return ""
    return text if len(text) <= MAX_BUNDLE_HASH and _HEX.fullmatch(text) else ""
