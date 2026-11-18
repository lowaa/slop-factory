from __future__ import annotations

import fnmatch
import re
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.workspace.repos import find_clones

SNAPSHOT_MESSAGE = "WIP: supervisor snapshot, may not build"
# The tip commit's subject tells the next start that the run before it was cut short.
INTERRUPTED_MESSAGE_PREFIX = "WIP: interrupted by a supervisor stop"
# Untracked files the interrupted save leaves in the slot: a model may write credentials without
# gitignoring them, and the save pushes to a remote others can read.
WITHHELD_NAME_PATTERNS = ("*.pem", "*.key", "id_*", ".env*", "*secret*", "*credential*", "*token*")
WITHHELD_MAX_BYTES = 5 * 1024 * 1024


@dataclass(frozen=True)
class SafetyPush:
    ok: bool
    commit: str = ""
    # Untracked files are never committed (they may hold secrets); the note names them.
    untracked: list[str] = field(default_factory=lambda: [])
    error: str = ""
    # Set when the branch had diverged from origin: the work went to this ref instead, and is safe there.
    rescue_ref: str = ""


@dataclass(frozen=True)
class Prepared:
    ok: bool
    error: str = ""
    # Something the slot should log although the branch is ready.
    warning: str = ""
    # Untracked files the interrupted save withheld from the push (secret-looking or oversized).
    withheld: list[str] = field(default_factory=lambda: [])
    # The branch holds work from a run that was cut short (uncommitted changes, or the stop's WIP commit).
    interrupted: bool = False


@dataclass(frozen=True)
class RepoSnapshot:
    """Where HEAD is (None when detached) and every local branch's tip, to see what a run touched."""

    head: str | None
    branches: dict[str, str]


class SlotRepo(Protocol):
    def prepare_branch(self, directory: Path, branch: str, base_commit: str = "") -> Prepared:
        """Put the slot checkout on `branch` before a claim's first run. Never stashes or discards.

        A dirty checkout already on `branch` is that ticket's own interrupted work and is kept as it is.

        A branch the server failed to make is cut from `base_commit` (origin/main without one) and pushed.
        """
        ...

    def check_clean(self, directory: Path) -> Prepared:
        """For a run with no branch: the checkout stays where it is, but never with someone's uncommitted changes."""
        ...

    def snapshot(self, directory: Path) -> RepoSnapshot: ...

    def stray_work(self, directory: Path, branch: str, before: RepoSnapshot) -> list[str]:
        """What a run left outside `branch` since `before`: a moved HEAD or commits on other branches."""
        ...

    def has_unpushed_work(self, directory: Path, branch: str) -> bool: ...

    def safety_push(self, directory: Path, branch: str) -> SafetyPush:
        """Commit tracked changes only (in the worktree holding `branch`) and push the branch without force.

        A branch that diverged from origin has its commits pushed to a rescue ref instead (`rescue_ref`).
        """
        ...

    def drop_superseded(self, directory: Path, branch: str) -> bool:
        """Reset a local `branch` whose patches all sit on origin to origin, and delete its helper branches.

        Returns whether anything was reset. Branches with a patch origin lacks are left alone.
        """
        ...

    def interrupted_push(self, directory: Path, ticket_id: int, branch: str) -> SafetyPush:
        """Commit everything (new non-ignored files too) and push `branch` without force, for a run about to be cut short."""
        ...

    def release_worktrees(self, directory: Path, branch: str) -> None:
        """Drop worktrees a run left behind for `branch`; a dirty one is kept."""
        ...


