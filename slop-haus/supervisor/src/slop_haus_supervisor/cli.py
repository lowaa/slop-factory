from __future__ import annotations

import argparse
import getpass
import os
import shutil
import subprocess
import sys
import tempfile
import time
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.base import ConfiguredHarness
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, build_harness
from slop_haus_supervisor.harness.login import HarnessLogins, TerminalCommands
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.launcher import multiplexer
from slop_haus_supervisor.launcher.multiplexer import LauncherError
from slop_haus_supervisor.launcher.supervisor_session import DEFAULT_IDLE_DEADLINE, DEFAULT_STOP_WAIT, SupervisorSession, host_session_permits_start
from slop_haus_supervisor.setup.guided import GuidedSetup, SetupOptions
from slop_haus_supervisor.setup.grant import REVOKED_ADVICE, GrantState, SupervisorGrant, is_revoked
from slop_haus_supervisor.setup.host_actions import HostActions
from slop_haus_supervisor.setup.reissue import reissue_slot_credentials
from slop_haus_supervisor.setup.prerequisites import Prerequisites
from slop_haus_supervisor.setup.prompts import TerminalPrompts
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.launcher.board_view import BoardView, lines as board_lines
from slop_haus_supervisor.service import log_setup
from slop_haus_supervisor.service.control import ControlSpool, ControlTimeout
from slop_haus_supervisor.service.exit_codes import ExitCode
from slop_haus_supervisor.service.installer import InstallOptions, ServiceInstaller, SessionHost
from slop_haus_supervisor.service.service_path import ServicePath, prepend_to_run_env
from slop_haus_supervisor.service.service_units import Launchd, ServiceManager, SystemdUser
from slop_haus_supervisor.service.serve import ServeOptions, serve
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import layered_env as lay_env
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace import package
from slop_haus_supervisor.slophaus.credentials import CREDENTIALS, device_sign_in, write_credentials
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.token import SlotToken, TokenUnavailable
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.workspace.handover import GitSlotRepo
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.layout import Layout, SlotService
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.workspace import rebuild, status, wrapper
from slop_haus_supervisor.workspace.git_access import GitAccess
from slop_haus_supervisor.workspace.names import SlotNamer, short_hostname
from slop_haus_supervisor.workspace.root import (
    ROOT_ENV,
    RootError,
    RootSources,
    find_root,
    host_roots,
    resolve_root,
    set_default_tenant,
)


