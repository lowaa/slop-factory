"""The real-process `run`: slots built from supervisor.toml on the real machine's ports."""

from __future__ import annotations

import logging
import os
import shutil
import threading
from dataclasses import replace
import time
from collections.abc import Callable, Iterable, Mapping, Sequence
from importlib import metadata
from pathlib import Path

from slop_haus_supervisor.fleet.child_process import SubprocessSpawner
from slop_haus_supervisor.fleet.host_retirement import HostRetirement
from slop_haus_supervisor.fleet.host_enrollment import RemoteControlEnrollment
from slop_haus_supervisor.fleet.host_slot import RemoteControlDeps
from slop_haus_supervisor.fleet.host_slots import ProvisionedHost, RemoteControlSlots, missing_login, LAUNCH_SUPERVISOR
from slop_haus_supervisor.fleet.policy import PolicyConfig
from slop_haus_supervisor.fleet.scheduler import LogAlerts, Scheduler, SchedulerConfig, Steppable
from slop_haus_supervisor.fleet.slot import Slot, SlotDeps
from slop_haus_supervisor.fleet.slot_report import SlotStateReporter
from slop_haus_supervisor.harness.codex_login import CodexLogin, CodexSlotGuard
from slop_haus_supervisor.harness.base import Harness
from slop_haus_supervisor.harness.base import ConfiguredHarness
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, UnsupportedHarness, build_harness
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.harness.tool_check import ToolChecker, login_shell_which, read_tool_list
from slop_haus_supervisor.workspace.harness_config import HarnessConfigStore
from slop_haus_supervisor.reconcile.host import SchedulerHost
from slop_haus_supervisor.reconcile.ledger import ResumeLedger
from slop_haus_supervisor.reconcile.reconciler import Reconciler
from slop_haus_supervisor.ports import ProcessRunner, SubprocessRunner, SystemClock
from slop_haus_supervisor.service.control import ControlSpool
from slop_haus_supervisor.review.logins import CREDENTIAL_NAMES, LOGIN_DIRS, ReviewLogins
from slop_haus_supervisor.review.reviewer import Reviewer, ReviewSettings, review_script
from slop_haus_supervisor.service.run_env import CODEX_SHARED_LOGIN, RUN_SETTINGS
from slop_haus_supervisor.slophaus.credentials import CREDENTIALS
from slop_haus_supervisor.harness.version import detect_version
from slop_haus_supervisor.slophaus.http_client import HttpSlopHaus
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.transport import ApiError as TransportApiError
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.steward.guard import scratch_dir
from slop_haus_supervisor.steward.harness import build_steward_harness
from slop_haus_supervisor.steward.enrollment import StewardEnrollment
from slop_haus_supervisor.steward.runner import StewardConfig, StewardRunner
from slop_haus_supervisor.steward.selector import StewardHarnessSelector
from slop_haus_supervisor.steward.server import BoardStewardStatus, ServerSteward
from slop_haus_supervisor.steward.variant import StewardHarnessVariant
from slop_haus_supervisor.slophaus.token import SlotToken
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.briefing import SlotBriefing
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.handover import GitSlotRepo
from slop_haus_supervisor.workspace.manifest_source import ApiManifestSource
from slop_haus_supervisor.workspace.reconcile import ReconcileSettings, WorkspaceReconciler
from slop_haus_supervisor.workspace.layered_env import AgentKind, EnvKind, LayeredEnv, known_values
from slop_haus_supervisor.workspace.layout import Layout, clone_protocol_chooser
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from slop_haus_supervisor.workspace.repos import find_clones



def read_secrets(path: Path) -> dict[str, str]:
    """A blank value is a placeholder, not a credential."""
    return {key: value for key, value in envfile.read(path).items() if value}


def _repo_roots(slot_dir: Path) -> list[Path]:
    """Where a slot's repo roots are: the slot itself, or its clones when the manifest nests them."""
    if (slot_dir / ".git").exists():
        return [slot_dir]
    return find_clones(slot_dir).paths


