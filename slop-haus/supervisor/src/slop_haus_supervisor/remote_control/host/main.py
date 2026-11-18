from __future__ import annotations

import argparse
import contextlib
import logging
import os
import signal
import sys
import threading
import uuid
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from types import FrameType

from slop_haus_supervisor.harness.codex_login import CodexLogin, CodexSlotGuard
from slop_haus_supervisor.ports import SystemClock
from slop_haus_supervisor.fleet.host_slot import SHARED_LOGIN_ROOT
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.remote_control.host.launch import Applied, ConfigWatch, LaunchRefused, PythonLaunch
from slop_haus_supervisor.remote_control.host.claude import RemoteControlClaude
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex
from slop_haus_supervisor.remote_control.host.codex_records import CodexRecords
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.remote_control.host.attachments import AttachmentStore, ClientDownloader
from slop_haus_supervisor.remote_control.host.host import Host, SessionHarness
from slop_haus_supervisor.remote_control.host.lease import HostLease, LeaseRefused
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.workspace.host_files import IdleMarker
from slop_haus_supervisor.remote_control.host.layout_reporter import LayoutReporter
from slop_haus_supervisor.remote_control.host.poller import Poller
from slop_haus_supervisor.remote_control.host.prompt import SYSTEM_PROMPT
from slop_haus_supervisor.remote_control.host.records import RecordHandler
from slop_haus_supervisor.remote_control.host.settings import Settings
from slop_haus_supervisor.workspace.host_files import HostLaunchRefused, host_lock, refusal, supervisor_lock
from slop_haus_supervisor.remote_control.pi.errors import PiErrors
from slop_haus_supervisor.remote_control.pi.harness import RemoteControlPi
from slop_haus_supervisor.remote_control.pi.options import pi_extension, pi_variant
from slop_haus_supervisor.remote_control.pi.rpc import PiRpc
from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.remote_control.wire.errors import Refused
from slop_haus_supervisor.remote_control.wire.reporter import Reporter
from slop_haus_supervisor.service.run_env import LauncherEnvError
from slop_haus_supervisor.service.instance_lock import AlreadyRunning
from slop_haus_supervisor.slophaus.token import SlotToken
from slop_haus_supervisor.workspace.harness_config import HarnessConfigError
from slop_haus_supervisor.workspace.layout import Layout

log = logging.getLogger(__name__)

# A service never reads ~/.bashrc (it returns early for a non-interactive shell), so a tool that works in a terminal can
# still be missing here. Only the supervisor applies PATH from run.env: a hand-launched host takes its own shell's.
SUPERVISED_NOT_ON_PATH_HINT = (
    " (if it is installed, a service does not read ~/.bashrc: add its directory to PATH in <root>/run.env, "
    "then restart the supervisor)"
)


def read_first_line(path: Path) -> str:
    try:
        with open(path, encoding="utf-8") as f:
            return f.readline().strip()
    except OSError:
        return ""


