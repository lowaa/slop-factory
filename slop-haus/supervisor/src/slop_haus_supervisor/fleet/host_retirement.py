"""The end of a Remote Control agent the owner removed: after its host has drained, nothing it could use is left."""

from __future__ import annotations

import logging
import shutil
from pathlib import Path
from dataclasses import dataclass
from typing import Protocol

from slop_haus_supervisor.service.instance_lock import AlreadyRunning
from slop_haus_supervisor.workspace.host_files import supervisor_lock
from slop_haus_supervisor.workspace.layout import Layout, UnpushedCheck


class AgentRetirer(Protocol):
    def retire_agent(self, name: str) -> None:
        """Asks the server to retire the agent, which revokes its OAuth grants so a copied token stops working."""
        ...


@dataclass(frozen=True)
class Retirement:
    # The directory was kept because it holds work no remote has.
    kept_directory: bool


class HostRetirement:
    def __init__(self, layout: Layout, retirer: AgentRetirer, repo: UnpushedCheck, logger: logging.Logger) -> None:
        self._layout = layout
        self._retirer = retirer
        self._repo = repo
        self._log = logger

    def retire(self, name: str) -> Retirement:
        """Only for a host that is no longer running: the caller waits for the scheduler's drain first.

        The server is asked first so the credentials are dead whether or not the directory can be deleted.
        """
        self._retirer.retire_agent(name)
        directory = self._layout.remote_control_workspace(name)
        if not directory.exists():
            return Retirement(kept_directory=False)
        if self._held_by_a_host(directory):
            # The slot paused for a host started by hand and was retired while it still runs: that host's files are not ours to delete.
            self._log.warning("%s: retired, but a host started by hand is running in %s, so it is kept", name, directory)
            return Retirement(kept_directory=True)
        if self._repo.has_unpushed_anywhere(directory):
            self._log.warning("%s: retired, but %s holds work that is not pushed, so it is kept", name, directory)
            return Retirement(kept_directory=True)
        shutil.rmtree(directory)
        return Retirement(kept_directory=False)

    @staticmethod
    def _held_by_a_host(directory: Path) -> bool:
        if not (directory / ".slop-haus").is_dir():
            return False
        lock = supervisor_lock(directory)
        try:
            lock.__enter__()
        except AlreadyRunning:
            return True
        lock.__exit__(None, None, None)
        return False
