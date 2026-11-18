from __future__ import annotations

import configparser
import plistlib
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from typing import Any

from slop_haus_supervisor.fleet.scheduler import SchedulerConfig
from slop_haus_supervisor.service.exit_codes import ExitCode
from slop_haus_supervisor.service.serve import ServeOptions

SERVICE_DIR = Path(__file__).resolve().parents[2] / "service"
# The drain, the scheduler's wait for slot threads past it, and the time a run's process tree is given to die.
MINIMUM_STOP_SECONDS = ServeOptions.drain_timeout + SchedulerConfig.join_grace + 10


def unit() -> configparser.ConfigParser:
    parser = configparser.ConfigParser(strict=False, interpolation=None)
    parser.optionxform = str  # pyright: ignore[reportAttributeAccessIssue]
    with (SERVICE_DIR / "slop-haus-supervisor@.service").open() as f:
        parser.read_file(f)
    return parser


class SystemdUnitTest(unittest.TestCase):
    def test_it_restarts_failures_but_not_what_a_restart_cannot_fix(self) -> None:
        service = unit()["Service"]

        self.assertEqual("on-failure", service["Restart"])
        self.assertEqual(
            {int(ExitCode.ALREADY_RUNNING), int(ExitCode.CONFIG)}, {int(c) for c in service["RestartPreventExitStatus"].split()}
        )

    def test_it_gives_a_drain_time_to_finish_and_signals_only_the_supervisor(self) -> None:
        service = unit()["Service"]

        self.assertEqual("mixed", service["KillMode"])
        self.assertGreaterEqual(int(service["TimeoutStopSec"]), MINIMUM_STOP_SECONDS)

    def test_uv_is_found_by_absolute_path_since_run_env_is_not_read_yet(self) -> None:
        self.assertTrue(unit()["Service"]["ExecStart"].split()[0].startswith(("/", "%h/")))

    def test_it_runs_one_instance_per_tenant_and_sets_no_environment(self) -> None:
        service = unit()["Service"]

        self.assertIn("--tenant %i", service["ExecStart"])
        self.assertNotIn("Environment", service)
        self.assertNotIn("EnvironmentFile", service)

    def test_systemd_verifies_it_when_present(self) -> None:
        analyze = shutil.which("systemd-analyze")
        true = shutil.which("true")
        if analyze is None or true is None:
            self.skipTest("systemd-analyze is not installed")

        # verify also checks that the command exists here, and uv is at
        # ~/.local/bin only on a host set up as the guide says: point a copy
        # at a command every host has, so the rest of the unit is checked
        # anywhere.
        text = (SERVICE_DIR / "slop-haus-supervisor@.service").read_text()
        self.assertIn("ExecStart=%h/.local/bin/uv ", text)
        with tempfile.TemporaryDirectory() as tmp:
            copy = Path(tmp) / "slop-haus-supervisor@.service"
            copy.write_text(text.replace("ExecStart=%h/.local/bin/uv ", f"ExecStart={true} "))
            done = subprocess.run(
                [analyze, "--user", "--no-pager", "verify", str(copy)],
                capture_output=True, text=True, check=False,
            )  # fmt: skip

        self.assertEqual(0, done.returncode, done.stdout + done.stderr)


class LaunchdPlistTest(unittest.TestCase):
    def load(self) -> dict[str, Any]:
        with (SERVICE_DIR / "com.slop-haus.supervisor.plist").open("rb") as f:
            return plistlib.load(f)

    def test_it_restarts_only_after_a_failure(self) -> None:
        self.assertEqual({"SuccessfulExit": False}, self.load()["KeepAlive"])

    def test_it_waits_out_a_drain_before_killing(self) -> None:
        self.assertGreaterEqual(self.load()["ExitTimeOut"], MINIMUM_STOP_SECONDS)

    def test_uv_is_found_by_absolute_path_since_run_env_is_not_read_yet(self) -> None:
        self.assertTrue(self.load()["ProgramArguments"][0].startswith("/"))

    def test_it_starts_when_loaded(self) -> None:
        self.assertIs(True, self.load()["RunAtLoad"])

    def test_it_logs_to_the_supervisors_rotated_file(self) -> None:
        arguments = self.load()["ProgramArguments"]

        self.assertIn("--log-file", arguments)
        log_path = arguments[arguments.index("--log-file") + 1]
        self.assertRegex(log_path, r"^/Users/USER/Library/Logs/slop-haus-supervisor-TENANT\.log$")
        self.assertEqual("/dev/null", self.load()["StandardOutPath"])

    def test_plutil_lints_it_when_present(self) -> None:
        plutil = shutil.which("plutil")
        if plutil is None:
            self.skipTest("plutil is not installed")

        done = subprocess.run([plutil, "-lint", str(SERVICE_DIR / "com.slop-haus.supervisor.plist")], capture_output=True, text=True, check=False)

        self.assertEqual(0, done.returncode, done.stdout + done.stderr)
