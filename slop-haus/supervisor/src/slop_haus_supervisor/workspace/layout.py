from __future__ import annotations

import fcntl
import logging
import os
import re
import subprocess
import tempfile
import shutil
from collections.abc import Callable, Generator, Sequence
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef
from slop_haus_supervisor.workspace import clone_probe
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import package
from slop_haus_supervisor.workspace.clone_probe import Probe
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.provisioned import Interactive, Kind, Provisioned, manifest_repos

HARNESSES = registry.NAMES

SECRETS_PLACEHOLDER = """\
# Read by the supervisor only, and kept on this host: nothing here is sent to the server.
# Set one of these for Claude:
#   CLAUDE_CODE_OAUTH_TOKEN  from `claude setup-token`
#   ANTHROPIC_API_KEY        an API key, instead
# Every other KEY=value here reaches each worker's environment too. Manage them with
# `slop-haus-supervisor secret set|unset|list`; the supervisor reads this file at start-up.
CLAUDE_CODE_OAUTH_TOKEN=
"""


@dataclass(frozen=True)
class Layout:
    root: Path

    @property
    def config_file(self) -> Path:
        return self.root / "supervisor.toml"

    @property
    def secrets_file(self) -> Path:
        return self.root / "secrets.env"

    @property
    def repo_env_file(self) -> Path:
        return self.root / "repo.env"

    @property
    def remote_control_dir(self) -> Path:
        """The by-kind layer of the host's env files, read by Remote Control slots only (layered_env.py)."""
        return self.root / "remote-control"

    def remote_control_workspace(self, name: str) -> Path:
        """Where a supervisor-run Remote Control host's workspace lives, apart from the env layer above."""
        return self.root / "remote-control-hosts" / cfg.check_name("remote control", name)

    def agent_dir(self, name: str) -> Path:
        """The by-agent layer: named by the agent, so it applies whatever the agent's kind."""
        return self.root / "agents" / cfg.check_name("agent", name)

    @property
    def supervisor_dir(self) -> Path:
        """The supervisor's own sign-in, laid out as a slot's is: `.slop-haus/credentials.json` and token.sh."""
        return self.root / "supervisor"

    @property
    def state_dir(self) -> Path:
        return self.root / "state"

    @property
    def paused_file(self) -> Path:
        return self.state_dir / "paused.json"

    @property
    def control_dir(self) -> Path:
        return self.state_dir / "control"

    @property
    def app(self) -> Path:
        return self.root / "app"

    @property
    def slots(self) -> Path:
        return self.root / "slots"

    def harness_dir(self, harness: str) -> Path:
        return self.root / "harness" / harness

    def slot(self, name: str) -> Path:
        return self.slots / cfg.check_name("slot", name)


def _write_secrets(path: Path) -> None:
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        # Someone's keys: never rewritten, only kept private.
        path.chmod(0o600)
        return
    with os.fdopen(fd, "w") as out:
        out.write(SECRETS_PLACEHOLDER)


@contextmanager
def _config_lock(layout: Layout) -> Generator[None]:
    """Serialises read-modify-write of supervisor.toml, so two `slot add` runs keep both slots."""
    with (layout.root / ".supervisor.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def _write_config(layout: Layout, config: SupervisorConfig) -> None:
    # Beside the target and renamed over it, so a crash never leaves half a file.
    fd, tmp = tempfile.mkstemp(dir=layout.root, prefix=".supervisor.toml-")
    with os.fdopen(fd, "w") as out:
        out.write(config.dumps())
    os.replace(tmp, layout.config_file)
    layout.config_file.chmod(0o644)


def set_up(layout: Layout, config: SupervisorConfig, archive: Path) -> SupervisorConfig:
    """Create or refresh the root. Re-running keeps the slots and secrets that exist."""
    if config.harness not in HARNESSES:
        raise cfg.ConfigError(f"harness {config.harness!r} must be one of {', '.join(HARNESSES)}")
    existing = cfg.load(layout.config_file) if layout.config_file.exists() else None
    # Before anything is touched: another tenant's root keeps its app too.
    if existing and existing.tenant_id != config.tenant_id:
        raise cfg.ConfigError(
            f"{layout.root} belongs to tenant {existing.tenant_id}, not {config.tenant_id} -- pick another --root"
        )
    layout.root.mkdir(parents=True, exist_ok=True)
    layout.slots.mkdir(exist_ok=True)
    for harness in HARNESSES:
        layout.harness_dir(harness).mkdir(parents=True, exist_ok=True)
    # The Claude config directory holds the login; no one else needs to read it.
    layout.harness_dir("claude").chmod(0o700)
    _write_secrets(layout.secrets_file)
    package.install(archive, layout.root)

    if existing:
        config = SupervisorConfig(config.server, config.tenant_id, config.tenant, config.harness, config.variant, existing.slots)
    _write_config(layout, config)
    return config


def run_interactive(argv: list[str], cwd: Path) -> int:
    return subprocess.run(argv, cwd=cwd, check=False).returncode


_REMOTE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9._/-]+$")