DEFAULT_SLOT_CAP = 4
# Names the server may refuse when it holds one this host cannot see (an archived slot keeps its name).
NAME_ATTEMPTS = 50


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="slop-haus-supervisor")
    sub = parser.add_subparsers(dest="command", required=True)

    def where(p: argparse.ArgumentParser) -> None:
        p.add_argument("--root", help="the tenant's directory, instead of looking for it")
        p.add_argument(
            "-t", "--tenant",
            help="the tenant's short name. Without it the root is: SLOP_HAUS_HOME, the root the current directory is in, "
            "the only root on this host, or the default set by `use`",
        )  # fmt: skip

    init = sub.add_parser("init", help="create or refresh a root; run by the setup script")
    where(init)
    init.add_argument("--server", required=True)
    init.add_argument("--tenant-id", required=True)
    init.add_argument(
        "--default-tenant",
        help="the name the server suggests; used unless --tenant is given, and made `NAME-2` and so on if that "
        "folder already belongs to a different tenant",
    )
    init.add_argument("--package", required=True, type=Path, help="the served package archive")
    init.add_argument("--harness", default="claude", choices=lay.HARNESSES)
    init.add_argument("--variant", default="")

    run = sub.add_parser(
        "run",
        help="run the supervisor in the foreground; exit codes: 0 stopped on request, 3 restart wanted (new version), "
        "75 another run holds the tenant, 78 configuration error, other non-zero a failure",
    )
    where(run)
    run.add_argument(
        "--drain-timeout", type=float, default=ServeOptions.drain_timeout, metavar="SECONDS",
        help="on SIGTERM/SIGINT, how long a running slot may reach a wait point before its run is ended",
    )  # fmt: skip
    run.add_argument(
        "--log-file", type=Path,
        help="also write the log to this size-rotated file (default: <root>/supervisor.log; stdout always gets it)",
    )  # fmt: skip
    run.add_argument("--no-log-file", action="store_true", help="write no log file, only stdout")
    run.add_argument("--log-file-max-bytes", type=int, default=log_setup.DEFAULT_LOG_BYTES)
    run.add_argument("--skip-push-check", action="store_true", help="do not check at start that git can push without a prompt")
    where(sub.add_parser("update", help="re-fetch the package from the server and swap <root>/app"))

    slot = sub.add_parser("slot", help="manage worker slots").add_subparsers(dest="slot_command", required=True)
    add = slot.add_parser("add", help="create a slot and sign its agent in")
    where(add)
    add.add_argument("name", nargs="?", help="default: <short hostname>-<n>, the number one past the highest in use")
    add.add_argument("--count", type=int, default=1, metavar="N", help="how many slots to add (default names only)")
    add.add_argument(
        "--setup-script",
        type=Path,
        help="the agent's setup script, from registering the agent on the board's Agents screen; "
        "not needed once `login` has signed the supervisor in, which creates the slot's agent itself",
    )
    unpause = slot.add_parser(
        "unpause",
        help="ask the running supervisor to unpause a slot; it checks the cause first and says why if it is still there",
    )
    where(unpause)
    unpause.add_argument("name", nargs="?")
    unpause.add_argument("--all", action="store_true", help="every paused slot")
    unpause.add_argument("--timeout", type=float, default=30.0, metavar="SECONDS", help="how long to wait for the supervisor")
    reissue = slot.add_parser(
        "reissue", help="give slots new credentials, after `login` replaced a revoked supervisor sign-in (the supervisor must be signed in)"
    )
    where(reissue)
    reissue.add_argument("name", nargs="?")
    reissue.add_argument("--all", action="store_true", help="every slot")
    remove = slot.add_parser("remove", help="retire a slot's agent and delete its directory, unless it holds unpushed work")
    where(remove)
    remove.add_argument("name")

    rebuild = slot.add_parser(
        "rebuild",
        help="re-provision a slot in the manifest's current layout, keeping its agent and sign-in; the supervisor must be stopped",
    )
    where(rebuild)
    rebuild.add_argument("name", nargs="?")
    rebuild.add_argument("--all", action="store_true", help="every slot")
    rebuild.add_argument("--keep-old", action="store_true", help="keep each old directory as <name>.pre-rebuild-<time>")

    login = sub.add_parser(
        "login", help="sign the supervisor in, so `slot add` can create slots without a sign-in each; approved in the browser"
    )
    where(login)
    login.add_argument("--slots", type=int, default=DEFAULT_SLOT_CAP, metavar="N", help="how many slots to ask to be allowed")

    def layered(name: str, help_: str) -> None:
        command = sub.add_parser(name, help=help_).add_subparsers(dest=f"{name}_command", required=True)
        for verb, verb_help in (
            ("set", "set KEY; the value is read from a hidden prompt, or stdin when piped"),
            ("unset", "remove KEY from the layer"),
            ("list", "print the key names and the layer each comes from (never the values)"),
            ("mask", "withhold base KEY from Remote Control slots (--for remote-control only)"),
            ("unmask", "stop withholding base KEY from Remote Control slots (--for remote-control only)"),
        ):
            verb_parser = command.add_parser(verb, help=verb_help)
            where(verb_parser)
            if verb in ("set", "unset", "mask", "unmask"):
                verb_parser.add_argument("key")
            layer = verb_parser.add_mutually_exclusive_group()
            layer.add_argument("--for", dest="for_kind", choices=[lay_env.Layer.KIND.value], help="the Remote Control layer, instead of the base")
            layer.add_argument("--agent", metavar="NAME", help="that agent's layer; `list` shows what a worker of that name gets")

    layered("secret", "manage <root>/secrets.env, the worker env vars and login, and its Remote Control and per-agent layers; local only, never sent to the server")
    layered("env", "manage <root>/run.env, the same way")

    setup = sub.add_parser(
        "setup",
        help="one guided pass: check the host, sign in, store the harness login token, add slots, say how to run; safe to re-run",
    )
    where(setup)
    setup.add_argument("--slots", type=int, metavar="N", help="how many slots, instead of asking (default 3)")

    harness = sub.add_parser("harness", help="log the harnesses in on this host").add_subparsers(dest="harness_command", required=True)
    harness_login = harness.add_parser(
        "login", help="run a harness's own login with its folder under the root, then say what is left (restart, unpause)"
    )
    where(harness_login)
    harness_login.add_argument("name", choices=registry.NAMES)
    where(harness.add_parser("status", help="per harness: logged in or missing, and whether any slot uses it"))

    start = sub.add_parser("start", help="run the supervisor detached in a screen (or tmux) session, logging to <root>/supervisor.log")
    where(start)
    start.add_argument(
        "--allow-host-session",
        action="store_true",
        help="start even from inside an agent session, whose end kills the supervisor with it",
    )
    stop = sub.add_parser("stop", help="drain the running supervisor (no new work) and wait for it to stop")
    where(stop)
    stop.add_argument("--now", action="store_true", help="the second signal: end the runs at once")
    stop.add_argument("--timeout", type=float, default=DEFAULT_STOP_WAIT, metavar="SECONDS", help="how long to wait for the drain")
    restart = sub.add_parser("restart", help="stop the supervisor (draining) and start it again")
    where(restart)
    restart.add_argument("--allow-host-session", action="store_true", help="as for start")
    restart.add_argument(
        "--when-idle",
        action="store_true",
        help="first wait for every Remote Control host to be between sessions, so no conversation is cut off; "
        "gives up after --idle-deadline and leaves the supervisor running. The way to update",
    )
    restart.add_argument("--idle-deadline", type=float, default=DEFAULT_IDLE_DEADLINE, metavar="SECONDS", help="how long --when-idle waits")
    restart.add_argument("--timeout", type=float, default=DEFAULT_STOP_WAIT, metavar="SECONDS", help="how long to wait for the drain")
    logs = sub.add_parser("logs", help="follow the supervisor's log")
    where(logs)
    logs.add_argument("-n", "--lines", type=int, default=50, metavar="N", help="how many lines to start from (default 50)")
    where(sub.add_parser("attach", help="attach to the supervisor's screen or tmux session"))

    svc = sub.add_parser("service", help="run the supervisor under the host's service manager (systemd user unit, launchd agent)")
    svc_sub = svc.add_subparsers(dest="service_command", required=True)
    install = svc_sub.add_parser(
        "install",
        help="write the unit for this tenant, hand a running supervisor over to it, start it and follow the log until the slots are up; safe to re-run",
    )
    where(install)
    install.add_argument("--when-idle", action="store_true", help="wait for slots mid-ticket to finish before draining a running supervisor")
    install.add_argument("--idle-deadline", type=float, default=DEFAULT_IDLE_DEADLINE, metavar="SECONDS", help="how long --when-idle waits")
    install.add_argument("--yes", action="store_true", help="drain a running supervisor without asking, even with slots mid-ticket")
    where(svc_sub.add_parser("uninstall", help="stop the service and remove its unit"))

    use = sub.add_parser("use", help="make a tenant the default when several are set up on this host")
    use.add_argument("tenant")
    status_ = sub.add_parser("status", help="each tenant's supervisor on this host, running or not, and its slots")
    where(status_)
    return parser


