from __future__ import annotations

import unittest

from fakes import FakeAlerts, FakeClock, FakeSlopHaus
from slop_haus_supervisor.fleet.lease_watch import LeaseWatch
from slop_haus_supervisor.fleet.policy import PolicyConfig
from slop_haus_supervisor.slophaus.client import ChecksState, Event, EventKind, LockState, WorkerState

HELD = WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING)


class LeaseWatchTest(unittest.TestCase):
    def watch(self, config: PolicyConfig | None = None) -> LeaseWatch:
        self.clock = FakeClock()
        self.client = FakeSlopHaus()
        self.alerts = FakeAlerts()
        self.config = config or PolicyConfig()
        return LeaseWatch("agent-1", self.client, self.alerts, self.config, self.clock)

    def test_the_slow_queue_alert_comes_once_after_the_threshold(self) -> None:
        watch = self.watch(PolicyConfig(lock_alert_after=45 * 60))
        self.client.states = [HELD]
        for _ in range(44):
            watch.observe()
            self.clock.advance(60)
        self.assertEqual([], self.alerts.messages)
        for _ in range(8):
            watch.observe()
            self.clock.advance(60)
        self.assertEqual(1, len(self.alerts.messages))
        self.assertEqual("agent-1 has held the integration lock for 45 minutes: slow queue?", self.alerts.messages[0])

    def test_the_threshold_is_configurable(self) -> None:
        watch = self.watch(PolicyConfig(lock_alert_after=120))
        self.client.states = [HELD]
        for _ in range(4):
            watch.observe()
            self.clock.advance(60)
        self.assertEqual(1, len(self.alerts.messages))

    def test_letting_go_of_the_lock_rearms_the_alert(self) -> None:
        watch = self.watch(PolicyConfig(lock_alert_after=60))
        self.client.states = [HELD]
        watch.observe()
        self.clock.advance(60)
        watch.observe()
        self.assertEqual(1, len(self.alerts.messages))
        self.client.states = [WorkerState(True)]
        watch.observe()
        self.assertIsNone(watch.lock_held_since)
        self.client.states = [HELD]
        watch.observe()
        self.clock.advance(60)
        watch.observe()
        self.assertEqual(2, len(self.alerts.messages))

    def test_starting_a_ticket_forgets_the_last_one(self) -> None:
        watch = self.watch()
        watch.message_pending = True
        watch.lock_taken()
        self.clock.advance(5)
        watch.start()
        self.assertFalse(watch.message_pending)
        self.assertIsNone(watch.lock_held_since)
        self.assertEqual(self.clock.now(), watch.last_heartbeat)

    def test_heartbeats_only_once_the_interval_has_passed(self) -> None:
        watch = self.watch()
        self.client.states = [WorkerState(True)]
        watch.start()
        self.assertTrue(watch.maintain())
        self.assertEqual(0, self.client.count("heartbeat"))
        self.clock.advance(self.config.heartbeat_interval)
        self.assertTrue(watch.maintain())
        self.assertEqual(1, self.client.count("heartbeat"))
        self.assertEqual(self.clock.now() + self.config.heartbeat_interval, watch.heartbeat_due_in())

    def test_a_lease_gone_from_the_server_is_not_heartbeaten(self) -> None:
        watch = self.watch()
        self.client.states = [WorkerState(False, "done")]
        watch.start()
        self.clock.advance(self.config.heartbeat_interval)
        self.assertFalse(watch.maintain())
        self.assertEqual(0, self.client.count("heartbeat"))

    def test_a_revocation_is_returned_and_a_message_is_kept_until_taken(self) -> None:
        watch = self.watch()
        self.client.events = [[Event(EventKind.MESSAGE), Event(EventKind.LEASE_REVOKED)]]
        self.assertTrue(watch.revoked())
        self.assertTrue(watch.take_message())
        self.assertFalse(watch.take_message())
        self.assertFalse(watch.revoked())
