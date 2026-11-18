from __future__ import annotations

import logging
import subprocess
import unittest
import unittest.mock
from pathlib import Path

from slop_haus_supervisor.harness.tool_check import ToolChecker
from slop_haus_supervisor.reconcile.ledger import ResumeLedger
from slop_haus_supervisor.reconcile.reconciler import Reconciler, headless_run
from slop_haus_supervisor.workspace import config as cfg
from reconcile_support import ReconcilerTestCase, entry


class ProvisionTest(ReconcilerTestCase):
    def test_an_empty_document_does_nothing(self) -> None:
        self.reconciler.tick()

        self.assertEqual([], self.api.reports)
        self.assertEqual([], self.api.calls)
        self.assertEqual([], self.host.started)

    def test_a_missing_slot_is_made_started_and_reported(self) -> None:
        self.want(entry("w1"))

        self.reconciler.tick()

        self.assertEqual(["create w1"], self.api.calls)
        self.assertTrue((self.layout.slot("w1") / ".slop-haus" / "credentials.json").is_file())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)
        self.assertEqual(["w1"], self.host.started)
        self.assertEqual(["provisioning", "running"], [r["phase"] for batch in self.api.reports for r in batch])
        self.assertEqual(1, self.api.last_phase()["generation"])

    def test_an_existing_slot_is_reported_running_and_left_untouched(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        self.api.calls.clear()
        self.api.reports.clear()
        self.host.started.clear()
        marker = self.layout.slot("w1") / "keep.txt"
        marker.write_text("mine")
        credentials = self.layout.slot("w1") / ".slop-haus" / "credentials.json"
        before = credentials.read_bytes()
        self.reconciler = self.build()

        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual([], self.api.calls)
        self.assertEqual([], self.host.started)
        self.assertEqual("mine", marker.read_text())
        self.assertEqual(before, credentials.read_bytes())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)

    def test_a_converged_document_is_not_reported_twice(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        reports = len(self.api.reports)

        self.reconciler.tick()

        self.assertEqual(reports, len(self.api.reports))
        self.assertEqual(["create w1"], self.api.calls)

    def test_a_failed_provision_is_reported_and_not_retried_at_once(self) -> None:
        self.want(entry("w1"))
        self.reconciler = self.build()
        self.api.fail_create = True

        self.reconciler.tick()
        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertIn("boom", str(self.api.last_phase()["reason"]))
        self.assertEqual(["create w1"], self.api.calls)

        self.now += 301
        self.api.fail_create = False
        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_a_slot_refused_at_the_cap_is_created_once_the_cap_is_raised(self) -> None:
        self.want(entry("w1"))
        self.reconciler = self.build()
        self.api.fail_create = True
        self.api.fail_message = "the supervisor is at its slot cap: retire a slot first"

        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertIn("slot cap", str(self.api.last_phase()["reason"]))

        # The owner raises the cap on the board; nothing restarts the supervisor.
        self.api.fail_create = False
        self.now += 301
        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual(["create w1", "create w1"], self.api.calls)

    def test_a_harness_without_the_worker_capability_is_reported_failed(self) -> None:
        self.want(entry("w1", harness="unknown"))

        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertEqual([], self.api.calls)

    def test_a_pi_slot_is_provisioned(self) -> None:
        self.want(entry("w1", harness="pi"))

        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_remote_control_and_steward_entries_are_identity_only(self) -> None:
        self.want(entry("rc1", kind="remote_control"))

        self.reconciler.tick()

        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual([], self.api.calls)
        self.assertFalse(self.layout.slot("rc1").exists())


class SlotHarnessTest(ReconcilerTestCase):
    def test_a_slot_is_started_with_the_harness_the_board_names(self) -> None:
        self.want(entry("w1", harness="pi"))

        self.reconciler.tick()

        self.assertEqual("pi", self.host.slot_harness["w1"])

    def test_a_changed_harness_waits_for_the_run_to_end_then_restarts_the_slot(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        self.host.draining = True
        self.want(entry("w1", harness="pi", generation=2))

        self.reconciler.tick()

        self.assertEqual([], self.host.retired)
        self.assertEqual("claude", self.host.slot_harness["w1"])
        self.assertEqual("provisioning", self.api.last_phase()["phase"])

        self.host.draining = False
        self.reconciler.tick()

        self.assertEqual(["w1"], self.host.retired)
        self.assertEqual(["w1", "w1"], self.host.started)
        self.assertEqual("pi", self.host.slot_harness["w1"])
        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_the_report_names_the_harness_the_slot_runs(self) -> None:
        self.want(entry("w1", harness="pi"))

        self.reconciler.tick()

        self.assertEqual("pi", self.api.last_phase()["harness"])

    def test_a_waiting_report_names_the_old_harness_still_running(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        self.host.draining = True
        self.want(entry("w1", harness="pi", generation=2))

        self.reconciler.tick()

        self.assertEqual("claude", self.api.last_phase()["harness"])

    def test_a_paused_slot_still_reports_its_harness(self) -> None:
        self.want(entry("w1", harness="pi", state="paused"))

        self.reconciler.tick()

        self.assertEqual("paused", self.api.last_phase()["phase"])
        self.assertEqual("pi", self.api.last_phase()["harness"])

    def test_a_retired_slot_reports_no_harness(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        self.want(entry("w1", state="removed", generation=2))

        self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase()["phase"])
        self.assertNotIn("harness", self.api.last_phase())

    def test_a_slot_already_running_the_desired_harness_is_left_alone(self) -> None:
        self.want(entry("w1"))
        self.reconciler.tick()
        self.reconciler.tick()

        self.assertEqual([], self.host.retired)
        self.assertEqual(["w1"], self.host.started)


class PauseTest(ReconcilerTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.want(entry("w1"))
        self.reconciler.tick()

    def test_the_owners_hold_pauses_and_running_releases_it(self) -> None:
        self.want(entry("w1", state="paused", generation=2))
        self.reconciler.tick()
        self.assertTrue(self.host.holds["w1"])
        self.assertEqual("paused", self.api.last_phase()["phase"])

        self.want(entry("w1", state="running", generation=3))
        self.reconciler.tick()
        self.assertFalse(self.host.holds["w1"])
        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_desired_running_alone_never_clears_a_cause_pause(self) -> None:
        self.host.cause["w1"] = "usage limit"

        self.reconciler.tick()
        self.want(entry("w1", generation=2))
        self.reconciler.tick()

        self.assertEqual([], self.host.unpaused)
        self.assertEqual({"phase": "paused", "reason": "usage limit"}, {k: self.api.last_phase()[k] for k in ("phase", "reason")})

    def test_a_raised_resume_generation_clears_a_cause_pause_once(self) -> None:
        self.host.cause["w1"] = "usage limit"

        self.want(entry("w1", generation=2, resume_generation=1))
        self.reconciler.tick()
        self.reconciler.tick()

        self.assertEqual([["w1"]], self.host.unpaused)
        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_the_applied_counter_survives_a_restart(self) -> None:
        self.want(entry("w1", generation=2, resume_generation=1))
        self.reconciler.tick()
        self.host.cause["w1"] = "usage limit"

        self.reconciler = self.build()
        self.reconciler.tick()

        self.assertEqual([["w1"]], self.host.unpaused)  # only the first, before the restart
        self.assertEqual("paused", self.api.last_phase()["phase"])


class RemoveTest(ReconcilerTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.want(entry("w1"))
        self.reconciler.tick()

    def remove(self) -> None:
        self.want(entry("w1", state="removed", generation=2))
        self.reconciler.tick()

    def test_a_slot_still_running_is_waited_for_and_nothing_is_revoked_or_deleted(self) -> None:
        self.host.draining = True

        self.remove()

        self.assertEqual("provisioning", self.api.last_phase()["phase"])
        self.assertTrue(self.layout.slot("w1").is_dir())
        self.assertNotIn("retire w1", self.api.calls)

    def test_drain_then_revoke_then_delete_then_retired(self) -> None:
        self.host.draining = True
        self.remove()
        self.host.draining = False

        self.reconciler.tick()

        self.assertEqual(["w1"], self.host.retired)
        self.assertIn("retire w1", self.api.calls)
        self.assertFalse(self.layout.slot("w1").exists())
        self.assertEqual((), cfg.load(self.layout.config_file).slots)
        self.assertEqual({"phase": "retired", "generation": 2}, {k: self.api.last_phase()[k] for k in ("phase", "generation")})

    def test_a_retired_agent_is_not_removed_again(self) -> None:
        self.remove()
        calls = list(self.api.calls)

        self.reconciler.tick()

        self.assertEqual(calls, self.api.calls)

    def test_an_agent_this_host_never_made_is_simply_retired(self) -> None:
        self.want(entry("ghost", id="22222222-2222-4222-8222-222222222222", state="removed", generation=3))

        self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase("22222222-2222-4222-8222-222222222222")["phase"])


class ReportTest(ReconcilerTestCase):
    def test_a_long_failure_reason_is_cut_to_what_the_server_accepts(self) -> None:
        self.want(entry("w1"))
        self.api.fail_message = "x" * 900
        self.api.fail_create = True

        self.reconciler.tick()

        self.assertEqual("x" * 500, self.api.last_phase()["reason"])


class HeadlessRunTest(unittest.TestCase):
    def test_setup_has_no_terminal_and_cannot_prompt_for_git_credentials(self) -> None:
        with unittest.mock.patch("subprocess.run") as run:
            run.return_value.returncode = 0
            headless_run(["bash", "x"], Path("/tmp"))

        kwargs = run.call_args.kwargs
        self.assertEqual(subprocess.DEVNULL, kwargs["stdin"])
        self.assertEqual("0", kwargs["env"]["GIT_TERMINAL_PROMPT"])
        self.assertGreater(kwargs["timeout"], 0)

    def test_a_setup_that_runs_out_of_time_fails(self) -> None:
        with unittest.mock.patch("subprocess.run", side_effect=subprocess.TimeoutExpired("x", 1)):
            self.assertNotEqual(0, headless_run(["bash", "x"], Path("/tmp")))


class ToolCheckTest(ReconcilerTestCase):
    def setUp(self) -> None:
        self.service: dict[str, str] = {"claude": "/s/claude"}
        self.login: dict[str, str] = {}
        self.extra: list[str] = []
        super().setUp()

    def build(self) -> Reconciler:
        return Reconciler(
            self.layout, self.api, self.host, self.repo, ResumeLedger(self.layout.state_dir / "reconcile.json"),
            logging.getLogger("test"), lambda: self.now, lambda argv, cwd: self._run(argv),
            tools=ToolChecker(self.service.get, self.login.get),
            extra_tools=lambda: self.extra,
        )

    def test_a_harness_only_the_login_shell_has_fails_the_slot_with_the_run_env_hint(self) -> None:
        self.login["pi"] = "/h/pi-node/bin/pi"
        self.want(entry("w1", harness="pi"))

        self.reconciler.tick()

        last = self.api.last_phase()
        self.assertEqual("failed", last["phase"])
        self.assertIn("pi is not on this service's PATH (found at /h/pi-node/bin in a login shell)", str(last["reason"]))
        self.assertIn("run.env", str(last["reason"]))
        self.assertEqual([], self.host.started)

    def test_the_slot_runs_once_the_supervisor_restarts_with_the_tool_on_its_path(self) -> None:
        self.want(entry("w1", harness="pi"))
        self.reconciler.tick()
        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.service["pi"] = "/s/pi"
        self.reconciler = self.build()

        self.reconciler.tick()
        self.assertEqual("running", self.api.last_phase()["phase"])
        self.assertEqual(["w1"], self.host.started)

    def test_a_missing_extra_tool_warns_and_keeps_the_slot_running(self) -> None:
        self.extra = ["go"]
        self.login["go"] = "/h/go/bin/go"
        self.want(entry("w1"))

        self.reconciler.tick()

        last = self.api.last_phase()
        self.assertEqual("running", last["phase"])
        self.assertIn("go is not on this service's PATH", str(last["reason"]))
        self.assertEqual(["w1"], self.host.started)

    def test_present_extra_tools_leave_no_warning(self) -> None:
        self.extra = ["go"]
        self.service["go"] = "/s/go"
        self.want(entry("w1"))

        self.reconciler.tick()

        self.assertNotIn("reason", self.api.last_phase())

    def test_a_failed_harness_is_not_looked_at_again_before_the_retry_window_and_is_after_it(self) -> None:
        self.want(entry("w1", harness="pi"))
        self.reconciler.tick()
        self.service["pi"] = "/s/pi"

        self.now += 10
        self.reconciler.tick()
        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertEqual([], self.host.started)

        self.now += 301
        self.reconciler.tick()
        self.assertEqual("running", self.api.last_phase()["phase"])

    def test_a_tool_installed_nowhere_and_several_missing_tools_are_all_named(self) -> None:
        self.extra = ["go", "node"]
        self.login["node"] = "/h/node/bin/node"
        self.want(entry("w1"))

        self.reconciler.tick()

        reason = str(self.api.last_phase()["reason"])
        self.assertIn("go is not installed", reason)
        self.assertIn("node is not on this service's PATH (found at /h/node/bin", reason)