def _secret_value(key: str) -> str:
    if sys.stdin.isatty():
        return getpass.getpass(f"value for {key}: ")
    return sys.stdin.readline().rstrip("\r\n")


def _layer_ref(args: argparse.Namespace) -> lay_env.LayerRef:
    if args.for_kind:
        return lay_env.LayerRef(lay_env.Layer.KIND)
    if args.agent:
        cfg.check_name("agent", args.agent)
        return lay_env.LayerRef(lay_env.Layer.AGENT, args.agent)
    return lay_env.LayerRef(lay_env.Layer.BASE)


def _layered(args: argparse.Namespace, layout: Layout, command: str, file: lay_env.EnvKind) -> None:
    if not layout.root.is_dir():
        raise cfg.ConfigError(f"{layout.root} does not exist: run the setup script first")
    ref = _layer_ref(args)
    target = lay_env.LayerFile(layout, ref, file)
    verb = getattr(args, f"{command}_command")
    match verb:
        case "set":
            value = _secret_value(args.key)
            if not value:
                raise cfg.ConfigError("no value given; use `unset` to remove a key")
            if file is lay_env.EnvKind.SECRETS and len(value) < SecretMasker.MIN_LENGTH:
                raise cfg.ConfigError(f"a value under {SecretMasker.MIN_LENGTH} characters could not be masked in output")
            target.set(args.key, value)
            print(f"set {args.key}; restart the supervisor for runs to see it")
        case "unset":
            print(f"removed {args.key}" if target.unset(args.key) else f"{args.key} was not set")
        case "mask":
            target.mask(args.key)
            print(f"{args.key} is withheld from Remote Control slots; restart the supervisor for it to apply")
        case "unmask":
            print(f"{args.key} is no longer withheld" if target.unmask(args.key) else f"{args.key} was not masked")
        case "list":
            for item in lay_env.listing(layout, ref, file):
                if ref.layer is lay_env.Layer.BASE:
                    print(f"{item.name} (empty)" if item.note else item.name)
                else:
                    print(f"{item.name} [{item.layer.value}]" + (f" ({item.note})" if item.note else ""))
        case _:
            raise AssertionError(f"unhandled {command} command {verb}")