def _origin_protocol(directory: Path) -> str | None:
    url = subprocess.run(
        ["git", "-C", str(directory), "config", "--get", "remote.origin.url"],
        capture_output=True, text=True, check=False,
    ).stdout.strip()  # fmt: skip
    if url.startswith(("git@", "ssh://")):
        return "ssh"
    if url.startswith(("https://", "http://")):
        return "https"
    return None


def _clone_protocol(
    layout: Layout, config: SupervisorConfig, remote: str | None = None, hints: Sequence[Path] = (), ssh_works: Probe = clone_probe.ssh_works
) -> str:
    """How this host clones: SLOP_HAUS_CLONE_PROTOCOL, the stored clone_protocol, how an existing repo was cloned
    (`hints`, then the other slots), then -- for a `remote` -- whether ssh works, else https.

    Rebuild moves the slot aside, so there may be no repo to learn from, and a private repo does not clone over
    https without a prompt: the probe is what finds the way that works.
    """
    chosen = os.environ.get("SLOP_HAUS_CLONE_PROTOCOL")
    if chosen in ("ssh", "https"):
        return chosen
    if chosen:
        raise cfg.ConfigError(f"SLOP_HAUS_CLONE_PROTOCOL={chosen!r} must be ssh or https")
    if config.clone_protocol:
        return config.clone_protocol
    for directory in [*hints, *(layout.slot(other) for other in config.slots)]:
        found = _origin_protocol(directory)
        if found:
            return found
    if remote is not None:
        host, _, path = remote.partition("/")
        probe = ssh_works(f"git@{host}:{path}.git")
        protocol = "ssh" if probe.works else "https"
        # Written so the next clone (and the next probe-less path) agrees, and so a silent https fallback is visible.
        # A choice another workspace stored while this one probed stands: under the lock, the first one written wins.
        stored = _record_slot(layout, lambda current: current if current.clone_protocol else current.with_clone_protocol(protocol))
        if stored.clone_protocol != protocol:
            print(f"clone protocol for this host: {stored.clone_protocol}, already stored in {layout.config_file} (this probe: {protocol}, {probe.reason})")
            return stored.clone_protocol
        print(f"clone protocol for this host: {protocol} ({probe.reason}); stored in {layout.config_file}")
        return protocol
    return "https"


def _record_slot(layout: Layout, update: Callable[[SupervisorConfig], SupervisorConfig]) -> SupervisorConfig:
    # Re-read under the lock: another slot may have been recorded while this one was being made.
    with _config_lock(layout):
        updated = update(cfg.load(layout.config_file))
        _write_config(layout, updated)
    return updated


def add_slot(
    layout: Layout,
    name: str,
    setup_script: Path | None,
    run: Interactive = run_interactive,
    service: SlotService | None = None,
) -> SupervisorConfig:
    """Make `<root>/slots/<name>` a workspace.

    With a `service` (the supervisor is signed in) the server creates the slot's agent and hands
    its credential over. Without one, the agent's own setup script is run in it: the one the board
    hands out when it registers the agent, which writes the managed files, clones the manifest's
    repos in full and walks the device sign-in. Either way the slot is recorded only once it
    succeeded, so a failed setup can simply be run again.
    """
    config = cfg.load(layout.config_file)
    directory = layout.slot(name)
    if name in config.slots:
        raise cfg.ConfigError(f"slot {name!r} already exists at {directory}")
    if service is not None:
        _provision_slot(layout, config, name, service, run)
        return _record_slot(layout, lambda current: current.with_slot(name))
    if setup_script is None or not setup_script.is_file():
        raise cfg.ConfigError(f"{setup_script} does not exist" if setup_script else "give --setup-script, or sign the supervisor in with `login`")
    directory.mkdir(parents=True, exist_ok=True)
    if run(["bash", str(setup_script.resolve())], directory) != 0:
        raise cfg.ConfigError(f"the setup script failed in {directory}; fix that and run slot add again")
    return _record_slot(layout, lambda current: current.with_slot(name))


