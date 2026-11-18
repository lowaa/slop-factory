from __future__ import annotations

import logging
import threading
import unittest
from pathlib import Path
from typing import Any

from slop_haus_supervisor.fleet.scheduler import Scheduler, SchedulerConfig
from slop_haus_supervisor.fleet.slot import Phase
from slop_haus_supervisor.fleet.slot_report import SlotStateReporter, error_class
from slop_haus_supervisor.slophaus.client import SlotSnapshot
from slop_haus_supervisor.workspace.handover import Prepared
from fakes import CLAIM, CLAIMABLE, FakeSlopHaus, Rig

LOG = logging.getLogger("test")
SECRET = "sk-ant-oat01-SECRETTOKEN"


class ApiError(Exception):
    pass


class StubSlot:
    agent = "a"
    directory = Path("/slots/a")

    def __init__(self, phase: Phase = Phase.IDLE) -> None:
        self.phase = phase

    def snapshot(self) -> SlotSnapshot:
        return SlotSnapshot(self.phase.value, 5.0, 7)

    def abandon_run(self) -> None:
        pass

    def release_lock_for_stop(self) -> None:
        pass

    info: tuple[str, float] | None = None

    def pause_info(self) -> tuple[str, float] | None:
        return self.info

    def try_unpause(self, manual: bool = False) -> str:
        return ""


class Ticks:
    def __init__(self) -> None:
        self.now = 100.0

    def __call__(self) -> float:
        return self.now


class ErrorClassTest(unittest.TestCase):
    def test_a_type_name_becomes_a_short_token(self) -> None:
        self.assertEqual("api_error", error_class(ApiError()))
        self.assertEqual("http_client_error", error_class(type("HTTPClientError", (Exception,), {})()))
        self.assertEqual(64, len(error_class(type("A" + "b" * 200, (Exception,), {})())))