def locate(args: argparse.Namespace, sources: RootSources, cwd: Path) -> Layout:
    tenant: str = args.tenant or ""
    if tenant:
        cfg.check_name("tenant", tenant)
    return Layout(find_root(sources, cwd, args.root, tenant))


def log_file(args: argparse.Namespace, layout: Layout) -> Path | None:
    """Where `run` writes its rotated log: an explicit path, else <root>/supervisor.log, unless opted out."""
    if args.no_log_file:
        if args.log_file:
            raise cfg.ConfigError("--log-file and --no-log-file contradict each other")
        return None
    if args.log_file:
        return args.log_file
    # A root that was never set up has nowhere to write; serve() reports that with the right message.
    return layout.root / "supervisor.log" if layout.root.is_dir() else None


def _init_tenant(args: argparse.Namespace, sources: RootSources) -> str:
    """The tenant name to set up: --tenant, else the server's suggestion made unique among the roots on this host."""
    if args.tenant or not args.default_tenant:
        return args.tenant or ""
    name = cfg.check_name("tenant", args.default_tenant)
    candidate, number = name, 1
    env_root = sources.env.get(ROOT_ENV, "")
    # A fixed root, like --root, does not follow the name; a clash there is set_up's to refuse.
    while not (args.root or (env_root and "{tenant}" not in env_root)):
        config_file = resolve_root(candidate, sources) / "supervisor.toml"
        # Another tenant's folder is left exactly as it is: its slots and credentials are its own.
        if not config_file.exists() or cfg.load(config_file).tenant_id == args.tenant_id:
            break
        number += 1
        candidate = f"{name}-{number}"
        print(f"{config_file.parent} belongs to a different tenant; using {candidate}")
    return candidate


def _token(layout: Layout, config: SupervisorConfig) -> str:
    if not config.slots:
        raise cfg.ConfigError("no slot has signed in yet, and the update is fetched as a slot's agent -- add one first")
    helper = layout.slot(config.slots[0]) / ".slop-haus" / "token.sh"
    done = subprocess.run([str(helper)], capture_output=True, text=True, check=False)
    if done.returncode != 0 or not done.stdout.strip():
        raise cfg.ConfigError(f"{helper} gave no token: {done.stderr.strip()}")
    return done.stdout.strip()


def _update(layout: Layout) -> None:
    config = cfg.load(layout.config_file)
    token = _token(layout, config)
    with tempfile.TemporaryDirectory(dir=layout.root) as tmp:
        archive = Path(tmp) / "package.tar.gz"
        package.fetch(config.server, token, archive)
        package.install(archive, layout.root)
    print(f"updated {layout.app}")


def _service(layout: Layout, config: SupervisorConfig) -> SupervisorApi | None:
    """The supervisor's own credential, once `login` has stored one; None means the per-slot sign-in path."""
    if not (layout.supervisor_dir / ".slop-haus" / CREDENTIALS).is_file():
        return None
    helper = layout.supervisor_dir / ".slop-haus" / "token.sh"
    token = SlotToken(helper, SubprocessRunner())
    return SupervisorApi(Transport(config.server), token.get)


def _login(layout: Layout, slots: int) -> None:
    if slots < 1:
        raise cfg.ConfigError("--slots must be at least 1")
    config = cfg.load(layout.config_file)
    # Constructed first: it refuses a server that is not https before anything is sent to it.
    transport = Transport(config.server)
    tokens = device_sign_in(config.server, {"kind": "supervisor", "slots": str(slots)}, print)
    here = layout.supervisor_dir
    made = not here.exists()
    here.mkdir(exist_ok=True)
    try:
        write_credentials(here, config.server, tokens)
        # The supervisor has no repos of its own; keep the update from cloning the manifest into it.
        (here / ".slop-haus" / "no-clone").touch()
        (here / ".slop-haus.env").write_text("SLOP_HAUS_AGENT=''\n")
        # token.sh, the refresh logic, arrives with the workspace files, as it does for a slot.
        script = SupervisorApi(transport, lambda: tokens.access_token).update_script(tokens)
        with tempfile.TemporaryDirectory() as scratch:
            path = Path(scratch) / "update.sh"
            path.write_text(script)
            if lay.run_interactive(["bash", str(path)], here) != 0:
                raise cfg.ConfigError(f"installing the supervisor's token helper failed in {here}")
    except BaseException:
        # A credential without token.sh would read as signed in, and then fail every slot add.
        if made:
            shutil.rmtree(here, ignore_errors=True)
        raise
    print(f"signed in; slot add now creates slots through the supervisor (up to {slots} if the approver allowed it)")


