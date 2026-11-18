from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

# A nested workspace keeps its clones this many folders below its root. Every scanner uses it: one that
# looked less deep would call a slot empty that another sees work in.
CLONE_DEPTH = 3


@dataclass(frozen=True)
class Clones:
    paths: list[Path] = field(default_factory=list[Path])
    # False when the walk met something it should not follow or could not read, so `paths` may be short.
    trusted: bool = True


def find_clones(root: Path, depth: int = CLONE_DEPTH) -> Clones:
    """The git clones below `root` (not `root` itself), at most `depth` folders down, sorted by path.

    Hidden folders are the harness's or ours and are not looked in. A symlinked folder or `.git` is never
    followed, since it may point outside the workspace where it is not ours to judge; it is left out and
    makes the result untrusted, for a caller that must not act on a partial answer. A clone is not looked
    inside for more.
    """
    found: list[Path] = []
    trusted = _collect(root, depth, found)
    return Clones(found, trusted)


def _collect(directory: Path, depth: int, found: list[Path]) -> bool:
    trusted = True
    try:
        children = sorted(directory.iterdir())
    except OSError:
        return False
    for child in children:
        if child.name.startswith("."):
            continue
        try:
            if child.is_symlink():
                trusted = False
                continue
            if not child.is_dir():
                continue
            git = child / ".git"
            if git.is_symlink():
                trusted = False
            elif git.exists():
                found.append(child)
            elif depth > 1:
                trusted = _collect(child, depth - 1, found) and trusted
        except OSError:
            trusted = False
    return trusted
