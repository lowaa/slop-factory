from __future__ import annotations

import fcntl
import json
import logging
import os
import re
import shutil
import subprocess
import tempfile
from collections.abc import Callable, Generator
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath
from typing import Protocol, cast

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.workspace.layout import UnpushedCheck
from slop_haus_supervisor.workspace.provisioned import _origin_matches  # pyright: ignore[reportPrivateUsage]
from slop_haus_supervisor.workspace.repos import CLONE_DEPTH

STATE_FILE = Path(".slop-haus") / "reconciled-repos.json"
LOCK_FILE = Path(".slop-haus") / "sync.lock"
GIT_TIMEOUT = 60.0
FETCH_TIMEOUT = 120.0


@dataclass(frozen=True)
class RepoSpec:
    """One manifest repo: `path` inside the workspace, `remote` as `host/owner/repo`, `base` the branch agents cut from."""

    path: str
    remote: str
    base: str = ""


@dataclass(frozen=True)
class Manifest:
    revision: str
    repos: tuple[RepoSpec, ...]


class ManifestSource(Protocol):
    def fetch(self) -> Manifest | None:
        """The server's current repo list; None when it cannot be had, which leaves the workspace as it is."""
        ...


@dataclass(frozen=True)
class ReconcileSettings:
    # `workspace_reconcile`: removal and moves. Off, only the additions (which update.sh does) happen.
    destructive: bool = False
    # The owner's confirmation, on the board, of the dry run's preview. Until then nothing is deleted.
    removal_confirmed: bool = False


@dataclass(frozen=True)
class Clone:
    path: str
    remote: str


@dataclass(frozen=True)
class Remove:
    path: str


@dataclass(frozen=True)
class WouldRemove:
    """Dry run: what a removal would delete, reported and not done."""

    path: str


@dataclass(frozen=True)
class Move:
    src: str
    dst: str


@dataclass(frozen=True)
class SetRemote:
    path: str
    remote: str


@dataclass(frozen=True)
class FetchBase:
    path: str
    base: str


@dataclass(frozen=True)
class KeepUnpushed:
    """A removal or move held back because deleting or relocating it would be risky; `reason` names no file contents."""

    path: str
    intended: str
    reason: str


@dataclass(frozen=True)
class MoveConflict:
    paths: tuple[str, ...]
    reason: str


@dataclass(frozen=True)
class DeferredBusy:
    reason: str


@dataclass(frozen=True)
class InvalidPath:
    path: str
    reason: str


Action = Clone | Remove | WouldRemove | Move | SetRemote | FetchBase | KeepUnpushed | MoveConflict | DeferredBusy | InvalidPath
DESTRUCTIVE = (Remove, Move)


@dataclass
class Report:
    revision: str = ""
    actions: list[Action] = field(default_factory=list[Action])
    # Set when the manifest moved between the plan and the apply: nothing was done.
    stale: bool = False
    failed: list[str] = field(default_factory=list[str])


class _GitFailed(Exception):
    pass


Busy = Callable[[Path], bool]


def process_in_workspace(workspace: Path) -> bool:
    """Whether another process has its working directory inside `workspace`: an agent's leftover background job.

    Reading /proc can fail per process (it ended, or is not ours); an unreadable one is skipped, and a host with
    no /proc finds nothing, so the advisory lock is then the only guard.
    """
    root = workspace.resolve()
    me = os.getpid()
    try:
        entries = os.listdir("/proc")
    except OSError:
        return False
    for entry in entries:
        if not entry.isdigit() or int(entry) == me:
            continue
        try:
            cwd = Path(os.readlink(f"/proc/{entry}/cwd"))
        except OSError:
            continue
        if cwd == root or root in cwd.parents:
            return True
    return False


def _deferred(actions: list[Action], reason: str) -> list[Action]:
    """`actions` without the destructive ones, which wait for a quieter workspace."""
    return [*(a for a in actions if not isinstance(a, DESTRUCTIVE)), DeferredBusy(reason)]


def check_path(path: str) -> str | None:
    """Why a manifest path may not be joined to the workspace, or None when it may.

    Relative, plain components only: no `..`, no hidden folder (`.slop-haus`, `.git`, which the clone scan never
    enters either) and no deeper than the scan looks.
    """
    if not path or "\x00" in path or "\\" in path:
        return "empty or contains an illegal character"
    parts = PurePosixPath(path).parts
    if path.startswith("/") or PurePosixPath(path).is_absolute():
        return "is not relative"
    if any(part in ("", ".", "..") or part.startswith(".") for part in parts) or len(parts) != len(path.split("/")):
        return "has a component that is empty, hidden or goes up"
    if len(parts) > CLONE_DEPTH:
        return f"is more than {CLONE_DEPTH} folders deep"
    return None