def _init(args: argparse.Namespace, sources: RootSources) -> Layout:
    tenant = _init_tenant(args, sources)
    if not (args.root or tenant or sources.env.get("SLOP_HAUS_HOME")):
        raise RootError("say which tenant: --tenant NAME or --root DIR (or set SLOP_HAUS_HOME)")
    if tenant:
        cfg.check_name("tenant", tenant)
    layout = Layout(resolve_root(tenant, sources, args.root))
    existing = cfg.load(layout.config_file) if layout.config_file.exists() else None
    config = SupervisorConfig(
        args.server.rstrip("/"), args.tenant_id, tenant or cfg.check_name("tenant", layout.root.name), args.harness, args.variant
    )
    lay.set_up(layout, config, args.package)
    if existing and existing.clone_protocol:
        # set_up writes the config afresh from the setup script's values.
        cfg.write(layout.config_file, cfg.load(layout.config_file).with_clone_protocol(existing.clone_protocol))
    print(f"set up {layout.root}")
    return layout


class SlotLister(SlotService, Protocol):
    def taken_names(self) -> set[str]: ...


def _board_workers_url(layout: Layout, service: SupervisorApi | None) -> str | None:
    """Where agents are added when the board provisions them for this tenant, else None.

    A host that has not signed the supervisor in cannot be reconciled, so the board does not manage it and there is
    nothing to ask. A server that cannot be asked is an error: acting locally on a guess is what the guards on this
    answer prevent.
    """
    if service is None:
        return None
    server = cfg.load(layout.config_file).server
    try:
        on = service.provision_from_board()
    except (ApiError, OSError, TokenUnavailable, ValueError) as err:
        raise cfg.ConfigError(f"could not check whether this tenant provisions agents from the board, so nothing was changed: {err}") from err
    return f"{server.rstrip('/')}/workers" if on else None


def _refuse_while_board_provisions(layout: Layout, service: SupervisorApi | None) -> None:
    """The reconciler makes the host match the board, so a slot added, unpaused or removed here would be undone."""
    url = _board_workers_url(layout, service)
    if url is not None:
        raise cfg.ConfigError(
            f"this tenant provisions agents from the board: add, unpause or remove the agent at {url}"
            " (a slot already on this host can be imported and managed there)"
        )


def _slot_add(args: argparse.Namespace, layout: Layout, service: SlotLister | None, env: Mapping[str, str]) -> None:
    run = GitAccess(env)
    if args.count < 1:
        raise cfg.ConfigError("--count must be at least 1")
    if args.name:
        if args.count != 1:
            raise cfg.ConfigError("--count goes with the default names; give either a name or --count")
        lay.add_slot(layout, args.name, args.setup_script, run=run, service=service)
        print(f"slot {args.name} is ready in {layout.slot(args.name)}")
        return
    if service is None and args.setup_script is not None:
        raise cfg.ConfigError("a setup script is for one named slot: give the slot's name")
    config = cfg.load(layout.config_file)
    taken = {*config.slots, *(d.name for d in layout.slots.iterdir())} if layout.slots.is_dir() else set(config.slots)
    if service is not None:
        taken.update(service.taken_names())
    namer = SlotNamer(short_hostname(), taken)
    for _ in range(args.count):
        for _attempt in range(NAME_ATTEMPTS):
            name = namer.next()
            try:
                lay.add_slot(layout, name, args.setup_script, run=run, service=service)
            except ApiError as err:
                if err.status == 409 and ("already exists" in str(err) or "holds that name" in str(err)):
                    continue
                raise
            print(f"slot {name} is ready in {layout.slot(name)}")
            break
        else:
            raise cfg.ConfigError(f"could not find a free slot name after {NAME_ATTEMPTS} tries")