class SlotService(Protocol):
    """What the server does for a signed-in supervisor (slophaus/supervisor_api.py)."""

    def create_slot(self, name: str) -> SlotRef: ...
    def find_slot(self, name: str) -> SlotRef | None: ...
    def adopt_slot(self, name: str) -> SlotRef: ...
    def is_retired(self, name: str) -> bool: ...
    def retire_slot(self, slot: SlotRef) -> None: ...
    def issue_credential(self, slot: SlotRef) -> TokenPair: ...
    def update_script(self, tokens: TokenPair) -> str: ...


def _script_protocol(layout: Layout, config: SupervisorConfig, script: str, hints: Sequence[Path] = (), ssh_works: Probe = clone_probe.ssh_works
) -> str | None:
    """The clone protocol for the script's manifest repos; None when it clones nothing."""
    remotes = [remote for _, remote in manifest_repos(script, "the slot's workspace files") if _REMOTE.match(remote)]
    return _clone_protocol(layout, config, remotes[0], hints, ssh_works) if remotes else None


def clone_protocol_chooser(layout: Layout, ssh_works: Probe = clone_probe.ssh_works) -> Callable[[str], str | None]:
    """How a workspace other than a slot (a Remote Control host's) clones: chosen as a slot's is, so one machine clones one way.

    supervisor.toml is read at each choice, not once: the protocol a slot's or another host's probe stored since is the
    one used, with no second probe.
    """
    return lambda script: _script_protocol(layout, cfg.load(layout.config_file), script, ssh_works=ssh_works)


class SlotEnrollment:
    """A slot's sign-in through the supervisor: the server's agent for the slot, then its credential."""

    def __init__(self, service: SlotService, slot: SlotRef, server: str) -> None:
        self._service = service
        self._slot = slot
        self.server = server

    def issue(self) -> TokenPair:
        return self._service.issue_credential(self._slot)

    def update_script(self, tokens: TokenPair) -> str:
        return self._service.update_script(tokens)


def _provision_slot(layout: Layout, config: SupervisorConfig, name: str, service: SlotService, run: Interactive) -> None:
    directory = layout.slot(name)
    # Before the agent exists, so a slot that cannot be made leaves nothing on the server.
    if directory.exists() and not directory.is_dir():
        raise cfg.ConfigError(f"{directory} exists and is not a directory")
    # The server is asked first: a cap reached or a name taken is its answer to give, and
    # nothing is on disk yet to clean up.
    slot = service.create_slot(name)
    made_directory = not directory.exists()
    try:
        unit = Provisioned(directory, Kind.SLOT, SubprocessRunner(), logging.getLogger("slop_haus_supervisor"), SlotEnrollment(service, slot, config.server), run)
        unit.ensure(lambda script: _script_protocol(layout, config, script))
    except BaseException as err:
        try:
            service.retire_slot(slot)
        except Exception as retire_err:
            err.add_note(f"slot {name!r} could not be retired either ({retire_err}); run `slot remove {name}`")
        if made_directory:
            shutil.rmtree(directory, ignore_errors=True)
        raise


class UnpushedCheck(Protocol):
    def has_unpushed_anywhere(self, directory: Path) -> bool: ...


def remove_slot(layout: Layout, name: str, service: SlotService | None, repo: UnpushedCheck) -> SupervisorConfig:
    """Retire the slot's agent and delete its directory, unless the directory holds work no remote has.

    The check comes first, so a refused removal retires nothing.
    """
    config = cfg.load(layout.config_file)
    directory = layout.slot(name)
    slot = service.find_slot(name) if service is not None else None
    if name not in config.slots and slot is None:
        if service is not None and service.is_retired(name):
            raise cfg.ConfigError(f"slot {name!r} is already retired: nothing is left to remove")
        raise cfg.ConfigError(f"no slot {name!r}")
    if directory.exists() and repo.has_unpushed_anywhere(directory):
        raise cfg.ConfigError(f"slot {name!r} has work that is not pushed in {directory}: push or discard it, then remove the slot")
    if service is not None and slot is not None:
        service.retire_slot(slot)
    if directory.exists():
        shutil.rmtree(directory)
    return _record_slot(layout, lambda current: current.without_slot(name))