def parse_args(argv: Sequence[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="slop-haus-remote-control")
    parser.add_argument("--workspace", type=Path, default=Path.cwd(),
                        help="the workspace the sessions run in, set up by slop-haus's setup (default: the current directory)")
    parser.add_argument("--harness", choices=registry.REMOTE_CONTROL_NAMES, default="claude",
                        help="the harness to run the sessions in (default %(default)s)")
    # pi's model flags, from the harness config.
    parser.add_argument("--provider", default="")
    parser.add_argument("--model", default="")
    parser.add_argument("--thinking", default="")
    parser.add_argument("--python-launch", action="store_true",
                        help="do the launcher's own steps here: fetch the harness config, load run.env, check the harness (off by default)")
    parser.add_argument("--supervised", action="store_true",
                        help="the supervisor started this host: its lock says so to a manual launch. Set by the supervisor, not by hand")
    parser.add_argument("--idle-timeout", type=float, default=Settings.idle_timeout,
                        help="stop a harness with nothing to do after this many seconds (default %(default)s)")
    args = parser.parse_args(argv)
    if args.harness != "pi" and (args.provider or args.model or args.thinking):
        parser.error("--provider, --model and --thinking are for --harness pi")
    return args


@dataclass
class ChosenHarness:
    harness: SessionHarness
    # How a record of it is handled; None for Claude Code's.
    records: Callable[[Host], RecordHandler] | None
    # Said when its binary is not on PATH.
    missing: str
    # Said at start-up: what the model can do, and where its login lives.
    banner: str


def shared_login_dir(harness: str, environ: Mapping[str, str]) -> Path | None:
    """The supervisor's login for `harness`, for a host it started; None for a host started by hand, which keeps its own."""
    shared = environ.get(SHARED_LOGIN_ROOT)
    return Layout(Path(shared)).harness_dir(harness) if shared else None


def choose_harness(args: argparse.Namespace, root: Path, state_dir: Path, server: str, token_script: Path,
                   settings: Settings, environ: Mapping[str, str] | None = None, codex_variant: str = "") -> ChosenHarness:
    login_dir = shared_login_dir(args.harness, os.environ if environ is None else environ) if args.supervised else None
    if args.harness == "pi":
        agent = RemoteControlPi(
            state_dir, pi_extension(root), SYSTEM_PROMPT, pi_variant(args.provider, args.model, args.thinking),
            grace=settings.harness_grace, login_dir=login_dir,
        )
        agent_dir = agent.config_dir(state_dir)
        return ChosenHarness(
            agent,
            lambda host: PiRpc(host.reporter, host, PiErrors(agent_dir), agent_dir),
            "pi is not on PATH: install pi (https://pi.dev) first",
            f"pi in {root}, running with permissions bypassed as you (bash, edit and write included), driven from the board "
            f"by you alone. Its login and settings live in {agent_dir} (log in once with PI_CODING_AGENT_DIR={agent_dir} pi "
            "and /login, or set the provider's API key variable).",
        )
    if args.harness == "codex":
        # A supervised host shares the workers' login, so under the owner's flag it too takes a locked, refreshed copy.
        verified = login_dir is not None and CodexSlotGuard(os.environ if environ is None else environ, log).verified
        codex = RemoteControlCodex(
            state_dir, server, SYSTEM_PROMPT, codex_variant, grace=settings.harness_grace, login_dir=login_dir,
            login=CodexLogin(login_dir, SystemClock(), log) if verified and login_dir else None,
        )
        codex_home = codex.config_dir(state_dir)
        return ChosenHarness(
            codex,
            lambda host: CodexRecords(host.reporter, host, codex_home),
            "codex is not on PATH: install Codex first",
            f"codex in {root}, running with its sandbox and approvals off (the model can run any command, edit files, commit and "
            f"push as you), driven from the board by you alone. Its login and settings live in {codex_home} (log in once with "
            f"CODEX_HOME={codex_home} codex login, with ChatGPT).",
        )
    claude = RemoteControlClaude(
        state_dir, server, SYSTEM_PROMPT, token_script=lambda _spec: token_script, grace=settings.harness_grace,
    )
    config_dir = claude.config_dir(state_dir)
    # Under a supervisor the login is its CLAUDE_CODE_OAUTH_TOKEN, which Claude Code prefers to a .credentials.json
    # in this directory; the directory only keeps this host's hooks, settings and transcripts.
    return ChosenHarness(
        claude, None, "claude is not on PATH: install Claude Code first",
        f"the model runs with permissions bypassed, as you, in {root}. It can run any command, edit files, commit and push, "
        f"driven from the board by you alone. Its login and settings live in {config_dir} (log in once with "
        f"CLAUDE_CONFIG_DIR={config_dir} claude, or set CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY).",
    )


def retune(harness: SessionHarness, given: Mapping[str, str], applied: Applied) -> None:
    """pi's model flags and Codex's variant after a change of the harness config: they are the harness's,
    not environment variables, so its next session starts with them. Claude Code's need nothing here."""
    if isinstance(harness, RemoteControlCodex):
        harness.use_variant(applied.variant)
        return
    if not isinstance(harness, RemoteControlPi):
        return
    try:
        variant = pi_variant(*(given[name] or applied.flags.get(name, "") for name in ("provider", "model", "thinking")))
    except Refused as e:
        log.warning("the new harness config is not used, the session keeps the one it has: %s", e)
        return
    harness.use_variant(variant)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="slop-haus: remote control: %(message)s")
    root = args.workspace.resolve()
    with contextlib.ExitStack() as held:
        # Two hosts for one agent would fight over its identity, so a second launcher is refused. The host holds the
        # lock itself, so it stays held if the supervisor that started it dies. The flag only says who to blame.
        try:
            held.enter_context(supervisor_lock(root) if args.supervised else host_lock(root))
        except AlreadyRunning as e:
            log.error("%s", refusal(e))
            return 1
        except OSError as e:
            log.error("%s", HostLaunchRefused(f"the workspace lock could not be taken: {e}"))
            return 1
        return serve(args, root)