def _slot_rebuild(args: argparse.Namespace, layout: Layout, service: SlotService | None, env: Mapping[str, str]) -> int:
    if bool(args.name) == bool(args.all):
        raise cfg.ConfigError("give a slot's name, or --all")
    config = cfg.load(layout.config_file)
    names = list(config.slots) if args.all else [args.name]
    runner = SubprocessRunner()
    failed: list[str] = []
    for name in names:
        try:
            done = rebuild.rebuild_slot(
                layout, name, service, GitSlotRepo(runner), rebuild.ServerSlotProbe(config.server, runner), GitAccess(env),
                keep_old=args.keep_old,
            )
        except (cfg.ConfigError, ApiError) as err:
            # One slot's refusal must not hide the others' outcomes; a lone slot's error is the whole answer.
            if not args.all:
                raise
            print(f"slot {name} NOT rebuilt: {err}", file=sys.stderr)
            failed.append(name)
            continue
        if not done.changed:
            print(f"slot {name}: {done.note}")
        else:
            print(f"slot {name} rebuilt" + (f"; the old directory is {done.kept}" if done.kept else ""))
    if failed:
        print(f"slop-haus-supervisor: {len(failed)} of {len(names)} slots not rebuilt: {', '.join(failed)}", file=sys.stderr)
    return 1 if failed else 0


def _slot_unpause(args: argparse.Namespace, layout: Layout, service: SupervisorApi | None) -> int:
    if bool(args.name) == bool(args.all):
        raise cfg.ConfigError("give a slot's name, or --all")
    _refuse_while_board_provisions(layout, service)
    running, _ = status.holder(layout.root / ".run.lock")
    if not running:
        raise cfg.ConfigError("the supervisor is not running; a slot starts unpaused when it does")
    try:
        answers = ControlSpool(layout.control_dir).request_unpause(None if args.all else [args.name], args.timeout)
    except ControlTimeout as err:
        raise cfg.ConfigError(str(err)) from err
    refused = False
    for name, why in sorted(answers.items()):
        print(f"{name}: {'unpaused' if not why else 'still paused: ' + why}")
        refused = refused or bool(why)
    return 1 if refused else 0


def _session(layout: Layout, *, writable: bool) -> SupervisorSession:
    config = cfg.load(layout.config_file)
    runner = SubprocessRunner()
    # `status` only reads, so a host with neither screen nor tmux can still ask.
    mux = multiplexer.choose(runner) if writable else multiplexer.choose_or_none(runner)
    return SupervisorSession(layout, config.tenant, mux)


def _launch_session(layout: Layout, sources: RootSources, *, writable: bool) -> SupervisorSession:
    """The session `start`/`stop`/`restart` drive: through the service unit once one is installed, else the multiplexer."""
    config = cfg.load(layout.config_file)
    try:
        manager = _service_manager(layout, config.tenant, sources.home)
    except cfg.ConfigError:
        return _session(layout, writable=writable)
    if not manager.unit_path.exists():
        return _session(layout, writable=writable)
    # The unit launches it, so a host with neither screen nor tmux can still start and stop.
    mux = multiplexer.choose_or_none(SubprocessRunner())
    return SupervisorSession(layout, config.tenant, mux, unit=manager)


def _service_which(layout: Layout, sources: RootSources) -> Callable[[str], str | None]:
    """Finds a tool where a service manager will look (run.env's PATH, then the manager's), not only in the shell."""
    shell_path = sources.env.get("PATH", os.defpath)
    service_path = ServicePath(layout.root / "run.env", SubprocessRunner(), sources.home)
    return lambda name: service_path.find(name, shell_path)


def _prerequisites(layout: Layout, config: SupervisorConfig, sources: RootSources) -> Prerequisites:
    return Prerequisites(
        SubprocessRunner(), config.harness, sources.env, sources.home, layout.root, service_which=_service_which(layout, sources)
    )


def _service_manager(layout: Layout, tenant: str, home: Path) -> ServiceManager:
    runner = SubprocessRunner()
    if sys.platform.startswith("linux"):
        return SystemdUser(layout, tenant, runner, home, getpass.getuser())
    if sys.platform == "darwin":
        return Launchd(layout, tenant, runner, home, os.getuid())
    raise cfg.ConfigError("no service manager is supported here; on Windows run the Linux setup inside WSL2 (see service/README.md)")


