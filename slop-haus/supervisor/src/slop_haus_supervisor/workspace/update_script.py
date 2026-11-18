from __future__ import annotations

import json
from typing import cast

REPOS_HEADER = "Slop-Haus-Repos"


class UpdateScript(str):
    """The server's update script, with the manifest's `(path, remote)` repos it came with.

    A `str` so everything that only runs the script keeps working; `repos` is None when the server
    is older than the header; a script that syncs repos is then refused.
    """

    repos: tuple[tuple[str, str], ...] | None

    def __new__(cls, text: str, repos: tuple[tuple[str, str], ...] | None = None) -> UpdateScript:
        made = super().__new__(cls, text)
        made.repos = repos
        return made


def parse_repos_header(value: str | None) -> tuple[tuple[str, str], ...] | None:
    """The repos the server listed; None when it sent none or sent something unreadable."""
    if value is None:
        return None
    try:
        listed = json.loads(value)
    except ValueError:
        return None
    if not isinstance(listed, list):
        return None
    repos: list[tuple[str, str]] = []
    for entry in cast(list[object], listed):
        fields = cast(dict[str, object], entry) if isinstance(entry, dict) else {}
        path, remote = fields.get("path"), fields.get("remote")
        if not isinstance(path, str) or not isinstance(remote, str):
            return None
        repos.append((path, remote))
    return tuple(repos)
