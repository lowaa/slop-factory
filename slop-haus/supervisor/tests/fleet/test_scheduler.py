from __future__ import annotations

import logging
import tempfile
import threading
import time
import unittest
from collections.abc import Callable
from pathlib import Path

from fakes import FakeSlot
from slop_haus_supervisor.fleet.scheduler import STEWARD_BACKOFF_CAP, Scheduler, SchedulerConfig, backed_off
from slop_haus_supervisor.fleet.slot import Phase
from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.service.control import ControlSpool
from slop_haus_supervisor.slophaus.http_client import ApiStopped
from slop_haus_supervisor.slophaus.transport import ApiError

LOG = logging.getLogger("test")


FAST = SchedulerConfig(idle_gap=0.0, error_backoff=0.0, drain_timeout=1000.0)


class SchedulerTest(unittest.TestCase):
    def run_until_done(self, scheduler: Scheduler) -> None:
        thread = threading.Thread(target=scheduler.run, daemon=True)
        thread.start()
        thread.join(10)
        self.assertFalse(thread.is_alive(), "the scheduler did not stop")

    def test_a_failing_step_does_not_end_the_loop(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a")
        slot.on_step = [RuntimeError("boom"), stop.set]
        scheduler = Scheduler([slot], stop, LOG, FAST)

        with self.assertLogs(LOG, "ERROR"):
            self.run_until_done(scheduler)

        self.assertEqual(2, slot.steps)

    def test_every_slot_is_stepped_on_its_own_thread(self) -> None:
        stop = threading.Event()
        first, second = FakeSlot("a"), FakeSlot("b")
        # The first only returns once the second has stepped, so a single thread would hang.
        first.on_step = [lambda: second.stepped.wait(10), stop.set]
        scheduler = Scheduler([first, second], stop, LOG, FAST)

        self.run_until_done(scheduler)

        self.assertTrue(second.stepped.is_set())

    def test_draining_stops_idle_slots_without_another_step(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a")
        slot.on_step = [stop.set]
        scheduler = Scheduler([slot], stop, LOG, FAST)

        self.run_until_done(scheduler)

        self.assertEqual(1, slot.steps)

    def test_a_slot_stopped_at_a_wait_point_releases_its_lock_once(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a", Phase.WAIT_CHECKS)
        slot.on_step = [stop.set]
        scheduler = Scheduler([slot], stop, LOG, FAST)

        self.run_until_done(scheduler)

        self.assertEqual(1, slot.lock_released)

    def test_a_slot_reviewing_at_a_stop_has_its_review_ended_at_once(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a", Phase.WAIT_REVIEW)
        slot.on_step = [stop.set]
        scheduler = Scheduler([slot], stop, LOG, FAST)

        self.run_until_done(scheduler)

        self.assertEqual(1, slot.steps)
        self.assertTrue(slot.abandoned.is_set())
        self.assertEqual(0, slot.lock_released, "abandoning releases the lock itself")

    def test_draining_lets_a_running_slot_reach_a_wait_point(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a", Phase.RUNNING)

        def reach_wait() -> None:
            slot.phase = Phase.WAIT_CHECKS

        slot.on_step = [stop.set, 0.0, reach_wait]
        scheduler = Scheduler([slot], stop, LOG, FAST)

        self.run_until_done(scheduler)

        self.assertEqual(3, slot.steps)

    def test_a_run_that_never_settles_is_left_after_the_drain_timeout(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a", Phase.RUNNING)
        slot.on_step = [stop.set]
        now = iter([0.0, 5.0, 11.0, 11.0, 11.0])
        scheduler = Scheduler([slot], stop, LOG, SchedulerConfig(0.0, 0.0, 10.0, 0.0), lambda: next(now))

        self.run_until_done(scheduler)

        self.assertEqual(2, slot.steps)
        self.assertTrue(slot.abandoned.is_set())

    def test_an_abort_ends_the_runs_without_waiting_for_the_drain_timeout(self) -> None:
        stop = threading.Event()
        release = threading.Event()
        self.addCleanup(release.set)
        slot = FakeSlot("a", Phase.RUNNING)
        scheduler = Scheduler([slot], stop, LOG, SchedulerConfig(0.0, 0.0, 1000.0, 1000.0))
        slot.on_step = [lambda: (scheduler.drain(), scheduler.abort(), release.wait(30))]

        with self.assertLogs(LOG, "WARNING"):
            self.run_until_done(scheduler)

        self.assertTrue(slot.abandoned.is_set())

    def test_shutdown_during_a_call_ends_that_slot(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a")
        slot.on_step = [ApiStopped()]

        self.run_until_done(Scheduler([slot], stop, LOG, FAST))

        self.assertEqual(1, slot.steps)

    def test_an_idle_slot_whose_poll_returns_at_once_is_held_back(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a")
        waits: list[float] = []
        real_wait = stop.wait

        class Spy(threading.Event):
            def wait(self, timeout: float | None = None) -> bool:
                waits.append(timeout or 0.0)
                return real_wait(0)

        spy = Spy()
        slot.on_step = [0.0, spy.set]
        self.run_until_done(Scheduler([slot], spy, LOG, SchedulerConfig(idle_gap=2.0, error_backoff=0.0)))

        self.assertEqual([2.0], waits)

    def test_a_step_stuck_in_a_call_does_not_hold_the_exit_forever(self) -> None:
        stop = threading.Event()
        release = threading.Event()
        self.addCleanup(release.set)
        slot = FakeSlot("a", Phase.RUNNING)
        slot.on_step = [lambda: (stop.set(), release.wait(30))]
        scheduler = Scheduler([slot], stop, LOG, SchedulerConfig(0.0, 0.0, 0.0, 0.0))

        with self.assertLogs(LOG, "WARNING"):
            self.run_until_done(scheduler)

    def test_a_failed_step_waits_the_error_backoff(self) -> None:
        waits: list[float] = []

        class Spy(threading.Event):
            def wait(self, timeout: float | None = None) -> bool:
                waits.append(timeout or 0.0)
                self.set()
                return True

        slot = FakeSlot("a")
        slot.on_step = [RuntimeError("boom")]
        with self.assertLogs(LOG, "ERROR"):
            self.run_until_done(Scheduler([slot], Spy(), LOG, SchedulerConfig(idle_gap=0.0, error_backoff=7.0)))

        self.assertEqual([7.0], waits)


class UnpauseRequestTest(unittest.TestCase):
    def test_named_slots_are_asked_manually_and_the_answers_returned(self) -> None:
        paused, other = FakeSlot("a", Phase.PAUSED), FakeSlot("b", Phase.PAUSED)
        other.unpause_answer = "still dirty"
        scheduler = Scheduler([paused, other], threading.Event(), LOG, FAST)

        answers = scheduler.unpause(["a", "b", "ghost"])

        self.assertEqual({"a": "", "b": "still dirty", "ghost": "no such slot"}, answers)
        self.assertEqual([True], paused.unpaused_manually)

    def test_all_skips_slots_that_are_not_paused(self) -> None:
        paused, running = FakeSlot("a", Phase.PAUSED), FakeSlot("b", Phase.RUNNING)
        answers = Scheduler([paused, running], threading.Event(), LOG, FAST).unpause(None)

        self.assertEqual({"a": "", "b": ""}, answers)
        self.assertFalse(hasattr(running, "unpaused_manually"))

    def test_a_named_slot_that_is_not_paused_says_so(self) -> None:
        answers = Scheduler([FakeSlot("a")], threading.Event(), LOG, FAST).unpause(["a"])
        self.assertEqual({"a": "not paused"}, answers)

    def test_the_running_scheduler_acts_on_requests_and_republishes_the_paused_slots(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            spool = ControlSpool(Path(tmp) / "control")
            paused_file = Path(tmp) / "paused.json"
            slot = FakeSlot("a", Phase.PAUSED)
            slot.info = ("dirty", 12.0)
            stop = threading.Event()
            scheduler = Scheduler([slot], stop, LOG, FAST, control=spool, paused_file=paused_file)
            thread = threading.Thread(target=scheduler.run, daemon=True)
            thread.start()
            try:
                self.wait_for(lambda: paused_state.read(paused_file) == [paused_state.PausedSlot("a", "dirty", 12.0)])
                slot.unpause_answer = ""
                slot.on_try_unpause = lambda: setattr(slot, "info", None)
                answers = spool.request_unpause(["a"], timeout=10)
                self.wait_for(lambda: paused_file.exists() and paused_state.read(paused_file) == [])
            finally:
                stop.set()
                thread.join(10)

            self.assertEqual({"a": ""}, answers)
            self.assertEqual([True], slot.unpaused_manually)

    @staticmethod
    def wait_for(condition: Callable[[], bool]) -> None:
        deadline = time.monotonic() + 10
        while not condition():
            if time.monotonic() > deadline:
                raise AssertionError("condition not reached")
            time.sleep(0.01)


class FakeSteward:
    def __init__(self) -> None:
        self.ticks = 0
        self.stopped = threading.Event()
        self.on_tick: list[object] = []
        self.ticked = threading.Event()

    def tick(self) -> object:
        self.ticks += 1
        self.ticked.set()
        action = self.on_tick.pop(0) if self.on_tick else None
        if isinstance(action, BaseException):
            raise action
        if callable(action):
            action()
        return None

    def stop(self) -> None:
        self.stopped.set()


class RecordingStop(threading.Event):
    """A stop event whose waits return at once and are recorded, so a backoff is seen without sleeping."""

    def __init__(self) -> None:
        super().__init__()
        self.waits: list[float] = []

    def wait(self, timeout: float | None = None) -> bool:
        if timeout is not None:
            self.waits.append(timeout)
        return self.is_set()


class StewardSchedulerTest(SchedulerTest):
    def test_the_wait_backs_off_on_503_and_resets_after_a_good_tick(self) -> None:
        stop = RecordingStop()
        steward = FakeSteward()
        down = ApiError("the steward is not available", 503)
        steward.on_tick = [down, down, None, down, stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, steward_tick=15.0), steward=steward)

        with self.assertLogs(LOG, "INFO") as logs:
            self.run_until_done(scheduler)

        self.assertEqual([30.0, 60.0, 15.0, 30.0], [w for w in stop.waits if w > 0][:4])
        self.assertEqual(["WARNING", "INFO", "WARNING", "INFO"], [r.levelname for r in logs.records])

    def test_the_steward_is_ticked_and_a_failing_tick_does_not_end_its_loop(self) -> None:
        stop = threading.Event()
        steward = FakeSteward()
        steward.on_tick = [RuntimeError("boom"), stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, steward_tick=0.0), steward=steward)

        with self.assertLogs(LOG, "ERROR"):
            self.run_until_done(scheduler)

        self.assertEqual(2, steward.ticks)

    def test_an_unavailable_steward_is_logged_once_without_a_traceback(self) -> None:
        stop = threading.Event()
        steward = FakeSteward()
        unavailable = ApiError("could not read the steward's board events: the steward is not available", 503)
        steward.on_tick = [unavailable, unavailable, unavailable, stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, steward_tick=0.0), steward=steward)

        with self.assertLogs(LOG, "WARNING") as logs:
            self.run_until_done(scheduler)

        self.assertEqual(1, len(logs.records))
        self.assertIsNone(logs.records[0].exc_info)

    def test_the_wait_doubles_up_to_a_cap(self) -> None:
        pause, seen = 15.0, list[float]()
        for _ in range(10):
            pause = backed_off(pause)
            seen.append(pause)
        self.assertEqual([30.0, 60.0, 120.0, 240.0, 480.0], seen[:5])
        self.assertEqual(STEWARD_BACKOFF_CAP, seen[-1])

    def test_an_api_error_that_is_not_a_503_keeps_its_traceback(self) -> None:
        stop = threading.Event()
        steward = FakeSteward()
        steward.on_tick = [ApiError("bad", 500), stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, steward_tick=0.0), steward=steward)

        with self.assertLogs(LOG, "ERROR") as logs:
            self.run_until_done(scheduler)

        self.assertIsNotNone(logs.records[0].exc_info)

    def test_stopping_ends_the_stewards_run(self) -> None:
        stop = threading.Event()
        steward = FakeSteward()
        steward.on_tick = [stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, steward_tick=0.0), steward=steward)

        self.run_until_done(scheduler)

        self.assertTrue(steward.stopped.wait(10))


class FakeReconciler:
    def __init__(self) -> None:
        self.on_tick: list[object] = []
        self.ticks = 0

    def tick(self) -> None:
        self.ticks += 1
        action = self.on_tick.pop(0) if self.on_tick else None
        if isinstance(action, BaseException):
            raise action
        if callable(action):
            action()


class DynamicSlotsTest(unittest.TestCase):
    run_until_done = SchedulerTest.run_until_done

    def test_the_reconciler_is_ticked_and_a_slot_it_adds_is_stepped(self) -> None:
        stop = threading.Event()
        reconciler = FakeReconciler()
        added = FakeSlot("late")
        added.on_step = [stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, reconcile_tick=0.0), reconciler=reconciler)
        reconciler.on_tick = [lambda: scheduler.add_slot(added)]

        self.run_until_done(scheduler)

        self.assertTrue(added.stepped.is_set())
        self.assertEqual({"a", "late"}, scheduler.slot_names())

    def test_a_failing_tick_does_not_end_the_reconcile_loop(self) -> None:
        stop = threading.Event()
        reconciler = FakeReconciler()
        reconciler.on_tick = [RuntimeError("boom"), stop.set]
        scheduler = Scheduler([FakeSlot("a")], stop, LOG, SchedulerConfig(idle_gap=0.0, reconcile_tick=0.0), reconciler=reconciler)

        with self.assertLogs(LOG, "ERROR"):
            self.run_until_done(scheduler)

        self.assertEqual(2, reconciler.ticks)

    def test_a_held_idle_slot_takes_no_step_until_released(self) -> None:
        stop = threading.Event()
        slot, other = FakeSlot("a"), FakeSlot("b")
        scheduler = Scheduler([slot, other], stop, LOG, FAST)
        scheduler.hold("a", True)
        stepped_while_held: list[int] = []

        def release() -> None:
            stepped_while_held.append(slot.steps)
            scheduler.hold("a", False)
            slot.stepped.wait(10)
            stop.set()

        # Runs on b's thread once a's loop has had every chance to step.
        other.on_step = [release]

        self.run_until_done(scheduler)

        self.assertEqual([0], stepped_while_held)
        self.assertGreaterEqual(slot.steps, 1)

    def test_a_retired_idle_slot_stops_releases_its_lease_and_is_forgotten(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a")
        scheduler = Scheduler([slot], stop, LOG, FAST)
        thread = threading.Thread(target=scheduler.run, daemon=True)
        thread.start()
        slot.stepped.wait(10)

        while not scheduler.retire("a"):
            threading.Event().wait(0.01)

        self.assertEqual(set(), scheduler.slot_names())
        self.assertEqual(1, slot.lock_released)
        stop.set()
        thread.join(10)

    def test_a_retiring_slot_that_is_running_is_waited_for(self) -> None:
        stop = threading.Event()
        slot = FakeSlot("a", Phase.RUNNING)
        release = threading.Event()
        slot.on_step = [lambda: release.wait(10)]
        scheduler = Scheduler([slot], stop, LOG, SchedulerConfig(idle_gap=0.0, drain_timeout=1000.0))
        thread = threading.Thread(target=scheduler.run, daemon=True)
        thread.start()
        slot.stepped.wait(10)

        self.assertFalse(scheduler.retire("a"))
        self.assertEqual({"a"}, scheduler.slot_names())
        slot.phase = Phase.IDLE
        release.set()
        while not scheduler.retire("a"):
            threading.Event().wait(0.01)
        stop.set()
        thread.join(10)
