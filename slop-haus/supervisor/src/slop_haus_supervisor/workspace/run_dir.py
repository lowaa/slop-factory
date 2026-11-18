"""Where a ticket's run starts."""

from __future__ import annotations

from pathlib import Path, PurePosixPath


def run_directory(workspace: Path, paths: tuple[str, ...]) -> Path:
    """The repo the ticket is in, or the workspace itself.

    A nested workspace's root is slop-haus's and its repos are subfolders; a run started inside one gets
    that repo's own CLAUDE.md, skills and relative commands, plus the root's AGENTS.md as a parent.
    A ticket is in exactly one repo, so its first path decides. The repo is the deepest checkout (a `.git`)
    the path runs through below the root; none means the root.
    """
    if not paths:
        return workspace
    parts = PurePosixPath(paths[0]).parts
    if any(p in ("..", "/") for p in parts):
        return workspace
    found = workspace
    current = workspace
    for part in parts:
        current = current / part
        if current.is_symlink() or not current.is_dir():
            break
        if (current / ".git").exists():
            found = current
    return found
