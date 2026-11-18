from __future__ import annotations

import logging
import unittest

from fakes import FakeAlerts, FakeClock
from slop_haus_supervisor.fleet.pause import PAUSE_RECHECK_INTERVAL, PauseState
from slop_haus_supervisor.fleet.phase import Phase
from slop_haus_supervisor.fleet.slot_log import SlotLog
from slop_haus_supervisor.harness.text import SecretMasker

LOGGER = logging.getLogger("test.pause")


class Host:
    """Stands in for the slot: a pause only reads and writes its phase."""

    def __init__(self, clock: FakeClock) -> None:
        self._clock = clock
        self._phase = Phase.IDLE
        self.phase_since = clock.now()

    @property
    def phase(self) -> Phase:
        return self._phase

    @phase.setter
    def phase(self, value: Phase) -> None:
        self._phase = value
        self.phase_since = self._clock.now()


class PauseStateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock()
        self.alerts = FakeAlerts()
        self.host = Host(self.clock)
        log = SlotLog("agent-1", LOGGER, SecretMasker(), self.clock)
        self.pause = PauseState(self.host, log, self.alerts, self.clock, PAUSE_RECHECK_INTERVAL)
        self.cause = "dirty"

    def check(self) -> str:
        return self.cause

    def test_beginning_pauses_the_slot_and_tells_a_person(self) -> None:
        with self.assertLogs(LOGGER, level=logging.WARNING) as captured:
            self.pause.begin("dirty", self.check)
        self.assertEqual(Phase.PAUSED, self.host.phase)
        self.assertEqual(["agent-1 paused: dirty"], self.alerts.messages)
        self.assertEqual(["slot=agent-1 paused: dirty"], [r.getMessage() for r in captured.records])

    def test_the_cause_is_looked_at_again_only_after_the_interval(self) -> None:
        self.pause.begin("dirty", self.check)
        self.clock.advance(PAUSE_RECHECK_INTERVAL - 1)
        self.assertFalse(self.pause.recheck_due())
        self.assertEqual(1.0, self.pause.seconds_to_recheck())
        self.clock.advance(1)
        self.assertTrue(self.pause.recheck_due())

    def test_a_cleared_cause_unpauses_by_itself(self) -> None:
        self.pause.begin("dirty", self.check)
        self.cause = ""
        with self.assertLogs(LOGGER, level=logging.INFO) as captured:
            self.assertEqual("", self.pause.try_unpause())
        self.assertEqual(Phase.IDLE, self.host.phase)
        self.assertEqual(["slot=agent-1 unpaused: dirty cleared"], [r.getMessage() for r in captured.records])

    def test_a_remaining_cause_keeps_the_pause_and_schedules_another_look(self) -> None:
        self.pause.begin("dirty", self.check)
        self.clock.advance(PAUSE_RECHECK_INTERVAL)
        self.assertEqual("dirty", self.pause.try_unpause())
        self.assertEqual(Phase.PAUSED, self.host.phase)
        self.assertFalse(self.pause.recheck_due())

    def test_a_cause_that_cannot_be_checked_only_ends_on_a_request(self) -> None:
        self.pause.begin("the login failed")
        self.assertEqual("the login failed", self.pause.try_unpause())
        self.assertEqual(Phase.PAUSED, self.host.phase)
        self.assertEqual("", self.pause.try_unpause(manual=True))
        self.assertEqual(Phase.IDLE, self.host.phase)

    def test_a_request_does_not_override_a_cause_that_remains(self) -> None:
        self.pause.begin("dirty", self.check)
        self.assertEqual("dirty", self.pause.try_unpause(manual=True))
        self.assertEqual(Phase.PAUSED, self.host.phase)

    def test_a_failing_check_keeps_the_pause(self) -> None:
        def boom() -> str:
            raise OSError("git gone")

        self.pause.begin("dirty", boom)
        self.assertIn("OSError", self.pause.try_unpause(manual=True))
        self.assertEqual(Phase.PAUSED, self.host.phase)

    def test_unpausing_a_slot_that_is_not_paused_does_nothing(self) -> None:
        self.assertEqual("", self.pause.try_unpause(manual=True))
        self.pause.unpause()
        self.assertEqual(Phase.IDLE, self.host.phase)
        self.assertIsNone(self.pause.info())

    def test_info_is_masked_one_line_and_gone_once_unpaused(self) -> None:
        self.pause.begin("git fetch failed: https://user:tok@host/r.git\nrefused")
        paused_at = self.host.phase_since
        info = self.pause.info()
        assert info is not None
        self.assertNotIn("tok", info[0])
        self.assertNotIn("\n", info[0])
        self.assertEqual(paused_at, info[1])
        self.pause.unpause()
        self.assertIsNone(self.pause.info())
