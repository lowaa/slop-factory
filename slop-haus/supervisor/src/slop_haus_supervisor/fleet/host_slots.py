"""Which desired agents become a Remote Control host process, and the host's login check."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass, replace
from pathlib import Path

from slop_haus_supervisor.fleet.host_slot import HostWorkspace, MissingLogin, RemoteControlDeps, RemoteControlSlot
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.codex_login import CodexSlotGuard
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, UnsupportedHarness, build_harness
from slop_haus_supervisor.service.run_env import CODEX_SHARED_LOGIN
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned

LAUNCH_SUPERVISOR = "supervisor"


def missing_login(root: Path, server: str, pi_extension: Path | None = None) -> MissingLogin:
    """What a worker's harness login check says, for the harness a host is to run (`harness` as the board names it)."""

    def missing(harness: str, secrets: Mapping[str, str]) -> str:
        try:
            checked = build_harness(HarnessKind.SLOT, harness, HarnessContext(root, server, {k: v for k, v in secrets.items() if v}, pi_extension=pi_extension))
        except UnsupportedHarness as err:
            return str(err)
        return checked.missing_credentials()

    return missing


class ProvisionedHost:
    """A Remote Control host's `Provisioned`, cloning the manifest's repos the way the machine's slots clone theirs.

    Left to itself, the update script's sync-repos guesses from the clones already there; a new workspace has none, so it
    guesses https, which a private repo refuses without a prompt.
    """

    def __init__(self, unit: Provisioned, protocol_for: Callable[[str], str | None]) -> None:
        self._unit = unit
        self._protocol_for = protocol_for

    def ensure(self) -> None:
        self._unit.ensure(self._protocol_for)

    def refresh(self) -> None:
        self._unit.refresh()


@dataclass(frozen=True)
class RemoteControlSlots:
    """Builds the slot for an entry the supervisor is to run, and for no other.

    `workspace_for` makes the agent's workspace (a `ProvisionedHost`), given its name.
    """

    deps: RemoteControlDeps
    workspace_for: Callable[[str], HostWorkspace]
    # A Codex session is a user of the host's one ChatGPT login like a Codex worker slot is: `codex_users` is
    # how many other Codex users (worker slots and Remote Control sessions) the host has now.
    codex_guard: CodexSlotGuard | None = None
    codex_users: Callable[[], int] = lambda: 0

    def build(self, kind: Kind, launch: str, name: str, harness: str) -> RemoteControlSlot | None:
        """None for anything but a `remote_control` entry with `launch: supervisor`: a manual host is the owner's.

        Raises ConfigError for a name that is no directory name, or a harness a host cannot run: neither becomes a process.
        """
        if kind is not Kind.REMOTE_CONTROL or launch != LAUNCH_SUPERVISOR:
            return None
        cfg.check_name("remote control", name)
        if harness not in registry.REMOTE_CONTROL_NAMES:
            raise cfg.ConfigError(f"the {harness} harness is not available for Remote Control hosts")
        if harness == "codex" and self.codex_guard is not None and not self.codex_guard.allows(self.codex_users() + 1):
            raise cfg.ConfigError(f"more than one Codex user needs {CODEX_SHARED_LOGIN}=verified in run.env")
        deps = replace(self.deps, workspace=self.workspace_for(name))
        return RemoteControlSlot(name, harness, deps)