def install_repo_env(layout: Layout, slot_dirs: Iterable[Path], agent: str | None = None) -> set[str]:
    """Puts each agent's effective repo.env into its slot's repo root(s) as `.env` (the self-review key file).

    The agent is the slot directory's name, or `agent` when the directories are not slots themselves (a rebuild's
    clone). Slot clones are fresh and `.env` is untracked, so it comes from here, never from the server.
    Returns the values, for masking.
    """
    texts: list[tuple[Path, str]] = []
    values: set[str] = set()
    for slot_dir in slot_dirs:
        name = agent or (slot_dir.name if slot_dir.parent == layout.slots else None)
        env = LayeredEnv(layout, AgentKind.WORKER, name)
        text = env.repo_env_text()
        if text is not None:
            texts.append((slot_dir, text))
            values.update(env.values(EnvKind.REPO).values())
    # All checked before any is written, so a failure never leaves some slots on a new file.
    for slot_dir, _ in texts:
        if not slot_dir.is_dir():
            raise cfg.ConfigError(f"slot directory {slot_dir} is missing: re-add the slot")
    for slot_dir, text in texts:
        for repo_dir in _repo_roots(slot_dir):
            envfile.write_private(repo_dir / ".env", text)
    return values


def agent_env(
    layout: Layout,
    kind: AgentKind = AgentKind.WORKER,
    maskers: Callable[[str], Iterable[SecretMasker]] = lambda _agent: (),
    warn: Callable[[str], None] = lambda _message: None,
) -> Callable[[str], dict[str, str]]:
    """Read at each run's start, so a `secret|env set --agent` reaches the agent's next run without a restart.

    What it reads is added to the agent's maskers first: a value set after start-up must be masked from its first run.
    """

    warned: set[str] = set()
    # Slots start runs on their own threads.
    warned_lock = threading.Lock()

    def warn_once(message: str) -> None:
        # Every run reads the files; the log should say it once.
        with warned_lock:
            if message in warned:
                return
            warned.add(message)
        warn(message)

    def env(agent: str) -> dict[str, str]:
        layered = LayeredEnv(layout, kind, agent, warn_once)
        values = layered.process_env()
        for masker in maskers(agent):
            masker.extend([*values.values(), *layered.mask_values()])
        return values

    return env


def review_env(layout: Layout, environ: Mapping[str, str]) -> Callable[[str], dict[str, str]]:
    """What slop-review runs with for an agent's change: what the agent's own run of it had, under the tenant's logins.

    The host's environment without slop-haus's own names (as a harness run gets it), the agent's secrets and
    run.env, and its repo.env values, which the agent would have loaded from the checkout's `.env`. Provider
    credentials and login directories come from those layers only, never the host's environment, which may be a
    person's shell; the reviewer points the login directories under the root. The reporting names are the reviewer's to set.

    HOME stays the host's. slop-review's claude sandbox denies reads of the credential stores under $HOME (~/.ssh,
    ~/.codex, ...), so a HOME elsewhere would open the real ones to the review's shell; and the blobless checkout's
    lazy fetches authenticate with the host's git setup, as the clone did.
    """
    dropped = {*CREDENTIAL_NAMES, *LOGIN_DIRS}

    def env(agent: str) -> dict[str, str]:
        layered = LayeredEnv(layout, AgentKind.WORKER, agent)
        merged = {
            k: v for k, v in environ.items() if k not in dropped and (not k.startswith("SLOP_HAUS_") or k in RUN_SETTINGS)
        }
        merged.update(layered.process_env())
        merged.update(layered.values(EnvKind.REPO))
        return merged

    return env


def build_reviewer(
    layout: Layout,
    config: SupervisorConfig,
    environ: Mapping[str, str],
    logger: logging.Logger,
    runner: ProcessRunner,
    codex_login: CodexLogin | None = None,
) -> Reviewer:
    """`codex_login` is the slots' shared refresher, so the review's copy of the login is refreshed under the same lock."""
    token = layout.supervisor_dir / ".slop-haus" / "token.sh"
    settings = ReviewSettings(
        review_script(environ), layout.state_dir / "reviews", config.server, token, ReviewLogins.under(layout.root, codex_login)
    )
    def remotes() -> list[str] | None:
        # Signed out, there is no token to read it with; the review says so before it asks.
        if not token.is_file():
            return None
        # Built per review, not at startup: a server the supervisor will not send
        # credentials to (plain http off this machine) refuses this review, and
        # must not stop the supervisor from running its agents.
        try:
            transport = Transport(config.server)
        except TransportApiError as err:
            logger.warning("reviews: cannot read the workspace manifest: %s", err)
            return None
        found = ApiManifestSource(SupervisorApi(transport, SlotToken(token, runner).get), logger).fetch()
        return None if found is None else [repo.remote for repo in found.repos]

    return Reviewer(settings, review_env(layout, environ), lambda agent: LayeredEnv(layout, AgentKind.WORKER, agent).repo_env_text(), remotes)


