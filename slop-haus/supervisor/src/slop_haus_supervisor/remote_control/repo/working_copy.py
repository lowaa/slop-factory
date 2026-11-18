from __future__ import annotations

import os
import re
import subprocess
from dataclasses import dataclass, field
from typing import Any, Collection

from slop_haus_supervisor.remote_control.wire.errors import Refused
from slop_haus_supervisor.text import clean_field, clean_text, truncate_bytes

COMMIT = re.compile(r"^[0-9a-f]{7,64}$")
# Limits on a repo state report, inside conductor.go's.
MAX_REPO_PATHS = 200
MAX_REPO_PATH_BYTES = 200
MAX_REPO_COMMITS = 20
MAX_REPO_PUSHES = 20
MAX_SUBJECT_BYTES = 200
MAX_REF_CHARS = 255

# What slop-haus's setup puts in the working copy of its own: the briefing
# import (the same line in every checkout, and normally committed) and the
# MCP config. Neither counts as the owner's uncommitted work.
CLAUDE_MD = "CLAUDE.md"
IMPORT_LINES = {"", "@.slop-haus/briefing.md", "# Agent instructions"}
MCP_CONFIG = ".mcp.json"

# Environment that would send a git command to some other repository.
GIT_REDIRECTS = (
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_INDEX_FILE",
    "GIT_OBJECT_DIRECTORY",
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_COMMON_DIR",
    "GIT_NAMESPACE",
)


@dataclass
class RemoteUpdates:
    pushes: list[dict[str, str]] = field(default_factory=lambda: [])
    # Tips that remote-tracking branches took by anything but a push.
    fetched: set[str] = field(default_factory=lambda: set())

    def fetched_tips(self, old: dict[str, str]) -> list[str]:
        """Every commit that was on a remote at the turn's start or came by a
        fetch since: none of it is the session's."""
        return sorted(sha for sha in set(old.values()) | self.fetched if COMMIT.match(sha))