def _service_command(args: argparse.Namespace, layout: Layout, sources: RootSources) -> int:
    config = cfg.load(layout.config_file)
    # The installer drains a supervisor the multiplexer started, so it gets the session without the unit.
    installer = ServiceInstaller(
        _service_manager(layout, config.tenant, sources.home),
        SessionHost(layout, _session(layout, writable=False)),
        _prerequisites(layout, config, sources).check,
        lambda: _service_which(layout, sources)("uv"),
        TerminalPrompts().ask,
    )
    if args.service_command == "uninstall":
        return installer.uninstall()
    return installer.install(InstallOptions(args.when_idle, args.yes, args.idle_deadline))


def _ignore(_result: object) -> None:
    pass


def _setup(args: argparse.Namespace, layout: Layout, sources: RootSources) -> int:
    if not layout.config_file.is_file():
        raise cfg.ConfigError(f"{layout.root} is not set up: run the setup script first")
    if args.slots is not None and args.slots < 1:
        raise cfg.ConfigError("--slots must be at least 1")
    config = cfg.load(layout.config_file)
    prerequisites = _prerequisites(layout, config, sources)

    def add_slots(count: int) -> None:
        service = _service(layout, cfg.load(layout.config_file))
        _refuse_while_board_provisions(layout, service)
        _slot_add(argparse.Namespace(name=None, count=count, setup_script=None), layout, service, sources.env)

    def board_url() -> str | None:
        return _board_workers_url(layout, _service(layout, cfg.load(layout.config_file)))

    def reissue_slots() -> int:
        service = _service(layout, cfg.load(layout.config_file))
        assert service is not None, "reissue runs after a login"
        return reissue_slot_credentials(layout, service)

    def grant() -> GrantState:
        return SupervisorGrant(layout, config.server, SubprocessRunner()).state()

    logins = _harness_logins(layout)
    actions = HostActions(
        layout, prerequisites, lambda slots: _login(layout, slots), add_slots, grant, reissue_slots, board_url, logins,
        lambda folder: _ignore(prepend_to_run_env(layout, folder)),
    )
    try:
        return GuidedSetup(layout, actions, TerminalPrompts(), harness=config.harness).run(SetupOptions(args.slots))
    except KeyboardInterrupt:
        print("\ninterrupted; nothing is left half-made: run `slop-haus setup` again to finish", file=sys.stderr)
        return 130


def _harness_logins(layout: Layout) -> HarnessLogins:
    def build(name: str) -> ConfiguredHarness:
        # Read afresh on each call: a login that stored a secret is judged by what is in the file now.
        secrets = {k: v for k, v in lay_env.LayeredEnv(layout, lay_env.AgentKind.WORKER).values(lay_env.EnvKind.SECRETS).items() if v}
        # pi's extension is not read to judge a login, so its path need not exist.
        context = HarnessContext(layout.root, cfg.load(layout.config_file).server, secrets, pi_extension=layout.root / ".slop-haus" / "pi" / "slop-haus.ts")
        return build_harness(HarnessKind.SLOT, name, context)

    return HarnessLogins(layout, build, TerminalCommands(), TerminalPrompts().ask_hidden)


def _harness(args: argparse.Namespace, layout: Layout) -> int:
    logins = _harness_logins(layout)
    if args.harness_command == "status":
        print("\n".join(logins.status_lines()))
        return 0
    outcome = logins.login(args.name)
    print("\n".join(outcome.lines))
    return 0 if outcome.ok else 1


def _status(args: argparse.Namespace, sources: RootSources, cwd: Path) -> None:
    if args.root or args.tenant:
        roots = [locate(args, sources, cwd).root]
    else:
        roots = list(host_roots(sources).values())
    if not roots:
        raise RootError("no supervisor is set up on this host: run the setup script first")
    for root in roots:
        layout = Layout(root)
        print("\n".join(status.of_root(root).lines()))
        print("\n".join(_harness_logins(layout).status_lines()))
        board = _board_lines(layout)
        # The log scrape is the fallback for a host that has not signed the supervisor in, or a server that cannot be asked.
        print("\n".join(board if board is not None else _session(layout, writable=False).status_lines()))
        if board is not None:
            print("\n".join(_session(layout, writable=False).session_lines()))


