from __future__ import annotations

import logging
import os
import signal
import tempfile
import threading
import unittest
from collections.abc import Callable
from pathlib import Path

from fakes import FakeRunner, FakeSlot
from slop_haus_supervisor.fleet.scheduler import Scheduler, SchedulerConfig
from slop_haus_supervisor.fleet.slot import Phase
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.service.exit_codes import ExitCode
from slop_haus_supervisor.service.instance_lock import InstanceLock
from slop_haus_supervisor.service.serve import ServeOptions, serve
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout

LOG = logging.getLogger("test-serve")


def ssh_remote_answering(push: ProcessResult) -> Callable[[list[str]], ProcessResult]:
    """Says the remote is ssh, and answers a push with `push`."""
    return lambda argv: ProcessResult(0, "git@example.com:a/b.git\n", "") if argv[1] == "remote" else push


class Notice(logging.Handler):
    """Sets an event when a log line holds `text`, so a test can wait for what the supervisor did."""

    def __init__(self, text: str) -> None:
        super().__init__()
        self.text = text
        self.seen = threading.Event()

    def emit(self, record: logging.LogRecord) -> None:
        if self.text in record.getMessage():
            self.seen.set()


class ServeTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.layout = Layout(self.tmp / "root")
        self.layout.root.mkdir()
        self.layout.config_file.write_text(SupervisorConfig("https://s", "t", "acme", slots=("a",)).dumps())
        self.slot = FakeSlot()
        self.built = 0
        self.environ: dict[str, str] = {"PATH": "/usr/bin"}
        self.restart: threading.Event | None = None
        self.scheduler: Scheduler | None = None

    def build(self, layout: Layout, config: SupervisorConfig, logger: logging.Logger, stop: threading.Event,
              scheduler_config: SchedulerConfig, restart_requested: threading.Event) -> Scheduler:  # fmt: skip
        self.built += 1
        self.restart = restart_requested
        self.scheduler_config = scheduler_config
        self.scheduler = Scheduler([self.slot], stop, LOG, scheduler_config)
        return self.scheduler

    def serve(self, options: ServeOptions = ServeOptions(check_push=False), runner: FakeRunner | None = None) -> ExitCode:
        return serve(self.layout, options, self.build, self.environ, runner)

    def send_after(self, ready: threading.Event, *signals: int, between: threading.Event | None = None) -> None:
        def sender() -> None:
            ready.wait(10)
            for index, sig in enumerate(signals):
                if index and between is not None:
                    between.wait(10)
                os.kill(os.getpid(), sig)

        thread = threading.Thread(target=sender, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 10)

    def test_sigterm_and_sigint_each_drain_and_exit_zero(self) -> None:
        for sig in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=sig.name):
                self.slot = FakeSlot()
                self.send_after(self.slot.stepped, sig)

                self.assertEqual(ExitCode.STOPPED, self.serve())

    def test_the_drain_timeout_reaches_the_scheduler(self) -> None:
        self.send_after(self.slot.stepped, signal.SIGTERM)

        self.serve(ServeOptions(drain_timeout=42.0, check_push=False))

        self.assertEqual(42.0, self.scheduler_config.drain_timeout)

    def test_a_second_signal_ends_a_run_that_is_still_going(self) -> None:
        release = threading.Event()
        self.addCleanup(release.set)
        self.slot = FakeSlot(Phase.RUNNING)
        # A run that only ends when it is abandoned; a drain alone would wait the whole timeout.
        self.slot.on_step = [lambda: self.slot.abandoned.wait(30)]
        draining = Notice("draining")
        logging.getLogger("slop-haus-supervisor").addHandler(draining)
        self.addCleanup(logging.getLogger("slop-haus-supervisor").removeHandler, draining)
        logging.getLogger("slop-haus-supervisor").setLevel(logging.INFO)
        self.send_after(self.slot.stepped, signal.SIGTERM, signal.SIGTERM, between=draining.seen)

        code = self.serve(ServeOptions(drain_timeout=1000.0, check_push=False))

        self.assertEqual(ExitCode.STOPPED, code)
        self.assertTrue(self.slot.abandoned.is_set())

    def test_the_handlers_are_put_back(self) -> None:
        before = signal.getsignal(signal.SIGTERM)
        self.send_after(self.slot.stepped, signal.SIGTERM)

        self.serve()

        self.assertEqual(before, signal.getsignal(signal.SIGTERM))

    def test_a_missing_config_is_the_config_code(self) -> None:
        self.layout.config_file.unlink()

        self.assertEqual(ExitCode.CONFIG, self.serve())
        self.assertEqual(0, self.built)

    def test_a_bad_config_is_the_config_code(self) -> None:
        self.layout.config_file.write_text("this is = = not toml")

        self.assertEqual(ExitCode.CONFIG, self.serve())

    def test_a_second_instance_for_the_tenant_exits_with_the_already_running_code(self) -> None:
        with InstanceLock(self.layout.root / ".run.lock"):
            code = self.serve()

        self.assertEqual(ExitCode.ALREADY_RUNNING, code)
        self.assertEqual(0, self.built)

    def test_the_lock_is_released_when_the_run_ends(self) -> None:
        self.send_after(self.slot.stepped, signal.SIGTERM)
        self.serve()

        with InstanceLock(self.layout.root / ".run.lock"):
            pass

    def test_a_server_restart_request_exits_with_the_restart_code(self) -> None:
        def ask_to_restart() -> None:
            assert self.restart is not None and self.scheduler is not None
            self.restart.set()
            self.scheduler.drain()

        self.slot.on_step = [ask_to_restart]

        self.assertEqual(ExitCode.RESTART, self.serve())

    def test_an_unexpected_error_is_a_failure(self) -> None:
        def broken(*_args: object) -> Scheduler:
            raise RuntimeError("boom")

        with self.assertLogs("slop-haus-supervisor", "ERROR"):
            code = serve(self.layout, ServeOptions(check_push=False), broken, self.environ)

        self.assertEqual(ExitCode.FAILED, code)

    def test_run_env_is_parsed_not_executed(self) -> None:
        marker = self.tmp / "ran"
        (self.layout.root / "run.env").write_text(f"PATH=/opt/tools\nGREETING=$(touch {marker})\n")
        self.send_after(self.slot.stepped, signal.SIGTERM)

        self.serve()

        self.assertFalse(marker.exists())
        self.assertEqual(f"$(touch {marker})", self.environ["GREETING"])
        self.assertEqual("/opt/tools:/usr/bin", self.environ["PATH"])

    def test_git_is_told_not_to_prompt_without_overriding_the_operator(self) -> None:
        self.environ["GIT_SSH_COMMAND"] = "ssh -i /custom"
        self.send_after(self.slot.stepped, signal.SIGTERM)

        self.serve()

        self.assertEqual("0", self.environ["GIT_TERMINAL_PROMPT"])
        self.assertEqual("ssh -i /custom", self.environ["GIT_SSH_COMMAND"])

    def test_a_push_that_would_prompt_is_a_config_error_before_anything_is_built(self) -> None:
        runner = FakeRunner(ssh_remote_answering(ProcessResult(128, "", "git@example.com: Permission denied (publickey).")))

        code = self.serve(ServeOptions(check_push=True), runner)

        self.assertEqual(ExitCode.CONFIG, code)
        self.assertEqual(0, self.built)

    def test_no_secret_reaches_the_log_stream_or_file(self) -> None:
        secret = "sk-very-secret-value"
        self.layout.secrets_file.write_text(f"ANTHROPIC_API_KEY={secret}\n")
        self.layout.repo_env_file.write_text("SELF_REVIEW_KEY=repo-secret-value\n")
        log_file = self.tmp / "supervisor.log"
        self.slot.on_step = [lambda: LOG.info("token %s and %s", secret, "repo-secret-value")]
        self.send_after(self.slot.stepped, signal.SIGTERM)

        self.serve(ServeOptions(log_file=log_file, check_push=False))

        text = log_file.read_text()
        self.assertNotIn(secret, text)
        self.assertNotIn("repo-secret-value", text)
        self.assertIn("token *** and ***", text)
        self.assertEqual(0o600, log_file.stat().st_mode & 0o777)
