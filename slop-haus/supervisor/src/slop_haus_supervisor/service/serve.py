from __future__ import annotations

import logging
import os
import signal
import threading
from collections.abc import Callable, MutableMapping
from dataclasses import dataclass
from pathlib import Path
from types import FrameType

from slop_haus_supervisor.fleet import wiring
from slop_haus_supervisor.fleet.scheduler import Scheduler, SchedulerConfig
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.ports import ProcessRunner, SubprocessRunner
from slop_haus_supervisor.service import log_setup, run_env
from slop_haus_supervisor.service.exit_codes import ExitCode
from slop_haus_supervisor.service.instance_lock import AlreadyRunning, InstanceLock
from slop_haus_supervisor.service.push_check import PushCheck
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.layered_env import known_values
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout

STOP_SIGNALS = (signal.SIGTERM, signal.SIGINT)

BuildScheduler = Callable[[Layout, SupervisorConfig, logging.Logger, threading.Event, SchedulerConfig, threading.Event], Scheduler]


@dataclass(frozen=True)
class ServeOptions:
    # How long a run is given to reach a wait point once stopping. A process manager's stop timeout
    # must exceed this by the scheduler's join grace and the kill grace.
    drain_timeout: float = 120.0
    log_file: Path | None = None
    log_max_bytes: int = log_setup.DEFAULT_LOG_BYTES
    check_push: bool = True


class StopSignals:
    """The first signal drains, a second ends the runs and exits. Handlers only set events."""

    def __init__(self, scheduler: Scheduler, logger: logging.Logger) -> None:
        self._scheduler = scheduler
        self._log = logger
        self._seen = 0

    def __call__(self, signum: int, _frame: FrameType | None) -> None:
        self._seen += 1
        name = signal.Signals(signum).name
        if self._seen == 1:
            self._log.info("%s: draining; no new work. Send another signal to stop at once", name)
            self._scheduler.drain()
        else:
            self._log.warning("%s: stopping at once", name)
            self._scheduler.abort()


def _masker(layout: Layout) -> SecretMasker:
    return SecretMasker([*wiring.read_secrets(layout.secrets_file).values(), *known_values(layout)])


def serve(
    layout: Layout,
    options: ServeOptions,
    build: BuildScheduler = wiring.build,
    environ: MutableMapping[str, str] = os.environ,
    runner: ProcessRunner | None = None,
) -> ExitCode:
    """Runs the supervisor in the foreground and says, by exit code, what a process manager should do."""
    logger = logging.getLogger("slop-haus-supervisor")
    try:
        masker = _masker(layout)
        with log_setup.configured(masker, options.log_file, options.log_max_bytes):
            return _serve(layout, options, logger, build, environ, runner or SubprocessRunner())
    except (cfg.ConfigError, envfile.EnvFileError, OSError) as err:
        # Before logging could start (an unreadable env file or log path).
        print(f"slop-haus-supervisor: {err}", flush=True)
        return ExitCode.CONFIG


def _serve(
    layout: Layout,
    options: ServeOptions,
    logger: logging.Logger,
    build: BuildScheduler,
    environ: MutableMapping[str, str],
    runner: ProcessRunner,
) -> ExitCode:
    try:
        config = cfg.load(layout.config_file)
        with InstanceLock(layout.root / ".run.lock"):
            applied = run_env.apply(layout.root / "run.env", environ)
            run_env.apply_git_defaults(environ)
            if applied:
                logger.info("run.env sets %s", ", ".join(applied))
            if options.check_push:
                PushCheck(runner, logger).check(layout.slot(name) for name in config.slots)
            restart_requested = threading.Event()
            scheduler = build(
                layout, config, logger, threading.Event(), SchedulerConfig(drain_timeout=options.drain_timeout), restart_requested
            )
            return _run(scheduler, config, restart_requested, logger)
    except AlreadyRunning as err:
        logger.error("%s; not starting a second", err)
        return ExitCode.ALREADY_RUNNING
    except (cfg.ConfigError, envfile.EnvFileError) as err:
        logger.error("configuration error: %s", err)
        return ExitCode.CONFIG
    except Exception:
        logger.exception("the supervisor failed")
        return ExitCode.FAILED


def _run(scheduler: Scheduler, config: SupervisorConfig, restart_requested: threading.Event, logger: logging.Logger) -> ExitCode:
    handler = StopSignals(scheduler, logger)
    previous = {sig: signal.signal(sig, handler) for sig in STOP_SIGNALS}
    try:
        logger.info("running %d slot(s) for %s", len(config.slots), config.server)
        scheduler.run()
    finally:
        for sig, old in previous.items():
            signal.signal(sig, old)
    if restart_requested.is_set():
        logger.info("stopped: the server asked for a newer supervisor; exiting for the process manager to restart it")
        return ExitCode.RESTART
    logger.info("stopped")
    return ExitCode.STOPPED