class GitSlotRepo:
    """A run may work its branch in a worktree of its own, so the slot's HEAD says nothing about the branch."""

    def __init__(self, runner: ProcessRunner) -> None:
        self._runner = runner

    def _git(self, directory: Path, *args: str):
        return self._runner.run(["git", *args], directory)

    def _untracked(self, directory: Path) -> list[str]:
        out = self._git(directory, "ls-files", "--others", "--exclude-standard").stdout
        return [line for line in out.splitlines() if line]

    def _checkout_of(self, directory: Path, branch: str) -> Path | None:
        listing = self._git(directory, "worktree", "list", "--porcelain").stdout
        path: str | None = None
        for line in listing.splitlines():
            if line.startswith("worktree "):
                path = line.removeprefix("worktree ")
            elif line == f"branch refs/heads/{branch}" and path is not None:
                # A run that deleted its worktree directory leaves an entry git has yet to prune;
                # there is nothing in it to commit, and running git there would not even start.
                return Path(path) if Path(path).is_dir() else None
        return None

    def _has_local_branch(self, directory: Path, branch: str) -> bool:
        return self._git(directory, "rev-parse", "--verify", "--quiet", f"refs/heads/{branch}").returncode == 0

    def _remote_has(self, directory: Path, branch: str) -> bool:
        return self._git(directory, "rev-parse", "--verify", "--quiet", f"refs/remotes/origin/{branch}").returncode == 0

    def _current_branch(self, directory: Path) -> str | None:
        return self._git(directory, "symbolic-ref", "--short", "-q", "HEAD").stdout.strip() or None

    def _check_clean(self, directory: Path, own_branch: str | None = None) -> Prepared:
        # Pruned so an attempt branch deleted on origin leaves no stale remote-tracking ref to switch to.
        fetch = self._git(directory, "fetch", "--prune", "origin")
        if fetch.returncode != 0:
            return Prepared(False, f"git fetch failed: {fetch.stderr.strip()}")
        # Untracked files are left alone: switching carries them along, and refuses if one is in the way.
        dirty = self._git(directory, "status", "--porcelain", "--untracked-files=no")
        if dirty.returncode != 0:
            return Prepared(False, "the slot checkout has uncommitted changes; they are left as they are")
        if dirty.stdout.strip():
            # Only the claimed ticket's own branch makes them that ticket's work, not the last ticket's.
            if own_branch is not None and self._current_branch(directory) == own_branch:
                return Prepared(True, interrupted=True)
            return Prepared(False, "the slot checkout has uncommitted changes; they are left as they are")
        return Prepared(True)

    def check_clean(self, directory: Path) -> Prepared:
        return self._check_clean(directory)

    def _cut_missing_branch(self, directory: Path, branch: str, base_commit: str) -> Prepared:
        base = base_commit or "origin/main"
        warning = f"{branch} was not on origin; cut it from {base}"
        # Pushed before the switch: a failed push must not leave a local-only branch the next try takes for ready.
        push = self._git(directory, "push", "origin", f"{base}:refs/heads/{branch}")
        if push.returncode != 0:
            return Prepared(False, f"{branch} exists neither locally nor on origin, and pushing it from {base} failed: {push.stderr.strip()}")
        fetch = self._git(directory, "fetch", "origin", branch)
        switch = self._git(directory, "switch", "-c", branch, "--track", f"origin/{branch}")
        if fetch.returncode != 0 or switch.returncode != 0:
            return Prepared(False, f"{branch} was pushed from {base} but switching to it failed: {(switch.stderr or fetch.stderr).strip()}")
        return Prepared(True, warning=warning)

    def prepare_branch(self, directory: Path, branch: str, base_commit: str = "") -> Prepared:
        clean = self._check_clean(directory, own_branch=branch)
        if not clean.ok:
            return clean
        remote = self._remote_has(directory, branch)
        if self._has_local_branch(directory, branch):
            switch = self._git(directory, "switch", branch)
        elif remote:
            switch = self._git(directory, "switch", "-c", branch, "--track", f"origin/{branch}")
        else:
            return self._cut_missing_branch(directory, branch, base_commit)
        if switch.returncode != 0:
            return Prepared(False, f"git switch {branch} failed: {switch.stderr.strip()}")
        interrupted = clean.interrupted or self._tip_is_interruption(directory, branch)
        if not remote:
            return Prepared(True, interrupted=interrupted, withheld=self._withheld_in(directory, interrupted))
        self._git(directory, "branch", f"--set-upstream-to=origin/{branch}", branch)
        # Commits of an earlier attempt stay; a branch behind origin is brought level.
        if not self.has_unpushed_work(directory, branch):
            reset = self._git(directory, "reset", "--keep", f"origin/{branch}")
            if reset.returncode != 0:
                return Prepared(False, f"git reset --keep failed: {reset.stderr.strip()}")
        interrupted = interrupted or self._tip_is_interruption(directory, branch)
        return Prepared(True, interrupted=interrupted, withheld=self._withheld_in(directory, interrupted))

    def _withheld_in(self, directory: Path, interrupted: bool) -> list[str]:
        return self._withheld(directory, self._untracked(directory)) if interrupted else []

    def _tip_is_interruption(self, directory: Path, branch: str) -> bool:
        subject = self._git(directory, "log", "-1", "--format=%s", f"refs/heads/{branch}").stdout.strip()
        return subject.startswith(INTERRUPTED_MESSAGE_PREFIX)

    def snapshot(self, directory: Path) -> RepoSnapshot:
        head = self._git(directory, "symbolic-ref", "--short", "-q", "HEAD").stdout.strip() or None
        listing = self._git(directory, "for-each-ref", "--format=%(refname:lstrip=2) %(objectname)", "refs/heads").stdout
        branches: dict[str, str] = {}
        for line in listing.splitlines():
            name, _, sha = line.rpartition(" ")
            if name:
                branches[name] = sha
        return RepoSnapshot(head, branches)

    def stray_work(self, directory: Path, branch: str, before: RepoSnapshot) -> list[str]:
        after = self.snapshot(directory)
        found: list[str] = []
        if after.head != branch:
            found.append(f"HEAD is on {after.head or 'a detached commit'}, not {branch}")
        for name, sha in after.branches.items():
            old = before.branches.get(name)
            if name == branch or old == sha:
                continue
            # Commits new to the repo: a branch merely created at an existing commit gained none.
            exclude = ["--remotes", *([old] if old else []), *(s for n, s in before.branches.items() if n != name)]
            new = self._git(directory, "rev-list", sha, "--not", *exclude).stdout.split()
            if new:
                found.append(f"{name} gained {len(new)} commit(s), tip {sha[:9]}")
        return found

    def has_unpushed_work(self, directory: Path, branch: str) -> bool:
        checkout = self._checkout_of(directory, branch)
        if checkout is not None:
            dirty = self._git(checkout, "status", "--porcelain", "--untracked-files=no")
            if dirty.returncode != 0 or dirty.stdout.strip():
                return True
        if not self._has_local_branch(directory, branch):
            return False
        # The branch ref, never HEAD: the slot may sit on another ticket's branch, whose commits
        # are not this branch's to push. Measured against the attempt's own remote branch: a commit
        # on some other branch (main, say) is still not pushed where the next agent will look. A
        # missing remote branch makes rev-list fail, and failing to ask counts as unpushed.
        ahead = self._git(directory, "rev-list", "--count", f"refs/heads/{branch}", "--not", f"refs/remotes/origin/{branch}")
        if ahead.returncode != 0:
            return True
        if ahead.stdout.strip() == "0":
            return False
        # Ahead of origin, or diverged: an agent that moved its work to another branch and pushed over the remote leaves the
        # local branch holding commits (a merge of main, say) that the remote replaced. Only a patch the
        # remote lacks is unpushed.
        behind = self._git(directory, "merge-base", "--is-ancestor", f"refs/remotes/origin/{branch}", f"refs/heads/{branch}")
        if behind.returncode == 0:
            return True
        return self._has_unmatched_patch(directory, branch, branch)

    def _has_unmatched_patch(self, directory: Path, remote_branch: str, local_branch: str) -> bool:
        """A commit is unpushed unless origin holds its patch (`git cherry` skips merges) or any remote holds the commit.

        The second half keeps main's commits, brought in by a merge, from reading as the branch's own.
        A merge commit with changes of its own (conflict resolution) is not weighed; agents rebuild
        their branch from the base rather than resolving conflicts into a diverged one.
        """
        cherry = self._git(directory, "cherry", f"refs/remotes/origin/{remote_branch}", f"refs/heads/{local_branch}")
        unmatched = {line[2:].strip() for line in cherry.stdout.splitlines() if line.startswith("+")}
        local_only = self._git(directory, "rev-list", "--no-merges", f"refs/heads/{local_branch}", "--not", "--remotes")
        if cherry.returncode != 0 or local_only.returncode != 0:
            return True
        return bool(unmatched & set(local_only.stdout.split()))

    def _helper_branches(self, directory: Path, branch: str) -> list[str]:
        match = re.fullmatch(r"(ticket-\d+-a\d+)", branch)
        if match is None:
            return []
        listing = self._git(directory, "for-each-ref", "--format=%(refname:lstrip=2)", f"refs/heads/{branch}-*").stdout
        return [name for name in listing.split() if name != branch]

    def drop_superseded(self, directory: Path, branch: str) -> bool:
        if not self._remote_has(directory, branch) or not self._has_local_branch(directory, branch):
            return False
        if self.has_unpushed_work(directory, branch):
            return False
        reset = False
        local = self._git(directory, "rev-parse", f"refs/heads/{branch}").stdout.strip()
        remote = self._git(directory, "rev-parse", f"refs/remotes/origin/{branch}").stdout.strip()
        if local != remote:
            checkout = self._checkout_of(directory, branch)
            if checkout is not None:
                reset = self._git(checkout, "reset", "--keep", f"refs/remotes/origin/{branch}").returncode == 0
            else:
                reset = self._git(directory, "branch", "-f", branch, f"refs/remotes/origin/{branch}").returncode == 0
        for helper in self._helper_branches(directory, branch):
            if self._has_unmatched_patch(directory, branch, helper):
                continue
            # Refused for a branch checked out somewhere, which is still in use.
            self._git(directory, "branch", "-D", helper)
        return reset

    def safety_push(self, directory: Path, branch: str) -> SafetyPush:
        return self._commit_and_push(directory, branch, SNAPSHOT_MESSAGE, include_untracked=False, rescue=True)

    def interrupted_push(self, directory: Path, ticket_id: int, branch: str) -> SafetyPush:
        message = f"{INTERRUPTED_MESSAGE_PREFIX} (ticket #{ticket_id})"
        return self._commit_and_push(directory, branch, message, include_untracked=True)

    def _commit_and_push(self, directory: Path, branch: str, message: str, include_untracked: bool, rescue: bool = False) -> SafetyPush:
        checkout = self._checkout_of(directory, branch)
        if checkout is None and not self._has_local_branch(directory, branch):
            return SafetyPush(True)
        untracked: list[str] = []
        if checkout is not None:
            untracked = self._untracked(checkout)
            add = self._git(checkout, "add", "-u")
            if include_untracked and add.returncode == 0:
                untracked = self._withheld(checkout, untracked)
                keep = [f for f in self._untracked(checkout) if f not in untracked]
                if keep:
                    add = self._git(checkout, "add", "--", *keep)
            if add.returncode != 0:
                return SafetyPush(False, untracked=untracked, error=add.stderr.strip())
            staged = self._git(checkout, "diff", "--cached", "--quiet")
            if staged.returncode != 0:
                commit = self._git(checkout, "commit", "-m", message)
                if commit.returncode != 0:
                    return SafetyPush(False, untracked=untracked, error=commit.stderr.strip())
        # Never forced, and only the attempt's own branch to its own name.
        push = self._git(directory, "push", "origin", f"refs/heads/{branch}:refs/heads/{branch}")
        if push.returncode != 0:
            if rescue:
                # Typically a non-fast-forward: the work stays safe on the remote under a name of its own.
                tip = self._git(directory, "rev-parse", "--short", f"refs/heads/{branch}").stdout.strip()
                ref = f"refs/heads/{branch}-rescue-{time.strftime('%Y%m%dT%H%M%S', time.gmtime())}-{tip}"
                rescued = self._git(directory, "push", "origin", f"refs/heads/{branch}:{ref}")
                if rescued.returncode == 0:
                    head = self._git(directory, "rev-parse", f"refs/heads/{branch}").stdout.strip()
                    return SafetyPush(True, commit=head, untracked=untracked, rescue_ref=ref)
            return SafetyPush(False, untracked=untracked, error=push.stderr.strip())
        head = self._git(directory, "rev-parse", f"refs/heads/{branch}").stdout.strip()
        return SafetyPush(True, commit=head, untracked=untracked)

    @staticmethod
    def _looks_withheld(checkout: Path, name: str) -> bool:
        parts = [part.lower() for part in Path(name).parts]
        if any(fnmatch.fnmatch(part, pattern) for part in parts for pattern in WITHHELD_NAME_PATTERNS):
            return True
        try:
            return (checkout / name).stat().st_size > WITHHELD_MAX_BYTES
        except OSError:
            return True

    def _withheld(self, checkout: Path, untracked: list[str]) -> list[str]:
        return [name for name in untracked if self._looks_withheld(checkout, name)]

    def has_unpushed_anywhere(self, directory: Path) -> bool:
        """Whether deleting the slot would lose work: any checkout with tracked changes, or any commit no remote holds.

        Failing to ask counts as yes. A slot cloned with no remote has nothing pushed, so it always does.
        A slot whose root is no repo is asked clone by clone; one with no clone, or a scan that fails, also counts as yes.
        """
        if (directory / ".git").exists():
            return self._repo_has_unpushed(directory)
        clones = find_clones(directory)
        return not clones.trusted or not clones.paths or any(self._repo_has_unpushed(clone) for clone in clones.paths)

    def _repo_has_unpushed(self, directory: Path) -> bool:
        listing = self._git(directory, "worktree", "list", "--porcelain")
        if listing.returncode != 0:
            return True
        checkouts = [Path(line.removeprefix("worktree ")) for line in listing.stdout.splitlines() if line.startswith("worktree ")]
        for checkout in checkouts:
            if checkout.is_dir():
                dirty = self._git(checkout, "status", "--porcelain", "--untracked-files=no")
                if dirty.returncode != 0 or dirty.stdout.strip():
                    return True
        # A repo with no commit yet has no HEAD to name, and nothing to lose.
        heads = ["HEAD"] if self._git(directory, "rev-parse", "--verify", "--quiet", "HEAD").returncode == 0 else []
        ahead = self._git(directory, "rev-list", "-1", "--branches", *heads, "--not", "--remotes")
        return ahead.returncode != 0 or bool(ahead.stdout.strip())

    def release_worktrees(self, directory: Path, branch: str) -> None:
        self._git(directory, "worktree", "prune")
        checkout = self._checkout_of(directory, branch)
        if checkout is None or checkout.resolve() == directory.resolve():
            return
        # No --force: git refuses a worktree with changes or untracked files, which stays for a person.
        self._git(directory, "worktree", "remove", str(checkout))