class WorkingCopy:
    """The workspace's git working copy, which the host only ever reads: it
    never moves it, so there is nothing here that could discard work."""

    def __init__(self, root: str, timeout: float = 120.0) -> None:
        self.root = root
        self.timeout = timeout

    def git(self, *args: str, check: bool = True, code: bool = False) -> Any:
        env = {k: v for k, v in os.environ.items() if k not in GIT_REDIRECTS}
        env.update(GIT_TERMINAL_PROMPT="0", GIT_OPTIONAL_LOCKS="0")
        # No hooks, and none of the config keys that name a program to run, so
        # the host's own reads cannot run one the model or a checkout of
        # someone else's put in .git/config. The repository's config is
        # otherwise the owner's, like the rest of the working copy.
        command = ["git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
                   "-c", "protocol.ext.allow=never", "-c", "remote.origin.uploadpack=git-upload-pack",
                   "-C", self.root] + list(args)
        try:
            out = subprocess.run(command, env=env, capture_output=True, text=True, timeout=self.timeout, errors="replace")
        except subprocess.TimeoutExpired:
            raise Refused("git %s took longer than %.0fs" % (args[0], self.timeout))
        except OSError as e:
            raise Refused("git could not run: %s" % e)
        if code:
            return out.returncode
        if check and out.returncode != 0:
            detail = clean_text((out.stderr or out.stdout).strip(), 400)
            raise Refused("git %s failed: %s" % (" ".join(args[:2]), detail))
        return out.stdout

    def head(self) -> str:
        return self.git("rev-parse", "HEAD").strip()

    def base_commit(self) -> str:
        """The commit changes are counted from; empty when there is none (no
        commit yet), in which case nothing is counted."""
        return self.git("rev-parse", "--verify", "--quiet", "HEAD", check=False).strip()

    def branch(self) -> str:
        return self.git("symbolic-ref", "--quiet", "--short", "HEAD", check=False).strip()

    # -- what the conversation did --

    def snapshot(self) -> dict[str, Any]:
        """The head, the local branches and the remote-tracking branches, before
        a turn."""
        out = self.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/", check=False)
        remotes = dict(line.split(" ", 1) for line in out.splitlines() if " " in line)
        heads = self.git("for-each-ref", "--format=%(objectname)", "refs/heads/", check=False).split()
        return {"head": self.git("rev-parse", "--verify", "--quiet", "HEAD", check=False).strip(), "remotes": remotes, "heads": heads}

    def repo_state(self, base: str, before: dict[str, Any] | None, pushed_paths: Collection[str] = ()) -> dict[str, Any]:
        """The repo as the board shows it after a turn. `before` is the
        snapshot from the turn's start, when there is one; `pushed_paths` is
        what the conversation's earlier pushes carried (see changed_paths)."""
        head = self.git("rev-parse", "--verify", "--quiet", "HEAD", check=False).strip()
        dirty = self.dirty_paths()
        upstream = self.git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}", check=False).strip()
        ahead = behind = 0
        if upstream:
            counts = self.git("rev-list", "--left-right", "--count", "HEAD...@{upstream}", check=False).split()
            if len(counts) == 2:
                ahead, behind = int(counts[0]), int(counts[1])
        else:
            ahead = len(self.git("rev-list", "HEAD", "--not", "--remotes", check=False).split())
        changed = self.changed_paths(base, pushed_paths)
        state: dict[str, Any] = {
            "branch": clean_field(self.branch(), MAX_REF_CHARS), "head": head, "dirty": bool(dirty), "dirty_count": len(dirty),
            "upstream": clean_field(upstream, MAX_REF_CHARS), "ahead": ahead, "behind": behind,
            "commits": [], "pushes": [],
            "changed_paths": [truncate_bytes(p, MAX_REPO_PATH_BYTES) for p in changed[:MAX_REPO_PATHS]],
            "changed_total": len(changed),
        }
        if before is not None:
            state["commits"] = self.commits_since(before)
            state["pushes"] = self.pushes_since(before.get("remotes", {}))
        return state

    def commits_since(self, before: dict[str, Any]) -> list[dict[str, str]]:
        """Commits now on HEAD or any local branch that were on none of them
        when the turn began -- so work left on a branch the turn then switched
        away from is still shown. Commits that came from a remote (a pull, a
        fetch and merge) are not the turn's, whichever way they arrived."""
        known = [sha for sha in [before.get("head", "")] + list(before.get("heads", [])) if COMMIT.match(sha or "")]
        if not known:
            return []
        updates = self.remote_updates(before.get("remotes", {}))
        out = self.git("log", "--format=%H %s", "-n", str(MAX_REPO_COMMITS), "HEAD", "--branches", "--not",
                       *known, *updates.fetched_tips(before.get("remotes", {})), check=False)
        commits: list[dict[str, str]] = []
        for line in out.splitlines():
            sha, _, subject = line.partition(" ")
            commits.append({"sha": sha, "subject": clean_field(subject, MAX_SUBJECT_BYTES)})
        return commits

    def pushes_since(self, old: dict[str, str]) -> list[dict[str, str]]:
        """Remote-tracking branches that moved because this repo pushed: git
        notes 'update by push' in the branch's reflog, where a fetch says
        'fetch'. A push by URL, or with the reflog switched off, is not seen."""
        return self.remote_updates(old).pushes[:MAX_REPO_PUSHES]

    def remote_updates(self, old: dict[str, str]) -> RemoteUpdates:
        """How each remote-tracking branch got from `old` to where it is now."""
        updates = RemoteUpdates()
        for name, sha in sorted(self.snapshot()["remotes"].items()):
            # origin/HEAD is a symbolic ref that follows a branch without a push of its own.
            if old.get(name) == sha or name.endswith("/HEAD"):
                continue
            short = name[len("refs/remotes/"):]
            entries = self.git("reflog", "show", "--format=%H %gs", "-n", "50", name, check=False).splitlines()
            pushed = False
            for entry in entries:
                tip, _, reason = entry.partition(" ")
                if tip == old.get(name):
                    break
                if reason.startswith("update by push"):
                    # A push followed by a fetch in one turn is still a push.
                    pushed = True
                else:
                    updates.fetched.add(tip)
            if pushed:
                updates.pushes.append({"ref": clean_field(short, 255), "from": old.get(name, ""), "to": sha})
            else:
                # No sign of a push: whatever moved it is another machine's work.
                updates.fetched.add(sha)
        return updates

    def pushed_paths(self, base: str, before: dict[str, Any]) -> list[str]:
        """Paths the commits this turn pushed touched, for the conversation to
        keep: once a commit is on a remote it no longer shows as unpushed, but
        it is still the session's work."""
        if not COMMIT.match(base or ""):
            return []
        old = before.get("remotes", {})
        updates = self.remote_updates(old)
        tips = [p["to"] for p in updates.pushes if COMMIT.match(p["to"])]
        if not tips:
            return []
        return self.commit_paths(*tips, "--not", base, *updates.fetched_tips(old))

    def commit_paths(self, *revs: str) -> list[str]:
        """Paths touched by the commits in a rev-list range. For a merge, only
        the paths whose result differs from every parent (--cc): what it
        brought in is another branch's, and what the session wrote is in the
        commits it merged or in its conflict resolution."""
        out = self.git("log", "--cc", "--no-ext-diff", "--no-renames", "--format=", "--name-only", "-z", *revs, "--", check=False)
        return [p for p in out.split("\0") if p]

    def changed_paths(self, base: str, pushed: Collection[str] = ()) -> list[str]:
        """The session's own work, as paths -- never what arrived from a remote.
        The union of:
          - uncommitted changes (tracked, staged or not) and untracked files;
          - paths touched by commits on HEAD that no remote-tracking branch
            holds and that are not `base` or its ancestors: commits made here,
            on a branch with or without an upstream, on a detached HEAD, or
            rewritten by a rebase. A merge of main into the branch adds none,
            for its commits are on a remote and the merge commit is skipped;
          - `pushed`, the paths of what the conversation pushed, which the
            host keeps because a pushed commit is on a remote and so no longer
            unpushed.
        A pull from a URL, which updates no remote-tracking branch, cannot be
        told from local commits and so counts."""
        if not COMMIT.match(base or ""):
            return []
        changed = [p for p in self.git("diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", "HEAD", "--", check=False).split("\0") if p]
        changed += [p for p in self.git("ls-files", "--others", "--exclude-standard", "-z", check=False).split("\0") if p]
        paths: set[str] = set()
        for path in changed:
            if path == MCP_CONFIG or (path == CLAUDE_MD and self.only_the_import_added()):
                continue
            paths.add(path)
        paths.update(p for p in self.commit_paths("HEAD", "--not", "--remotes", base) if p != MCP_CONFIG)
        paths.update(p for p in pushed if p != MCP_CONFIG)
        return sorted(clean_field(p, 4096) for p in paths)

    def dirty_paths(self) -> list[str]:
        out = self.git("status", "--porcelain=v1", "-z", "--untracked-files=normal")
        entries = [e for e in out.split("\0") if e]
        paths: list[str] = []
        skip = False
        for entry in entries:
            if skip:
                skip = False
                continue
            status, path = entry[:2], entry[3:]
            if status[0] in "RC":
                skip = True
            if self.is_setup_file(status, path):
                continue
            paths.append(clean_field(path, 200))
        return paths

    def is_setup_file(self, status: str, path: str) -> bool:
        if path == MCP_CONFIG and status == "??":
            return True
        if path == CLAUDE_MD:
            return self.only_the_import_added()
        return False

    def only_the_import_added(self, since: str = "HEAD") -> bool:
        """Whether CLAUDE.md differs from `since` by nothing but the import. A
        change count passes the conversation's base, so an edit the turn has
        committed is not hidden by the import added after it."""
        if not self.git("ls-files", "--", CLAUDE_MD).strip():
            # Setup creates it when the repo has none.
            try:
                with open(os.path.join(self.root, CLAUDE_MD), encoding="utf-8") as f:
                    return {line.strip() for line in f} <= IMPORT_LINES
            except OSError:
                return False
        diff = self.git("diff", "--no-ext-diff", "-U0", since, "--", CLAUDE_MD)
        added = [l[1:] for l in diff.splitlines() if l.startswith("+") and not l.startswith("+++")]
        removed = [l for l in diff.splitlines() if l.startswith("-") and not l.startswith("---")]
        return not removed and all(a.strip() in IMPORT_LINES for a in added)