class ReporterTest(unittest.TestCase):
    def setUp(self) -> None:
        self.client = FakeSlopHaus()
        self.ticks = Ticks()
        self.slot = StubSlot()
        self.layouts: list[Path] = []
        self.layout: dict[str, Any] | None = None
        self.bundle = "c1ff58edfd3c9bb6"
        self.reporter = SlotStateReporter(self.client, LOG, 30.0, self.ticks, self.read_layout, lambda _root: self.bundle)

    def read_layout(self, root: Path) -> dict[str, Any] | None:
        self.layouts.append(root)
        return self.layout

    def test_every_report_carries_the_bundle_the_workspace_has_now(self) -> None:
        self.reporter.after_step(self.slot, None)
        self.bundle = "0123456789abcdef"
        self.ticks.now += 31
        self.reporter.after_step(self.slot, None)

        self.assertEqual(["c1ff58edfd3c9bb6", "0123456789abcdef"], [r.bundle_hash for r in self.client.slot_reports])

    def test_a_bundle_that_cannot_be_read_does_not_stop_the_report(self) -> None:
        def broken(_root: Path) -> str:
            raise PermissionError("no")

        SlotStateReporter(self.client, LOG, 30.0, self.ticks, self.read_layout, broken).after_step(self.slot, None)

        self.assertEqual([""], [r.bundle_hash for r in self.client.slot_reports])

    def test_the_first_report_carries_the_layout_of_the_slot_root(self) -> None:
        self.layout = {"root": {"path": ".", "git": False, "origin": ""}, "repos": []}
        self.reporter.after_step(self.slot, None)

        self.assertEqual([Path("/slots/a")], self.layouts)
        self.assertEqual({"root": {"path": ".", "git": False, "origin": ""}, "repos": []}, self.client.slot_reports[0].layout)

    def test_the_layout_is_read_again_when_the_phase_moves_but_not_for_a_throttled_repeat(self) -> None:
        self.layout = {"root": {"path": ".", "git": False, "origin": ""}, "repos": []}
        self.reporter.after_step(self.slot, None)
        self.ticks.now += 31
        self.reporter.after_step(self.slot, None)
        self.slot.phase = Phase.RUNNING
        self.reporter.after_step(self.slot, None)

        self.assertEqual(2, len(self.layouts))
        self.assertIsNone(self.client.slot_reports[1].layout)
        self.assertIsNotNone(self.client.slot_reports[2].layout)

    def test_a_layout_that_could_not_be_read_is_retried_with_the_next_report(self) -> None:
        results: list[dict[str, Any] | None] = [None, {"root": {"path": ".", "git": True, "origin": "h/x"}, "repos": []}]
        reporter = SlotStateReporter(self.client, LOG, 30.0, self.ticks, lambda _root: results.pop(0))

        reporter.after_step(self.slot, None)
        self.ticks.now += 31
        reporter.after_step(self.slot, None)

        self.assertIsNone(self.client.slot_reports[0].layout)
        self.assertEqual("h/x", (self.client.slot_reports[1].layout or {})["root"]["origin"])

    def test_a_layout_whose_report_failed_is_sent_with_the_next_one_even_after_an_earlier_success(self) -> None:
        self.layout = {"root": {"path": ".", "git": False, "origin": ""}, "repos": []}
        self.reporter.after_step(self.slot, None)
        self.slot.phase = Phase.RUNNING
        self.client.fail_once["report_slot_state"] = ApiError("503")
        self.reporter.after_step(self.slot, None)
        self.ticks.now += 31
        self.reporter.after_step(self.slot, None)

        self.assertIsNotNone(self.client.slot_reports[2].layout)

    def test_a_layout_that_raises_does_not_stop_the_report(self) -> None:
        def boom(_root: Path) -> dict[str, Any] | None:
            raise OSError("gone")

        SlotStateReporter(self.client, LOG, 30.0, self.ticks, boom).after_step(self.slot, None)

        self.assertIsNone(self.client.slot_reports[0].layout)

    def test_a_report_carries_phase_age_ticket_and_error_class_but_never_the_message(self) -> None:
        self.reporter.after_step(self.slot, ApiError(f"401 for token {SECRET} on ticket body xyz"))

        self.assertEqual([SlotSnapshot("idle", 5.0, 7, "api_error", 1, bundle_hash=self.bundle)], self.client.slot_reports)
        self.assertNotIn(SECRET, repr(self.client.slot_reports))
        self.assertNotIn("xyz", repr(self.client.slot_reports))

    def test_the_reporter_sends_the_snapshots_pause_reason_on(self) -> None:
        class Paused(StubSlot):
            def snapshot(self) -> SlotSnapshot:
                return SlotSnapshot("paused", 5.0, None, pause_reason="push refused")

        self.reporter.after_step(Paused(Phase.PAUSED), None)

        self.assertEqual([SlotSnapshot("paused", 5.0, None, pause_reason="push refused", bundle_hash=self.bundle)], self.client.slot_reports)

    def test_the_streak_counts_the_same_class_and_resets_on_success_or_a_new_class(self) -> None:
        self.reporter.after_step(self.slot, ApiError())
        self.reporter.after_step(self.slot, ApiError())
        self.reporter.after_step(self.slot, RuntimeError())
        self.reporter.after_step(self.slot, None)

        got = [(r.error_class, r.error_streak) for r in self.client.slot_reports]
        # The second same-class step is inside the throttle window; the class changes are not.
        self.assertEqual([("api_error", 1), ("runtime_error", 1), ("", 0)], got)

    def test_reports_are_throttled_unless_the_phase_or_error_class_changed(self) -> None:
        self.reporter.after_step(self.slot, None)
        self.ticks.now += 29
        self.reporter.after_step(self.slot, None)
        self.assertEqual(1, len(self.client.slot_reports))

        self.slot.phase = Phase.RUNNING
        self.reporter.after_step(self.slot, None)
        self.assertEqual(2, len(self.client.slot_reports))

        self.ticks.now += 29
        self.reporter.after_step(self.slot, ApiError())
        self.assertEqual(3, len(self.client.slot_reports))

        self.ticks.now += 29
        self.reporter.after_step(self.slot, ApiError())
        self.assertEqual(3, len(self.client.slot_reports))
        self.ticks.now += 1
        self.reporter.after_step(self.slot, ApiError())
        self.assertEqual(3, self.client.slot_reports[-1].error_streak)
        self.assertEqual(4, len(self.client.slot_reports))

    def test_a_failed_report_is_logged_and_does_not_raise(self) -> None:
        self.client.fail_once["report_slot_state"] = OSError(SECRET)

        with self.assertLogs(LOG, "WARNING") as logs:
            self.reporter.after_step(self.slot, None)
            self.reporter.after_step(self.slot, ApiError())

        self.assertEqual(2, len(self.client.slot_reports))
        self.assertNotIn(SECRET, "\n".join(logs.output))