def _pi_extension(layout: Layout) -> Path | None:
    extension = layout.root / ".slop-haus" / "pi" / "slop-haus.ts"
    return extension if extension.is_file() and not extension.is_symlink() else None


def _version() -> str:
    try:
        return metadata.version("slop-haus-supervisor")
    except metadata.PackageNotFoundError:
        return ""


def slot_checkouts(layout: Layout) -> list[Path]:
    """The checkouts a Codex steward may read; listed at each build, as slots can be added from the board."""
    return sorted(path for path in layout.slots.glob("*") if path.is_dir())


def steward_forbidden(layout: Layout) -> list[Path]:
    """Directories a Codex steward's shell must not read: the probe plants a canary in each."""
    return [layout.root, layout.supervisor_dir, layout.state_dir]


def build_steward(
    layout: Layout,
    config: SupervisorConfig,
    logger: logging.Logger,
    runner: ProcessRunner,
    secrets: dict[str, str],
    masker: SecretMasker,
    codex_login: CodexLogin | None = None,
) -> StewardRunner | None:
    """The tenant's steward, once the supervisor has its own credential (`login`); None means the per-slot sign-in path."""
    supervisor_state = layout.supervisor_dir / ".slop-haus"
    if not (supervisor_state / CREDENTIALS).is_file():
        return None
    api = SupervisorApi(Transport(config.server), SlotToken(supervisor_state / "token.sh", runner).get)
    server = ServerSteward(api)
    sign_in = Provisioned(layout.root / "steward", Kind.STEWARD, runner, logger, StewardEnrollment(api, config.server), masker=masker)
    steward_config = StewardConfig()
    store = sign_in.harness_config()

    def build(harness: str, review_roots: Sequence[Path]) -> Harness:
        return build_steward_harness(
            layout.root, config.server, steward_config, secrets, masker, sign_in.token_script, harness,
            review_roots, forbidden=steward_forbidden(layout),
            codex_login=codex_login,
        )

    selector = StewardHarnessSelector(
        store, build, lambda: slot_checkouts(layout), BoardStewardStatus(api, logger), logger, masker
    )
    return StewardRunner(
        selector,
        server,
        server,
        server,
        server,
        SystemClock(),
        scratch_dir(layout.root),
        logger,
        steward_config,
        identity=sign_in,
        masker=masker,
        modes=server,
        variants=StewardHarnessVariant(store, logger, lambda: selector.name),
    )


def _signed_in(layout: Layout) -> bool:
    return (layout.supervisor_dir / ".slop-haus" / CREDENTIALS).is_file()


class _ServerRetiresOnReport:
    """The server ends a removed Remote Control agent's grants when the supervisor reports it retired (and at the owner's write).

    So there is nothing to ask beforehand: the reconciler's `retired` report, sent once the host has drained and its
    directory is dealt with, is the request.
    """

    def retire_agent(self, name: str) -> None:
        return None


class _PerHostWorkspace:
    """Stands in the shared deps; `RemoteControlSlots` swaps in each host's own workspace."""

    def ensure(self) -> None:
        raise AssertionError("a host slot is built with its own workspace")

    def refresh(self) -> None:
        raise AssertionError("a host slot is built with its own workspace")


class _LateReconciler:
    """The scheduler is built with this and the reconciler (which needs the scheduler) is set after."""

    def __init__(self) -> None:
        self.target: Reconciler | None = None

    def tick(self) -> None:
        if self.target is not None:
            self.target.tick()


def build_workspace_reconciler(api: SupervisorApi, runner: ProcessRunner, logger: logging.Logger) -> WorkspaceReconciler | None:
    """Slots' checkouts follow the manifest only while the owner's `workspace_reconcile` is on; unreadable means off.

    Read once at start, so turning it on or off takes a supervisor restart.
    """
    try:
        on = api.workspace_reconcile()
    except Exception as err:  # noqa: BLE001 -- an unreadable flag must not stop the supervisor, and reads as off
        logger.warning("workspace_reconcile not read, so off: %s", type(err).__name__)
        return None
    if not on:
        return None
    return reconciler_for(api, runner, logger)


def reconciler_for(
    api: SupervisorApi, runner: ProcessRunner, logger: logging.Logger, removal_confirmed: bool = False
) -> WorkspaceReconciler:
    # Nothing is removed until the owner confirms the dry run's preview; that confirmation is not wired yet.
    settings = ReconcileSettings(destructive=True, removal_confirmed=removal_confirmed)
    return WorkspaceReconciler(ApiManifestSource(api, logger), runner, GitSlotRepo(runner), logger, settings=lambda: settings)