def _no_symlink_on_path(workspace: Path, path: str) -> bool:
    current = workspace
    for part in PurePosixPath(path).parts:
        current = current / part
        if current.is_symlink():
            return False
    return True


def _nested(a: str, b: str) -> bool:
    return a == b or a.startswith(b + "/") or b.startswith(a + "/")


def default_remote_url(existing: str, remote: str) -> str:
    """`remote` in the way `existing` was cloned, so a re-point does not change how this host authenticates."""
    host, _, path = remote.partition("/")
    if existing.startswith(("git@", "ssh://")):
        return f"git@{host}:{path}.git"
    return f"https://{host}/{path}.git"


class WorkspaceReconciler:
    """Brings a workspace's checkouts in line with the manifest: re-point, move, remove, and fetch a changed base.

    Additions are planned and reported but left to the workspace's update.sh, which clones them (with the host's
    protocol and prompts); the reconciler runs first, so a clone into a vacated path works.

    What a workspace once held is remembered in `.slop-haus/reconciled-repos.json`, and only those checkouts are
    candidates for removal or a move: a clone the agent made itself is never ours to delete. The file is inside
    the workspace, so it is not trusted either: a path is validated and its origin must still be the recorded remote.
    """

    def __init__(
        self,
        source: ManifestSource,
        runner: ProcessRunner,
        unpushed: UnpushedCheck,
        logger: logging.Logger,
        settings: Callable[[], ReconcileSettings] = ReconcileSettings,
        busy: Busy = process_in_workspace,
        remote_url: Callable[[str, str], str] = default_remote_url,
    ) -> None:
        self._source = source
        self._runner = runner
        self._unpushed = unpushed
        self._log = logger
        self._settings = settings
        self._busy = busy
        self._remote_url = remote_url

    # -- state --------------------------------------------------------------------------------------------

    def _read_state(self, workspace: Path) -> list[RepoSpec]:
        try:
            listed = json.loads((workspace / STATE_FILE).read_text())
        except (OSError, ValueError):
            return []
        specs: list[RepoSpec] = []
        for entry in cast(list[object], listed) if isinstance(listed, list) else []:
            fields = cast(dict[str, object], entry) if isinstance(entry, dict) else {}
            path, remote, base = fields.get("path"), fields.get("remote"), fields.get("base", "")
            if isinstance(path, str) and isinstance(remote, str) and isinstance(base, str):
                specs.append(RepoSpec(path, remote, base))
        return specs

    def _write_state(self, workspace: Path, specs: list[RepoSpec]) -> None:
        target = workspace / STATE_FILE
        target.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=target.parent, prefix=".reconciled-")
        with os.fdopen(fd, "w") as out:
            json.dump([{"path": s.path, "remote": s.remote, "base": s.base} for s in specs], out)
        os.replace(tmp, target)

    # -- git ----------------------------------------------------------------------------------------------

    def _git(self, directory: Path, *args: str, timeout: float = GIT_TIMEOUT) -> tuple[int, str]:
        try:
            done = self._runner.run(["git", "-C", str(directory), *args], directory, timeout)
        except (OSError, subprocess.SubprocessError):
            return 1, ""
        return done.returncode, done.stdout

    def _ok(self, directory: Path, *args: str, timeout: float = GIT_TIMEOUT) -> bool:
        return self._git(directory, *args, timeout=timeout)[0] == 0

    def _origin(self, checkout: Path) -> str:
        code, out = self._git(checkout, "config", "--get", "remote.origin.url")
        return out.strip() if code == 0 else ""

    def _risk(self, checkout: Path) -> str | None:
        """Why this checkout may not be deleted or moved, or None when it is clean.

        Each condition is asked on its own, and a question that cannot be answered counts as a yes.
        """
        code, out = self._git(checkout, "status", "--porcelain", "--untracked-files=no")
        if code != 0 or out.strip():
            return "uncommitted changes"
        code, out = self._git(checkout, "ls-files", "--others", "--exclude-standard", "--directory", "--no-empty-directory")
        if code != 0 or out.strip():
            return "untracked files"
        code, out = self._git(checkout, "stash", "list")
        if code != 0 or out.strip():
            return "stashed changes"
        if self._unpushed.has_unpushed_anywhere(checkout):
            return "commits no remote has"
        return None

    # -- plan ---------------------------------------------------------------------------------------------

    def plan(self, workspace: Path, manifest: Manifest) -> list[Action]:
        """What bringing `workspace` to `manifest` would do. Reads the disk, changes nothing."""
        settings = self._settings()
        actions: list[Action] = []
        specs: list[RepoSpec] = []
        for spec in manifest.repos:
            reason = check_path(spec.path) or (None if valid_remote(spec.remote) else "has a remote that is not host/owner/repo") or next(
                (f"is inside or contains {other.path}" for other in specs if _nested(spec.path, other.path)), None
            )
            if reason is None and not _no_symlink_on_path(workspace, spec.path):
                reason = "goes through a symlink"
            if reason:
                actions.append(InvalidPath(spec.path, reason))
            else:
                specs.append(spec)

        previous = {s.path: s for s in self._read_state(workspace)}
        by_path = {s.path: s for s in specs}
        for spec in specs:
            checkout = workspace / spec.path
            if not (checkout / ".git").exists():
                actions.append(Clone(spec.path, spec.remote))
                continue
            origin = self._origin(checkout)
            old = previous.get(spec.path)
            # Its recorded remote is wanted at another path: that is a move, which the departures plan refuses or does.
            leaving = old is not None and old.remote != spec.remote and any(s.remote == old.remote and s.path != spec.path for s in specs)
            if not leaving and (not origin or not _origin_matches(checkout, spec.remote)):
                actions.append(SetRemote(spec.path, spec.remote))
            if spec.base and old is not None and old.base != spec.base:
                actions.append(FetchBase(spec.path, spec.base))

        # A manifest with an entry that cannot be used is not one to delete by: the entry may be what names a repo
        # that is meant to stay.
        if settings.destructive and not any(isinstance(a, InvalidPath) for a in actions):
            departures = self._plan_departures(workspace, previous, by_path, settings)
            # The move fills that path, so there is nothing to clone into it.
            moved_to = {a.dst for a in departures if isinstance(a, Move)}
            actions = [a for a in actions if not (isinstance(a, Clone) and a.path in moved_to)] + departures
        return actions

    def _plan_departures(
        self, workspace: Path, previous: dict[str, RepoSpec], by_path: dict[str, RepoSpec], settings: ReconcileSettings
    ) -> list[Action]:
        out: list[Action] = []
        moves: list[Move] = []
        conflicts: list[Action] = []
        taken: set[str] = set()
        for path, old in sorted(previous.items()):
            if path in by_path and by_path[path].remote == old.remote:
                continue
            checkout = workspace / path
            if check_path(path) or not _no_symlink_on_path(workspace, path) or not (checkout / ".git").exists():
                continue
            if not _origin_matches(checkout, old.remote):
                # Not the checkout the workspace recorded: someone replaced it, so it is not ours to touch.
                out.append(KeepUnpushed(path, "remove", "its origin is not the recorded remote"))
                continue
            destination = next(
                (s for s in by_path.values() if s.remote == old.remote and s.path != path and s.path not in taken), None
            )
            if destination is None:
                if path in by_path:
                    continue  # the remote at this path changed: re-pointed, not removed
                intended: Action = Remove(path)
                label = "remove"
            else:
                taken.add(destination.path)
                intended = Move(path, destination.path)
                label = "move"
                if path in by_path or destination.path in previous or (workspace / destination.path).exists():
                    conflicts.append(MoveConflict((path, destination.path), "the target is taken, or the paths swap or form a cycle"))
                    continue
            risk = self._risk(checkout)
            if risk:
                out.append(KeepUnpushed(path, label, risk))
            elif isinstance(intended, Move):
                moves.append(intended)
            else:
                out.append(intended if settings.removal_confirmed else WouldRemove(path))
        if conflicts:
            # A swap or cycle is refused whole, never half-done.
            moves = []
        return [*conflicts, *moves, *out]

    # -- apply --------------------------------------------------------------------------------------------

    def apply(self, workspace: Path, actions: list[Action]) -> list[str]:
        """Performs `actions` in order (re-points, moves, removals); returns what failed. The caller holds the lock."""
        failed: list[str] = []
        order: dict[type, int] = {SetRemote: 0, FetchBase: 0, Move: 1, Remove: 2}
        for action in sorted(actions, key=lambda a: order.get(type(a), 3)):
            try:
                if isinstance(action, SetRemote):
                    self._set_remote(workspace, action)
                elif isinstance(action, FetchBase):
                    if not self._ok(workspace / action.path, "fetch", "origin", action.base, timeout=FETCH_TIMEOUT):
                        raise _GitFailed(f"FetchBase {action.path}")
                elif isinstance(action, Move):
                    self._move(workspace, action)
                elif isinstance(action, Remove):
                    self._remove(workspace, action)
            except _GitFailed as err:
                failed.append(str(err))
            except OSError as err:
                failed.append(f"{type(action).__name__} {getattr(action, 'path', getattr(action, 'src', ''))}: {type(err).__name__}")
        return failed

    def _set_remote(self, workspace: Path, action: SetRemote) -> None:
        checkout = workspace / action.path
        existing = self._origin(checkout)
        url = self._remote_url(existing, action.remote)
        if not self._ok(checkout, "remote", "set-url" if existing else "add", "origin", url):
            raise _GitFailed(f"SetRemote {action.path}")
        if not self._ok(checkout, "fetch", "origin", timeout=FETCH_TIMEOUT):
            raise _GitFailed(f"SetRemote {action.path}: fetch")

    def _move(self, workspace: Path, action: Move) -> None:
        src, dst = workspace / action.src, workspace / action.dst
        if dst.exists() or dst.is_symlink():
            return
        dst.parent.mkdir(parents=True, exist_ok=True)
        os.rename(src, dst)
        # Linked worktrees name the old path; this points them at the new one.
        self._git(dst, "worktree", "repair")
        parent = src.parent
        while parent != workspace and parent.is_relative_to(workspace):
            try:
                parent.rmdir()
            except OSError:
                break
            parent = parent.parent

    def _remove(self, workspace: Path, action: Remove) -> None:
        checkout = workspace / action.path
        if checkout.is_symlink() or not checkout.resolve().is_relative_to(workspace.resolve()):
            return
        shutil.rmtree(checkout)

    # -- the whole thing ----------------------------------------------------------------------------------

    @contextmanager
    def _lock(self, workspace: Path) -> Generator[bool]:
        path = workspace / LOCK_FILE
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a") as handle:
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                yield False
                return
            yield True

    def reconcile(self, workspace: Path) -> Report | None:
        """Plan, then apply under the workspace's lock after planning again against a freshly read manifest.

        Nothing destructive is applied from a stale plan: when the revision moved in between, nothing is done and
        the next run starts over. Returns None when there is no manifest to go by.
        """
        manifest = self._source.fetch()
        if manifest is None:
            return None
        report = Report(manifest.revision, self.plan(workspace, manifest))
        if not any(isinstance(a, (SetRemote, FetchBase, Move, Remove)) for a in report.actions):
            self._remember(workspace, manifest, report)
            return report
        with self._lock(workspace) as held:
            if not held:
                report.actions = _deferred(report.actions, "the workspace is locked")
                return report
            fresh = self._source.fetch()
            if fresh is None or fresh.revision != manifest.revision:
                report.stale = True
                return report
            report.actions = self.plan(workspace, fresh)
            if any(isinstance(a, DESTRUCTIVE) for a in report.actions) and self._busy(workspace):
                report.actions = _deferred(report.actions, "a process is running in the workspace")
            report.failed = self.apply(workspace, report.actions)
            self._remember(workspace, fresh, report)
        for action in report.actions:
            self._log.info("%s: workspace reconcile: %s", workspace.name, action)
        return report

    def _remember(self, workspace: Path, manifest: Manifest, report: Report) -> None:
        """The manifest's repos, plus any recorded checkout still on disk (kept or deferred) so it is judged again."""
        previous = self._read_state(workspace)
        # What did not work out (an invalid entry, a failed git call) keeps its old record, so it is judged again.
        unsettled = {a.path for a in report.actions if isinstance(a, InvalidPath)} | {
            spec.path for spec in manifest.repos if any(f.split(" ")[1].rstrip(":") == spec.path for f in report.failed)
        }
        current = {s.path for s in manifest.repos if s.path not in unsettled}
        kept = [s for s in previous if s.path not in current and ((workspace / s.path).exists() or s.path in unsettled)]
        valid = [s for s in manifest.repos if s.path in current]
        try:
            self._write_state(workspace, [*valid, *kept])
        except OSError as err:
            self._log.warning("%s: workspace reconcile could not record its state: %s", workspace.name, type(err).__name__)


_SAFE_REMOTE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9._/-]+$")


def valid_remote(remote: str) -> bool:
    return bool(_SAFE_REMOTE.match(remote))