def _board_lines(layout: Layout) -> list[str] | None:
    """The board's own view of the supervisors, or None (with the reason on stderr) when it cannot be asked."""
    try:
        config = cfg.load(layout.config_file)
        helper = layout.supervisor_dir / ".slop-haus" / "token.sh"
        if not (layout.supervisor_dir / ".slop-haus" / CREDENTIALS).is_file():
            print("  board: the supervisor is not signed in (`slop-haus login`); showing the log instead", file=sys.stderr)
            return None
        view = BoardView(Transport(config.server), SlotToken(helper, SubprocessRunner()).get)
        return board_lines(view.fetch(), time.time())
    except (OSError, ApiError, cfg.ConfigError, TokenUnavailable, ValueError) as err:
        if is_revoked(err):
            print(f"  board: {REVOKED_ADVICE}; showing the log instead", file=sys.stderr)
            return None
        print(f"  board: could not ask the server ({err}); showing the log instead", file=sys.stderr)
        return None


def _use(tenant: str, sources: RootSources) -> None:
    cfg.check_name("tenant", tenant)
    root = resolve_root(tenant, sources)
    if not (root / "supervisor.toml").is_file():
        known = ", ".join(host_roots(sources)) or "none"
        raise RootError(f"no tenant {tenant!r} is set up at {root} (set up here: {known})")
    set_default_tenant(sources, tenant)
    print(f"{tenant} is now the default tenant")


def main(
    argv: Sequence[str] | None = None, env: Mapping[str, str] | None = None, home: Path | None = None, cwd: Path | None = None
) -> int:
    args = _parser().parse_args(argv)
    sources = RootSources(home or Path.home(), os.environ if env is None else env)
    here = cwd or Path.cwd()
    try:
        match args.command:
            case "use":
                _use(args.tenant, sources)
                return 0
            case "status":
                _status(args, sources, here)
                return 0
            case "init":
                layout = _init(args, sources)
                for note in wrapper.install(sources.home, layout.root, sources.env):
                    print(note)
                return 0
            case _:
                pass
        layout = locate(args, sources, here)
        match args.command:
            case "update":
                _update(layout)
                for note in wrapper.install(sources.home, layout.root, sources.env):
                    print(note)
            case "login":
                _login(layout, args.slots)
            case "slot":
                config = cfg.load(layout.config_file)
                service = _service(layout, config)
                if args.slot_command == "unpause":
                    return _slot_unpause(args, layout, service)
                if args.slot_command == "rebuild":
                    return _slot_rebuild(args, layout, service, sources.env)
                if args.slot_command == "reissue":
                    if bool(args.name) == bool(args.all):
                        raise cfg.ConfigError("give a slot's name, or --all")
                    if service is None:
                        raise cfg.ConfigError("reissue needs the supervisor's sign-in: run `login` first")
                    return 1 if reissue_slot_credentials(layout, service, None if args.all else [args.name]) else 0
                if args.slot_command == "remove":
                    _refuse_while_board_provisions(layout, service)
                    lay.remove_slot(layout, args.name, service, GitSlotRepo(SubprocessRunner()))
                    print(f"slot {args.name} removed")
                else:
                    _refuse_while_board_provisions(layout, service)
                    _slot_add(args, layout, service, sources.env)
            case "secret":
                _layered(args, layout, "secret", lay_env.EnvKind.SECRETS)
            case "env":
                _layered(args, layout, "env", lay_env.EnvKind.RUN)
            case "setup":
                return _setup(args, layout, sources)
            case "harness":
                return _harness(args, layout)
            case "start":
                if not host_session_permits_start(sources.env, args.allow_host_session):
                    return 1
                return _launch_session(layout, sources, writable=True).start()
            case "stop":
                return _launch_session(layout, sources, writable=False).stop(args.now, args.timeout)
            case "restart":
                if not host_session_permits_start(sources.env, args.allow_host_session):
                    return 1
                return _launch_session(layout, sources, writable=True).restart(args.when_idle, args.idle_deadline, args.timeout)
            case "logs":
                return subprocess.call(_session(layout, writable=False).logs_argv(args.lines))
            case "attach":
                return subprocess.call(_session(layout, writable=True).attach_argv())
            case "service":
                return _service_command(args, layout, sources)
            case "run":
                return int(
                    serve(
                        layout,
                        ServeOptions(args.drain_timeout, log_file(args, layout), args.log_file_max_bytes, not args.skip_push_check),
                    )
                )
            case _:
                raise AssertionError(f"unhandled command {args.command}")
    except (cfg.ConfigError, LauncherError, RootError, package.PackageError, EnvFileError, TokenUnavailable) as err:
        print(f"slop-haus-supervisor: {err}", file=sys.stderr)
        return int(ExitCode.CONFIG) if args.command == "run" else 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