def build(
    layout: Layout,
    config: SupervisorConfig,
    logger: logging.Logger,
    stop: threading.Event,
    scheduler_config: SchedulerConfig = SchedulerConfig(),
    restart_requested: threading.Event | None = None,
) -> Scheduler:
    """`restart_requested` is set when the server asks for a newer supervisor; the stop event is set with it."""
    signed_in = _signed_in(layout)
    # A signed-in host can be filled from the board, so it may start empty.
    if not config.slots and not signed_in:
        raise cfg.ConfigError("no slots: add one with `slop-haus-supervisor slot add`")
    slot_maskers: dict[str, SecretMasker] = {}
    secrets = {name: value for name, value in LayeredEnv(layout, AgentKind.WORKER).values(EnvKind.SECRETS).items() if value}
    install_repo_env(layout, (layout.slot(name) for name in config.slots))
    masker = SecretMasker([*secrets.values(), *known_values(layout)])

    codex_guard = CodexSlotGuard(os.environ, logger)
    # The owner's flag says several Codex slots share the login, so they share its one locked refresher too.
    codex_login = CodexLogin(layout.root / "harness" / "codex", SystemClock(), logger) if codex_guard.verified else None

    def new_harness(name: str) -> ConfiguredHarness:
        try:
            return build_harness(
                HarnessKind.SLOT, name, HarnessContext(
                    layout.root,
                    config.server,
                    secrets,
                    agent_env=agent_env(
                        layout, maskers=lambda agent: [masker, *([slot_maskers[agent]] if agent in slot_maskers else [])], warn=logger.warning
                    ),
                    masker=masker,
                    pi_extension=_pi_extension(layout),
                    codex_login=codex_login,
                ),
                default_variant=config.variant,
            )
        except UnsupportedHarness as err:
            raise cfg.ConfigError(str(err)) from err

    harness = new_harness(config.harness)
    # Which harness each worker slot runs; a slot not in here runs the supervisor's default.
    slot_harnesses: dict[str, str] = {}

    def harness_of(name: str) -> str:
        return slot_harnesses.get(name, harness.name)

    def check_codex_slots(count: int) -> None:
        if not codex_guard.allows(count):
            raise cfg.ConfigError(f"more than one Codex slot needs {CODEX_SHARED_LOGIN}=verified in run.env")

    def check_ready(built: ConfiguredHarness) -> None:
        missing = built.missing_credentials()
        if missing:
            raise cfg.ConfigError(f"set {missing} in {layout.secrets_file}")
        try:
            built.check_installed()
        except FileNotFoundError as err:
            raise cfg.ConfigError(str(err)) from err
        entry = registry.find(built.name)
        if entry is not None and entry.unattended_warning:
            logger.warning(entry.unattended_warning)

    if harness.name == "codex":
        check_codex_slots(len(config.slots))
    check_ready(harness)

    runner = SubprocessRunner()
    tokens = {name: SlotToken(layout.slot(name) / ".slop-haus" / "token.sh", runner) for name in config.slots}
    def on_restart() -> None:
        if restart_requested is not None:
            restart_requested.set()
        stop.set()

    # A harness is stateless between runs and a client keeps its state per agent, so one of each serves every slot on that harness.
    pool: dict[str, SlotDeps] = {}

    def make_client(harness_name: str) -> HttpSlopHaus:
        return HttpSlopHaus(
            config.server,
            tokens,
            harness_name,
            logger,
            version=_version(),
            harness_version=detect_version(harness_name),
            default_variant=config.variant,
            pause=stop.wait,
            on_restart=on_restart,
        )

    reconciler = None
    if signed_in:
        supervisor_api = SupervisorApi(Transport(config.server), SlotToken(layout.supervisor_dir / ".slop-haus" / "token.sh", runner).get)
        reconciler = build_workspace_reconciler(supervisor_api, runner, logger)

    reviewer = build_reviewer(layout, config, os.environ, logger, runner, codex_login)
    reviewer.clear_leftovers()
    if reviewer.missing():
        logger.info("reviews: none on this host (%s); an agent that asks for one is told so", reviewer.missing())

    def deps_for(harness_name: str) -> SlotDeps:
        if harness_name not in pool:
            built = harness if harness_name == harness.name else new_harness(harness_name)
            if built is not harness:
                check_ready(built)
            pool[harness_name] = SlotDeps(
                make_client(harness_name), built, GitSlotRepo(runner), SystemClock(), LogAlerts(logger), PolicyConfig(),
                SlotBriefing(runner, logger, reconciler=reconciler), logger, masker, reviewer=reviewer,
            )
        return pool[harness_name]

    client = deps_for(harness.name).client

    def make_slot(name: str, harness_name: str) -> Slot:
        deps_for(harness_name)
        # The slot is not in the scheduler yet (a switch drained it first), so it is not among the users counted.
        if harness_name == "codex":
            check_codex_slots(codex_users() + 1)
        slot_harnesses[name] = harness_name
        host_harnesses.pop(name, None)
        tokens[name] = SlotToken(layout.slot(name) / ".slop-haus" / "token.sh", runner)
        # Its values are the host's, already in the masker from the build.
        install_repo_env(layout, [layout.slot(name)])
        return Slot(name, layout.slot(name), slot_deps(name))

    def variant_of(name: str) -> str:
        """The board's harness config for the harness this slot runs, else supervisor.toml's variant when that is the harness it was written for."""
        harness_name = harness_of(name)
        store = HarnessConfigStore(layout.slot(name), config.server, tokens[name].get, logger)
        board = store.refresh()
        if board is not None and board.harness and board.harness != harness_name:
            logger.warning("%s: the board's harness config is for %s but the slot runs %s; the desired state wins", name, board.harness, harness_name)
        from_board = board.variant(harness_name) if board is not None else ""
        return from_board or (config.variant if harness_name == harness.name else "")

    def slot_deps(name: str) -> SlotDeps:
        slot_masker = SecretMasker(LayeredEnv(layout, AgentKind.WORKER, name).mask_values())
        slot_maskers[name] = slot_masker
        return replace(deps_for(harness_of(name)), masker=slot_masker, variant_of=variant_of)

    # A host's harness is its own, not the workers'; hosts of this supervisor that are still in the scheduler are users.
    host_harnesses: dict[str, str] = {}

    def codex_users() -> int:
        names = scheduler.slot_names()
        workers = sum(1 for name in names - set(host_harnesses) if harness_of(name) == "codex")
        return workers + sum(1 for name in names if host_harnesses.get(name) == "codex")

    host_slots = RemoteControlSlots(
        RemoteControlDeps(
            layout, _PerHostWorkspace(), SubprocessSpawner(), SystemClock(), LogAlerts(logger), missing_login(layout.root, config.server, _pi_extension(layout)),
            os.environ, logger, masker,
        ),
        lambda name: ProvisionedHost(
            Provisioned(
                layout.remote_control_workspace(name), Kind.REMOTE_CONTROL, runner, logger, RemoteControlEnrollment(api, config.server, name), masker=masker
            ),
            clone_protocol_chooser(layout),
        ),
        codex_guard,
        codex_users,
    )

    def make_host(name: str, host_harness: str) -> Steppable:
        host = host_slots.build(Kind.REMOTE_CONTROL, LAUNCH_SUPERVISOR, name, host_harness)
        assert host is not None
        host_harnesses[name] = host_harness
        # The scheduler reports every slot's state, a host's included, with that agent's own credential.
        tokens[name] = SlotToken(layout.remote_control_workspace(name) / ".slop-haus" / "token.sh", runner)
        return host

    slots = [Slot(name, layout.slot(name), slot_deps(name)) for name in config.slots]
    late = _LateReconciler() if signed_in else None
    scheduler = Scheduler(
        slots,
        stop,
        logger,
        scheduler_config,
        reporter=SlotStateReporter(client, logger),
        control=ControlSpool(layout.control_dir),
        paused_file=layout.paused_file,
        steward=build_steward(layout, config, logger, runner, secrets, masker, codex_login),
        reconciler=late,
    )
    if late is not None:
        api = SupervisorApi(Transport(config.server), SlotToken(layout.supervisor_dir / ".slop-haus" / "token.sh", runner).get)
        late.target = Reconciler(
            layout,
            api,
            SchedulerHost(scheduler, make_slot, make_host, lambda name: host_harnesses.get(name) or harness_of(name)),
            GitSlotRepo(runner),
            ResumeLedger(layout.state_dir / "reconcile.json"),
            logger,
            time.monotonic,
            host_retirement=HostRetirement(layout, _ServerRetiresOnReport(), GitSlotRepo(runner), logger),
            # run_env.apply has already run, so this process's PATH is the service's.
            tools=ToolChecker(shutil.which, login_shell_which(runner, Path.home())),
            extra_tools=lambda: read_tool_list(layout.root / "slot-tools.txt"),
        )
    return scheduler