class WedgedSlotTest(unittest.TestCase):
    """A RUNNING slot with no run, so every step raises and none reaches a poll."""

    def test_a_slot_whose_every_step_raises_still_reports_a_growing_streak(self) -> None:
        rig = Rig()
        slot = rig.slot
        slot.phase = Phase.RUNNING
        stop = threading.Event()
        reporter = SlotStateReporter(rig.client, LOG, 0.0)
        scheduler = Scheduler(
            [slot], stop, LOG, SchedulerConfig(idle_gap=0.0, error_backoff=0.0, drain_timeout=0.05), reporter=reporter
        )
        done = threading.Event()
        original = rig.client.report_slot_state

        def report(agent: str, snapshot: SlotSnapshot) -> None:
            original(agent, snapshot)
            if snapshot.error_streak >= 3:
                stop.set()
                done.set()

        rig.client.report_slot_state = report
        thread = threading.Thread(target=scheduler.run, daemon=True)

        with self.assertLogs(LOG, "ERROR"):
            thread.start()
            thread.join(10)

        self.assertTrue(done.is_set())
        self.assertEqual(0, rig.client.count("poll"))
        streaks = [r.error_streak for r in rig.client.slot_reports]
        self.assertEqual([1, 2, 3], streaks[:3])
        self.assertEqual({("running", "runtime_error")}, {(r.phase, r.error_class) for r in rig.client.slot_reports})

    def test_a_claimed_slot_reports_its_ticket_and_how_long_it_has_been_in_the_phase(self) -> None:
        rig = Rig()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.script_run(None)
        rig.clock.advance(500)
        rig.slot.step()
        rig.clock.advance(90)

        snap = rig.slot.snapshot()

        self.assertEqual(("running", 7), (snap.phase, snap.ticket_id))
        self.assertEqual(90.0, snap.phase_age_seconds)

    def test_a_paused_slot_reports_why_it_is_paused_and_an_unpaused_one_does_not(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "the slot checkout has uncommitted changes")
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.slot.step()
        rig.clock.advance(90)

        snap = rig.slot.snapshot()

        self.assertEqual("paused", snap.phase)
        self.assertIn("uncommitted changes", snap.pause_reason)
        self.assertEqual(90.0, snap.phase_age_seconds)
        rig.slot.phase = Phase.IDLE
        self.assertEqual("", rig.slot.snapshot().pause_reason)

    def test_the_age_counts_from_the_last_change_of_phase(self) -> None:
        rig = Rig()
        rig.clock.advance(500)
        rig.slot.phase = Phase.WAIT_LOCK
        rig.clock.advance(7)

        self.assertEqual(7.0, rig.slot.snapshot().phase_age_seconds)

    def test_looking_again_in_the_same_phase_does_not_restart_its_age(self) -> None:
        rig = Rig()
        rig.clock.advance(5)
        rig.slot.phase = Phase.WAIT_LOCK
        rig.clock.advance(60)
        rig.slot.phase = Phase.WAIT_LOCK

        self.assertEqual(60.0, rig.slot.snapshot().phase_age_seconds)

    def test_a_failing_report_does_not_stop_the_slot_being_stepped(self) -> None:
        rig = Rig()
        rig.client.fail_once["report_slot_state"] = OSError("down")
        stop = threading.Event()
        steps: list[int] = []

        class Counting(StubSlot):
            def step(self) -> float:
                steps.append(1)
                if len(steps) == 3:
                    stop.set()
                return 0.0

        scheduler = Scheduler(
            [Counting()], stop, LOG, SchedulerConfig(idle_gap=0.0, drain_timeout=1000.0), reporter=SlotStateReporter(rig.client, LOG, 0.0)
        )

        with self.assertLogs(LOG, "WARNING"):
            scheduler.run()

        self.assertEqual(3, len(steps))
