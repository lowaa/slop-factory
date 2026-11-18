from __future__ import annotations

import logging
import shutil
import subprocess
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.fleet.wiring import install_repo_env
from slop_haus_supervisor.ports import ProcessRunner, SubprocessRunner
from slop_haus_supervisor.service.instance_lock import AlreadyRunning, InstanceLock
from slop_haus_supervisor.slophaus.credentials import CREDENTIALS, TokenPair, credentials_lock
from slop_haus_supervisor.slophaus.token import SlotToken
from slop_haus_supervisor.slophaus.transport import ApiError, Transport
from slop_haus_supervisor.workspace import clone_probe
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.provisioned import Interactive, Kind, Provisioned, manifest_repos
from slop_haus_supervisor.workspace.layout import (
    Layout,
    SlotService,
    UnpushedCheck,
    _script_protocol,  # pyright: ignore[reportPrivateUsage]
    run_interactive,
)

# What names a slot's agent and signs it in; the rest of the old directory is rebuilt.
IDENTITY_FILES = (".slop-haus.env", f".slop-haus/{CREDENTIALS}", ".slop-haus/server-url")


class SlotProbe(Protocol):
    """Asks the server about a slot with the slot's own credential."""

    def access_token(self, directory: Path) -> str: ...
    def lease_held(self, access_token: str) -> bool: ...


class ServerSlotProbe:
    def __init__(self, server: str, runner: ProcessRunner) -> None:
        self._transport = Transport(server)
        self._runner = runner

    def access_token(self, directory: Path) -> str:
        return SlotToken(directory / ".slop-haus" / "token.sh", self._runner).get()

    def lease_held(self, access_token: str) -> bool:
        answer = self._transport.send("GET", "/api/worker/state", token=access_token)
        if not answer.ok:
            raise ApiError(f"could not read the slot's state: {answer.detail()}", answer.status)
        return bool(answer.json().get("lease"))


@dataclass(frozen=True)
class Rebuilt:
    name: str
    changed: bool
    # Where the previous directory was left (--keep-old); None when it was deleted or nothing was changed.
    kept: Path | None = None
    note: str = ""


def _is_repo(directory: Path) -> bool:
    return (directory / ".git").exists()


def _repo_folder(directory: Path, script: str, label: str) -> str | None:
    """The subfolder the old root repo's clone becomes, by its remote; the only repo, if the manifest has just one."""
    repos = [(path, remote) for path, remote in manifest_repos(script, label)]
    if len(repos) == 1:
        return repos[0][0]
    url = subprocess.run(["git", "-C", str(directory), "config", "--get", "remote.origin.url"], capture_output=True, text=True, check=False).stdout
    tail = url.strip().removesuffix(".git").replace(":", "/")
    return next((path for path, remote in repos if tail.endswith(remote)), None)


def _rebuild_locked(
    layout: Layout,
    name: str,
    service: SlotService | None,
    repo: UnpushedCheck,
    probe: SlotProbe,
    run: Interactive = run_interactive,
    keep_old: bool = False,
    clock: Callable[[], float] = time.time,
    ssh_works: clone_probe.Probe = clone_probe.ssh_works,
) -> Rebuilt:
    """Re-provision `<root>/slots/<name>` in the manifest's current layout, keeping its agent and sign-in.

    The directory is rebuilt from a fresh clone rather than by reusing the old `.git`: the old one
    carries worktrees, hooks and config from the old layout, and a refused rebuild (below) has
    already shown that nothing in it is unpushed, so the network is the only cost.
    Everything that could refuse comes before the old directory is touched, and so does choosing the
    clone protocol: once the directory is aside there is no clone left to learn it from. A rebuild
    whose result is not a checkout of every manifest repo puts the old directory back.
    """
    config = cfg.load(layout.config_file)
    directory = layout.slot(name)
    if name not in config.slots or not directory.is_dir():
        raise cfg.ConfigError(f"no slot {name!r} at {directory}")
    if service is None:
        raise cfg.ConfigError("rebuild fetches the slot's workspace files through the supervisor's sign-in: run `login` first")
    if service.find_slot(name) is None:
        # A slot name the server already knows as an agent (a reused agent) has no supervisor link
        # yet; this supervisor lists it in its own config, so it takes it on (the server keeps the slot cap).
        try:
            service.adopt_slot(name)
        except ApiError as err:
            raise cfg.ConfigError(f"the server has no live slot {name!r} of this supervisor and could not adopt one: {err}") from err
    token = probe.access_token(directory)
    script = service.update_script(TokenPair(token, "", 0))
    label = "the slot's workspace files"
    if not _is_repo(directory):
        return Rebuilt(name, False, note="already matches the manifest's layout")

    if repo.has_unpushed_anywhere(directory):
        raise cfg.ConfigError(f"slot {name!r} has work that is not pushed in {directory}: push or discard it, then rebuild")
    if probe.lease_held(token):
        raise cfg.ConfigError(f"slot {name!r} holds a lease: let its ticket finish or release it, then rebuild")


    protocol = _script_protocol(layout, config, script, [directory], ssh_works)
    folder = _repo_folder(directory, script, label)
    old_env = directory / ".env"
    carried_env = old_env.read_text() if old_env.is_file() else None
    aside = directory.with_name(f"{name}.pre-rebuild-{int(clock())}")
    # The old directory's credentials lock is taken before the rename and held to the end: flock follows the
    # inode, so a token.sh refreshing in the old directory finishes first, and one that starts later waits
    # for the copy below instead of spending a refresh token the copy then overwrites.
    with credentials_lock(directory):
        directory.rename(aside)
        try:
            directory.mkdir()
            # A token.sh started now resolves the new path, where there is no inode of the old lock: it takes
            # this one, and waits until the sign-in has been copied. Not held through install, whose update.sh
            # may itself run token.sh and would otherwise wait on us.
            with credentials_lock(directory):
                for relative in IDENTITY_FILES:
                    source = aside / relative
                    if source.is_file():
                        (directory / relative).parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                        shutil.copy2(source, directory / relative)
            Provisioned(directory, Kind.SLOT, SubprocessRunner(), logging.getLogger("slop_haus_supervisor"), run=run).install(script, protocol)
            clone = directory / folder if folder else directory
            if clone.is_dir():
                if not install_repo_env(layout, [clone], agent=name) and carried_env is not None:
                    envfile.write_private(clone / ".env", carried_env)
        except BaseException as err:
            shutil.rmtree(directory, ignore_errors=True)
            aside.rename(directory)
            if isinstance(err, cfg.ConfigError):
                raise cfg.ConfigError(f"{err}; the old directory is back in place at {directory}") from err
            raise
        if keep_old:
            return Rebuilt(name, True, kept=aside)
        shutil.rmtree(aside, ignore_errors=True)
        return Rebuilt(name, True)


def rebuild_slot(
    layout: Layout,
    name: str,
    service: SlotService | None,
    repo: UnpushedCheck,
    probe: SlotProbe,
    run: Interactive = run_interactive,
    keep_old: bool = False,
    clock: Callable[[], float] = time.time,
    ssh_works: clone_probe.Probe = clone_probe.ssh_works,
) -> Rebuilt:
    # The supervisor's own instance lock, held throughout: a `start` cannot slip in between the
    # checks and the swap, and two rebuilds take turns.
    try:
        with InstanceLock(layout.root / ".run.lock"):
            return _rebuild_locked(layout, name, service, repo, probe, run, keep_old, clock, ssh_works)
    except AlreadyRunning as err:
        raise cfg.ConfigError(f"the supervisor is running or another rebuild is: stop it, then rebuild ({err})") from err