def serve(args: argparse.Namespace, root: Path) -> int:
    launch: PythonLaunch | None = None
    codex_variant = ""
    # A flag given on the command line replaces the config's, now and after a change on the board.
    given = {"provider": args.provider, "model": args.model, "thinking": args.thinking}
    if args.python_launch:
        launch = PythonLaunch(root, os.environ, SubprocessRunner(), log)
        try:
            applied = launch.run(args.harness)
        except (LaunchRefused, LauncherEnvError, HarnessConfigError, OSError) as e:
            log.error("%s", e)
            return 1
        args.provider = given["provider"] or applied.flags.get("provider", "")
        args.model = given["model"] or applied.flags.get("model", "")
        args.thinking = given["thinking"] or applied.flags.get("thinking", "")
        codex_variant = applied.variant
    server = os.environ.get("SLOP_HAUS_URL") or read_first_line(root / ".slop-haus" / "server-url")
    token_script = root / ".slop-haus" / "token.sh"
    if not server:
        log.error("no server address: re-run setup")
        return 1

    settings = Settings(idle_timeout=args.idle_timeout)
    stop = threading.Event()
    state_dir = root / ".slop-haus" / "conductor"
    try:
        chosen = choose_harness(args, root, state_dir, server, token_script, settings, codex_variant=codex_variant)
    except Refused as e:
        log.error("%s", e)
        return 1
    try:
        chosen.harness.check_installed()
    except FileNotFoundError:
        log.error("%s%s", chosen.missing, SUPERVISED_NOT_ON_PATH_HINT if args.supervised else "")
        return 127

    client = Client(server, SlotToken(token_script, SubprocessRunner(), max_age=settings.token_ttl, env={"SLOP_HAUS_URL": server}))
    host_id = str(uuid.uuid4())
    reporter = Reporter(client, host_id, stop, settings.min_report_interval)
    watch = ConfigWatch(launch, args.harness, lambda applied: retune(chosen.harness, given, applied)) if launch is not None else None
    host = Host(str(root), reporter, chosen.harness, settings, stopping=stop, records=chosen.records,
                idle_marker=IdleMarker(state_dir / "idle"), config=watch,
                attachments=AttachmentStore(str(root), ClientDownloader(client)))
    lease = HostLease(client, host_id, stop)
    try:
        lease.acquire()
    except LeaseRefused as e:
        log.error("%s", e)
        return 1
    except (ServerError, Unreachable) as e:
        log.error("could not take the host lease: %s", e)
        return 1
    poller = Poller(client, host_id, host, settings.poll_wait, lease_refresh=settings.lease_refresh)

    def interrupted(signum: int, frame: FrameType | None) -> None:
        stop.set()

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)

    log.info("%s", chosen.banner)
    log.info("host %s serving %s", host_id, server)
    reporter.start()
    LayoutReporter(client, root, stop, settings.layout_interval).start()
    lease.start()
    poller.start()
    host.run()
    reporter.drain()
    # After the drain, which still needs the host id to be the server's: a
    # poll in flight cannot take the Remote Control back, only a poll that
    # starts can, and the poller starts none once stop is set.
    client.release(host_id)
    lease.release()
    return 0


if __name__ == "__main__":
    sys.exit(main())
