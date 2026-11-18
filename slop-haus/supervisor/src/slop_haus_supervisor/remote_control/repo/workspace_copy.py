from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, Collection, cast

from slop_haus_supervisor.remote_control.repo.working_copy import (
    MAX_REF_CHARS,
    MAX_REPO_COMMITS,
    MAX_REPO_PATHS,
    MAX_REPO_PATH_BYTES,
    MAX_REPO_PUSHES,
    WorkingCopy,
)
from slop_haus_supervisor.text import truncate_bytes
from slop_haus_supervisor.workspace.repos import find_clones


class WorkspaceCopy:
    """The workspace's repos, read as one working copy.

    A workspace whose root is a repo is that repo, and reports exactly as WorkingCopy does. A nested
    workspace's root is a plain directory with checkouts below it, so each is read on its own and the
    board gets one report: changed paths are relative to the workspace root (as a ticket's paths are),
    counts are summed, and the branch, head and upstream are those of the repo the conversation has
    most to show in (the first, when none has).
    """

    def __init__(self, root: str, timeout: float = 120.0) -> None:
        self.root = root
        self.timeout = timeout

    def repos(self) -> dict[str, WorkingCopy]:
        """Repo folders by their path from the root ("" for the root itself). Read afresh: a repo can be cloned mid-conversation."""
        if os.path.exists(os.path.join(self.root, ".git")):
            return {"": WorkingCopy(self.root, self.timeout)}
        found = {rel: WorkingCopy(os.path.join(self.root, rel), self.timeout) for rel in self._checkouts()}
        # Nothing to read below: the root's own git failure is the error worth reporting.
        return found or {"": WorkingCopy(self.root, self.timeout)}

    def _checkouts(self) -> list[str]:
        return [str(path.relative_to(self.root)) for path in find_clones(Path(self.root)).paths]

    def base_commit(self) -> str:
        """The root repo's commit, as WorkingCopy gives it; a nested workspace's, a JSON object of each repo's."""
        repos = self.repos()
        if list(repos) == [""]:
            return repos[""].base_commit()
        return json.dumps({rel: copy.base_commit() for rel, copy in repos.items()}, sort_keys=True)

    @staticmethod
    def _bases(base: str, repos: Collection[str]) -> dict[str, str]:
        if list(repos) == [""]:
            return {"": base}
        try:
            parsed: object = json.loads(base)
        except ValueError:
            return {}
        if not isinstance(parsed, dict):
            return {}
        return {k: v for k, v in cast(dict[object, object], parsed).items() if isinstance(k, str) and isinstance(v, str)}

    def snapshot(self) -> dict[str, Any]:
        repos = self.repos()
        if list(repos) == [""]:
            return repos[""].snapshot()
        return {"repos": {rel: copy.snapshot() for rel, copy in repos.items()}}

    @staticmethod
    def _before(before: dict[str, Any] | None, rel: str) -> dict[str, Any] | None:
        if before is None:
            return None
        if "repos" not in before:
            return before if rel == "" else None
        sub: object = before["repos"].get(rel)
        return cast(dict[str, Any], sub) if isinstance(sub, dict) else None

    @staticmethod
    def _own(paths: Collection[str], rel: str) -> list[str]:
        if rel == "":
            return list(paths)
        return [p[len(rel) + 1:] for p in paths if p.startswith(rel + "/")]

    @staticmethod
    def _prefixed(paths: Collection[str], rel: str) -> list[str]:
        return list(paths) if rel == "" else [f"{rel}/{p}" for p in paths]

    def pushed_paths(self, base: str, before: dict[str, Any]) -> list[str]:
        repos = self.repos()
        bases = self._bases(base, repos)
        out: list[str] = []
        for rel, copy in repos.items():
            sub = self._before(before, rel)
            if sub is not None:
                out += self._prefixed(copy.pushed_paths(bases.get(rel, ""), sub), rel)
        return out

    def repo_state(self, base: str, before: dict[str, Any] | None, pushed_paths: Collection[str] = ()) -> dict[str, Any]:
        repos = self.repos()
        bases = self._bases(base, repos)
        states = {
            rel: copy.repo_state(bases.get(rel, ""), self._before(before, rel), self._own(pushed_paths, rel))
            for rel, copy in repos.items()
        }
        if list(states) == [""]:
            return states[""]
        return self._merged(states)

    @staticmethod
    def _merged(states: dict[str, dict[str, Any]]) -> dict[str, Any]:
        def has_work(s: dict[str, Any]) -> bool:
            return bool(s["dirty"] or s["commits"] or s["pushes"] or s["changed_total"])

        lead = next((s for s in states.values() if has_work(s)), next(iter(states.values())))
        changed = sorted(
            truncate_bytes(f"{rel}/{p}", MAX_REPO_PATH_BYTES) for rel, s in states.items() for p in s["changed_paths"]
        )
        return {
            "branch": lead["branch"][:MAX_REF_CHARS], "head": lead["head"], "upstream": lead["upstream"],
            "dirty": any(s["dirty"] for s in states.values()),
            "dirty_count": sum(s["dirty_count"] for s in states.values()),
            "ahead": sum(s["ahead"] for s in states.values()),
            "behind": sum(s["behind"] for s in states.values()),
            "commits": [c for s in states.values() for c in s["commits"]][:MAX_REPO_COMMITS],
            "pushes": [p for s in states.values() for p in s["pushes"]][:MAX_REPO_PUSHES],
            "changed_paths": changed[:MAX_REPO_PATHS],
            "changed_total": sum(s["changed_total"] for s in states.values()),
        }
