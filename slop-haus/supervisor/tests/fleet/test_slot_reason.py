from __future__ import annotations

import unittest

from fakes import Rig
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.slophaus.client import ChecksState, Event, EventKind, LockState, WorkerState
from slop_haus_supervisor.workspace.handover import Prepared
from test_slot import FINISHED, started


class SlotReasonTest(unittest.TestCase):
    """Every phase a slot can wait in says why in its snapshot, so an operator need not read logs."""

    def assert_reason(self, rig: Rig, phase: Phase, reason: Reason | None) -> None:
        self.assertEqual(phase, rig.slot.phase)
        snapshot = rig.slot.snapshot()
        self.assertEqual(reason.value if reason else "", snapshot.reason)

    def test_a_slot_that_has_not_polled_is_starting(self) -> None:
        self.assert_reason(Rig(), Phase.IDLE, Reason.STARTING)

    def test_an_idle_slot_whose_poll_found_nothing(self) -> None:
        rig = Rig()
        rig.slot.step()
        self.assert_reason(rig, Phase.IDLE, Reason.NO_CLAIMABLE)

    def test_a_disabled_server_is_a_paused_fleet(self) -> None:
        rig = Rig()
        rig.client.events = [[Event(EventKind.DISABLED)]]
        rig.slot.step()
        self.assert_reason(rig, Phase.IDLE, Reason.FLEET_PAUSED)

    def test_a_restart_request_is_an_update(self) -> None:
        rig = Rig()
        rig.client.events = [[Event(EventKind.RESTART)]]
        rig.slot.step()
        self.assert_reason(rig, Phase.IDLE, Reason.UPDATING)

    def test_a_running_slot_gives_no_reason(self) -> None:
        rig = Rig()
        started(rig, None)
        self.assert_reason(rig, Phase.RUNNING, None)

    def test_a_paused_slot(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "the slot checkout has uncommitted changes")
        started(rig, None)
        self.assert_reason(rig, Phase.PAUSED, Reason.SLOT_PAUSED)

    def test_queued_behind_the_lock_names_the_ticket(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        self.assert_reason(rig, Phase.WAIT_LOCK, Reason.LOCK_QUEUED)
        self.assertEqual("ticket #7", rig.slot.snapshot().reason_detail)

    def test_waiting_for_checks(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING))
        rig.slot.step()
        self.assert_reason(rig, Phase.WAIT_CHECKS, Reason.WAITING_CI)
        self.assertEqual("ticket #7", rig.slot.snapshot().reason_detail)

    def test_backing_off_after_a_crash(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.CRASHED))
        rig.slot.step()
        self.assert_reason(rig, Phase.BACKOFF, Reason.BACKOFF_AFTER_ERROR)
        self.assertEqual("ticket #7", rig.slot.snapshot().reason_detail)

    def test_waiting_for_a_usage_limit_to_reset_is_a_cooldown(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.USAGE_LIMIT, reset_at=rig.clock.now() + 3600))
        rig.slot.step()
        self.assert_reason(rig, Phase.WAIT_RESET, Reason.COOLDOWN)
        self.assertEqual("ticket #7", rig.slot.snapshot().reason_detail)


if __name__ == "__main__":
    unittest.main()
