"""What a slot's workspace looks like on disk, in the shape the server's Workspace page takes.

The remote-control host's LayoutReporter sends this for a slot's conversation, and the supervisor
reports it from the slot root. Keep the credential stripping in step with strip_userinfo in
server/internal/workspace/assets/py/describe-session.py and StripUserinfo on the server.
"""

from __future__ import annotations

import os
import subprocess
import time
from pathlib import Path
from typing import Any

from slop_haus_supervisor.workspace.repos import find_clones

# The server's wire limits (worker.WorkerLayoutEntry): a report past them is refused whole, which
# would also lose the slot-state report it rides on, so a layout that cannot fit is not sent.
MAX_REPOS = 50
MAX_ORIGIN = 500
BUDGET_SECONDS = 4.0


class _Unavailable(Exception):
    """git could not answer, so the layout cannot be told truthfully."""


def strip_userinfo(remote: str) -> str:
    remote = remote.strip()
    # Everything up to the last "@": a password may hold an unescaped "/", "?" or "#".
    if "@" in remote:
        remote = remote[remote.rfind("@") + 1 :]
    for mark in ("?", "#"):
        remote = remote.split(mark, 1)[0]
    scheme, sep, rest = remote.partition("://")
    if sep and scheme.replace("+", "").replace("-", "").replace(".", "").isalnum():
        remote = rest
    return remote


def _origin(directory: Path, deadline: float) -> str:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise _Unavailable("out of time")
    try:
        result = subprocess.run(
            ["git", "-C", str(directory), "config", "--local", "--get", "remote.origin.url"],
            capture_output=True, timeout=remaining, check=False,
        )  # fmt: skip
    except (OSError, subprocess.SubprocessError) as err:
        raise _Unavailable(str(err)) from err
    if result.returncode == 1:
        return ""
    if result.returncode != 0:
        # git failing (dubious ownership, say) is not an absent origin, and saying "none" would be a false report.
        raise _Unavailable(f"git exited {result.returncode}")
    origin = strip_userinfo(result.stdout.decode("utf-8", "replace"))
    if len(origin) > MAX_ORIGIN:
        raise _Unavailable("origin too long")
    return origin


def _is_git(directory: Path) -> bool:
    # A file, not a directory, in a worktree or a submodule.
    return os.path.lexists(directory / ".git")


def describe_layout(root: Path) -> dict[str, Any] | None:
    """The root and the git repos below it, or None when git could not say: no report beats a wrong one."""
    deadline = time.monotonic() + BUDGET_SECONDS
    try:
        root_git = _is_git(root)
        layout: dict[str, Any] = {"root": {"path": ".", "git": root_git, "origin": _origin(root, deadline) if root_git else ""}}
        try:
            os.listdir(root)
        except OSError as err:
            # An unreadable root is not an empty one; the server keeps its last report instead.
            raise _Unavailable(str(err)) from err
        clones = find_clones(root).paths[:MAX_REPOS]
        repos = [{"path": clone.relative_to(root).as_posix(), "git": True, "origin": _origin(clone, deadline)} for clone in clones]
        layout["repos"] = repos
        return layout
    except _Unavailable:
        return None
