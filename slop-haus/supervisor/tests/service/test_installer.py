from __future__ import annotations

import plistlib
import tempfile
from collections.abc import Callable
import unittest
from pathlib import Path

from slop_haus_supervisor.launcher.activity import Activity, SlotActivity
from slop_haus_supervisor.launcher.multiplexer import Screen
from slop_haus_supervisor.launcher.supervisor_session import SupervisorSession
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.service.installer import Clock, InstallOptions, ServiceInstaller
from slop_haus_supervisor.service.run_command import run_argv
from slop_haus_supervisor.service.service_units import Launchd, ServiceManager, SystemdUser
from slop_haus_supervisor.setup.prerequisites import Finding
from slop_haus_supervisor.workspace.layout import Layout

UV = "/home/me/.local/bin/uv"


class Recorder:
    """Every command and the drain, in order, so a test can say what came before what."""

    def __init__(self) -> None:
        self.events: list[str] = []
        self.answers: dict[str, ProcessResult] = {}

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: object = None) -> ProcessResult:
        line = " ".join(argv)
        self.events.append(line)
        for prefix, result in self.answers.items():
            if line.startswith(prefix):
                return result
        return ProcessResult(0, "", "")


class FakeHost:
    def __init__(self, events: list[str]) -> None:
        self.events = events
        self.is_running = False
        self.slots: tuple[SlotActivity, ...] = ()
        self.up_after_start = True

    def running(self) -> bool:
        return self.is_running or (self.up_after_start and any(e.startswith("systemctl --user start") for e in self.events))

    def activity(self) -> Activity:
        return Activity(self.slots, None)

    def drain(self) -> int:
        self.events.append("drain")
        self.is_running = False
        return 0


class InstallerTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name) / "home"
        self.layout = Layout(self.home / "slop-haus" / "acme")
        self.runner = Recorder()
        self.host = FakeHost(self.runner.events)
        self.findings = [Finding("uv", True, "found")]
        self.out: list[str] = []
        self.answer = "n"
        self.slept = 0
        self.manager: ServiceManager = SystemdUser(self.layout, "acme", self.runner, self.home, "me")
        self.up()

    def up(self) -> None:
        self.runner.answers["systemctl --user show"] = ProcessResult(0, "ActiveState=active\nExecMainStatus=0\n", "")
        self.runner.answers["loginctl"] = ProcessResult(0, "Linger=yes\n", "")
        self.host.slots = (SlotActivity("s-1", "idle", "", 0.0),)

    def installer(self, ask: Callable[[str, str], str] | None = None) -> ServiceInstaller:
        def sleep(_s: float) -> None:
            self.slept += 1

        ticks = iter(range(10_000))
        return ServiceInstaller(
            self.manager, self.host, lambda: self.findings, lambda: UV, ask or (lambda _q, _d: self.answer), self.out.append,
            Clock(sleep, lambda: float(next(ticks))),
        )  # fmt: skip

    def install(self, options: InstallOptions | None = None) -> int:
        return self.installer().install(options or InstallOptions())

    def text(self) -> str:
        return "\n".join(self.out)

    def unit(self) -> dict[str, dict[str, str]]:
        sections: dict[str, dict[str, str]] = {}
        current: dict[str, str] = {}
        for line in self.manager.unit_path.read_text().splitlines():
            if line.startswith("["):
                sections[line.strip("[]")] = current = {}
            elif "=" in line and not line.startswith("#"):
                key, _, value = line.partition("=")
                current[key] = value
        return sections

    def test_the_unit_runs_the_same_command_as_start_with_uv_by_absolute_path(self) -> None:
        self.assertEqual(0, self.install())

        exec_start = self.unit()["Service"]["ExecStart"]
        self.assertEqual(" ".join(run_argv(UV, self.layout)), exec_start)
        self.assertIn("--no-dev", exec_start)

    def test_start_and_the_unit_cannot_drift(self) -> None:
        session = SupervisorSession(self.layout, "acme", Screen(Recorder()), which=lambda _n: UV)

        self.assertEqual(run_argv(UV, self.layout), session.command())

    def test_the_unit_keeps_the_sample_restart_and_drain_settings(self) -> None:
        self.install()

        service = self.unit()["Service"]
        self.assertEqual("on-failure", service["Restart"])
        self.assertEqual("75 78", service["RestartPreventExitStatus"])
        self.assertEqual("mixed", service["KillMode"])
        self.assertGreaterEqual(int(service["TimeoutStopSec"]), 150)

    def test_a_path_with_a_space_or_percent_is_quoted_for_systemd(self) -> None:
        self.layout = Layout(self.home / "my slop%haus")
        self.manager = SystemdUser(self.layout, "acme", self.runner, self.home, "me")

        self.install()

        exec_start = self.unit()["Service"]["ExecStart"]
        self.assertIn('"' + str(self.layout.app).replace("%", "%%") + '"', exec_start)

    def test_it_loads_enables_and_starts(self) -> None:
        self.assertEqual(0, self.install())

        events = [e for e in self.runner.events if e.startswith("systemctl")]
        self.assertEqual(
            ["daemon-reload", "enable slop-haus-supervisor@acme.service", "start slop-haus-supervisor@acme.service --no-block"],
            [e.removeprefix("systemctl --user ") for e in events[:3]],
        )

    def test_a_rerun_is_idempotent_and_a_change_shows_a_diff(self) -> None:
        self.install()
        before = self.manager.unit_path.read_text()
        self.out.clear()

        self.install()
        self.assertEqual(before, self.manager.unit_path.read_text())
        self.assertIn("is up to date", self.text())

        self.out.clear()
        self.manager.unit_path.write_text(before.replace("RestartSec=10", "RestartSec=99"))
        self.install()
        self.assertEqual(before, self.manager.unit_path.read_text())
        self.assertIn("-RestartSec=99", self.text())
        self.assertIn("+RestartSec=10", self.text())

    def test_it_refuses_while_a_required_tool_is_missing_from_the_service_path(self) -> None:
        self.findings = [Finding("claude", False, "is at /h/.local/bin/claude, which the service won't see", path_fix="/h/.local/bin")]

        self.assertEqual(1, self.install())

        self.assertFalse(self.manager.unit_path.exists())
        self.assertEqual([], self.runner.events)
        self.assertIn("claude", self.text())

    def test_a_warning_finding_does_not_refuse(self) -> None:
        self.findings = [Finding("gh", False, "install gh", blocking=False)]

        self.assertEqual(0, self.install())

    def test_lingering_off_prints_the_exact_sudo_line_and_runs_nothing_as_root(self) -> None:
        self.runner.answers["loginctl"] = ProcessResult(0, "Linger=no\n", "")

        self.assertEqual(0, self.install())

        self.assertIn("sudo loginctl enable-linger me", self.text())
        self.assertFalse(any("sudo" in e for e in self.runner.events))

    def test_lingering_on_says_nothing(self) -> None:
        self.install()

        self.assertNotIn("linger", self.text())

    def test_a_running_supervisor_is_drained_before_the_unit_starts(self) -> None:
        self.host.is_running = True

        self.assertEqual(0, self.install())

        self.assertLess(self.runner.events.index("drain"), self.runner.events.index("systemctl --user start slop-haus-supervisor@acme.service --no-block"))
        self.assertIn("already running", self.text())

    def test_a_slot_mid_ticket_asks_before_draining_and_declining_changes_nothing(self) -> None:
        self.host.is_running = True
        self.host.slots = (SlotActivity("s-1", "running", "(ticket #4)", 0.0),)

        self.assertEqual(1, self.install())

        self.assertNotIn("drain", self.runner.events)
        self.assertFalse(any("start" in e for e in self.runner.events))
        self.assertFalse(self.manager.unit_path.exists())
        self.assertIn("--when-idle", self.text())

    def test_yes_drains_a_busy_supervisor_without_asking(self) -> None:
        self.host.is_running = True
        self.host.slots = (SlotActivity("s-1", "running", "", 0.0),)

        def never(_q: str, _d: str) -> str:
            raise AssertionError("asked")

        self.assertEqual(0, self.installer(never).install(InstallOptions(yes=True)))
        self.assertIn("drain", self.runner.events)

    def test_agreeing_or_yes_drains_a_busy_supervisor(self) -> None:
        self.host.is_running = True
        self.host.slots = (SlotActivity("s-1", "running", "", 0.0),)
        self.answer = "y"

        self.assertEqual(0, self.install())
        self.assertIn("drain", self.runner.events)

    def test_when_idle_waits_for_the_slot_then_drains(self) -> None:
        busy = (SlotActivity("s-1", "running", "", 0.0),)
        idle = (SlotActivity("s-1", "idle", "", 0.0),)
        self.host.is_running = True
        calls = iter([busy, busy])

        def activity() -> Activity:
            return Activity(next(calls, idle), None)

        self.host.activity = activity

        self.assertEqual(0, self.install(InstallOptions(when_idle=True)))

        self.assertGreaterEqual(self.slept, 1)
        self.assertIn("drain", self.runner.events)

    def test_when_idle_gives_up_at_its_deadline_and_leaves_the_supervisor(self) -> None:
        self.host.is_running = True
        self.host.slots = (SlotActivity("s-1", "running", "", 0.0),)

        self.assertEqual(1, self.install(InstallOptions(when_idle=True, idle_deadline=3)))

        self.assertNotIn("drain", self.runner.events)
        self.assertFalse(self.manager.unit_path.exists())
        self.assertIn("still mid-ticket", self.text())

    def exited(self, status: int, state: str = "failed") -> None:
        self.runner.answers["systemctl --user show"] = ProcessResult(0, f"ActiveState={state}\nExecMainStatus={status}\n", "")
        self.runner.answers["journalctl"] = ProcessResult(0, "ERROR configuration error: claude is not installed\n", "")
        self.host.up_after_start = False

    def test_a_config_error_exit_is_shown_with_the_log_not_left_to_journalctl(self) -> None:
        self.exited(78)

        self.assertEqual(1, self.install(InstallOptions(follow_seconds=30)))

        self.assertIn("configuration error (78)", self.text())
        self.assertIn("claude is not installed", self.text())

    def test_exit_75_says_another_supervisor_holds_the_tenant(self) -> None:
        self.exited(75)

        self.assertEqual(1, self.install(InstallOptions(follow_seconds=30)))

        self.assertIn("another supervisor holds this tenant (75)", self.text())

    def test_a_retrying_unit_shows_its_failure(self) -> None:
        self.exited(1, "activating")

        self.assertEqual(1, self.install(InstallOptions(follow_seconds=30)))

        self.assertIn("status 1", self.text())

    def test_it_follows_until_the_slots_are_up(self) -> None:
        self.assertEqual(0, self.install())

        self.assertIn("up: 1 slot(s)", self.text())

    def test_a_start_that_never_gets_slots_up_reports_the_log_after_the_deadline(self) -> None:
        self.host.slots = ()
        self.runner.answers["journalctl"] = ProcessResult(0, "still syncing\n", "")

        self.assertEqual(1, self.install(InstallOptions(follow_seconds=3)))

        self.assertIn("did not come up", self.text())
        self.assertIn("still syncing", self.text())

    def test_uninstall_stops_disables_and_removes_the_unit(self) -> None:
        self.install()
        self.runner.events.clear()

        self.assertEqual(0, self.installer().uninstall())

        self.assertFalse(self.manager.unit_path.exists())
        self.assertEqual(
            ["stop slop-haus-supervisor@acme.service", "disable slop-haus-supervisor@acme.service"],
            [e.removeprefix("systemctl --user ") for e in self.runner.events],
        )


class LaunchdTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name)
        self.layout = Layout(self.home / "slop-haus" / "acme")
        self.runner = Recorder()
        self.manager = Launchd(self.layout, "acme", self.runner, self.home, 501)

    def test_the_plist_has_the_same_command_and_a_restart_on_failure_only(self) -> None:
        agent = plistlib.loads(self.manager.render("/opt/homebrew/bin/uv").encode())

        self.assertEqual(run_argv("/opt/homebrew/bin/uv", self.layout), agent["ProgramArguments"])
        self.assertEqual({"SuccessfulExit": False}, agent["KeepAlive"])
        self.assertEqual("com.slop-haus.supervisor.acme", agent["Label"])
        self.assertNotIn("EnvironmentVariables", agent)

    def test_load_replaces_a_loaded_agent_then_bootstraps_it(self) -> None:
        self.manager.unit_path.parent.mkdir(parents=True)
        self.manager.unit_path.write_text(self.manager.render("/u"))

        self.manager.load()

        self.assertEqual(
            ["launchctl bootout gui/501/com.slop-haus.supervisor.acme", f"plutil -lint {self.manager.unit_path}",
             f"launchctl bootstrap gui/501 {self.manager.unit_path}"],
            self.runner.events,
        )  # fmt: skip

    def test_a_nonzero_last_exit_of_a_stopped_agent_is_reported(self) -> None:
        self.runner.answers["launchctl print"] = ProcessResult(0, "state = waiting\n last exit code = 78\n", "")

        self.assertEqual(78, self.manager.state().exit_status)
