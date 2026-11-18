from __future__ import annotations

import threading
import unittest
from typing import Any

from slop_haus_supervisor.remote_control.host.lease import ACQUIRE, RELEASE, RENEW, HostLease, LeaseRefused
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable

TTL = 90.0


class FakeServer:
    """The lease endpoints as the host sees them: answers what the test queued for each path, and records the calls."""

    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.answers: dict[str, list[Exception | None]] = {}

    def post(self, path: str, body: dict[str, Any], timeout: float) -> dict[str, Any]:
        self.calls.append((path, body))
        queued = self.answers.get(path)
        outcome = queued.pop(0) if queued else None
        if outcome is not None:
            raise outcome
        return {"ttl_seconds": TTL, "expires_at": "x"}


class HostLeaseTest(unittest.TestCase):
    def setUp(self) -> None:
        self.now = 1000.0
        self.server = FakeServer()
        self.stop = threading.Event()
        self.lease = HostLease(self.server, "holder-1", self.stop, lambda: self.now)

    def test_a_second_host_is_refused_while_the_lease_is_live(self) -> None:
        self.server.answers[ACQUIRE] = [ServerError(409, "another host holds this agent's lease")]

        with self.assertRaises(LeaseRefused):
            self.lease.acquire()

        self.assertEqual({"holder": "holder-1"}, self.server.calls[0][1])
        self.assertFalse(self.stop.is_set())

    def test_renewals_are_tried_several_times_per_ttl(self) -> None:
        self.lease.acquire()

        self.assertEqual(TTL / 3, self.lease.renew_interval)

    def test_a_missed_renewal_the_lease_outlives_does_not_stop_the_host(self) -> None:
        self.lease.acquire()
        self.server.answers[RENEW] = [Unreachable("down")]
        self.now += 30

        self.assertTrue(self.lease.renew_once())
        self.assertFalse(self.stop.is_set())

    def test_a_missed_renewal_stops_the_host_once_the_lease_has_run_out(self) -> None:
        self.lease.acquire()
        self.server.answers[RENEW] = [Unreachable("down")]
        self.now += TTL

        self.assertFalse(self.lease.renew_once())
        self.assertTrue(self.stop.is_set())

    def test_a_renewal_the_server_refuses_stops_the_host_at_once(self) -> None:
        self.lease.acquire()
        self.server.answers[RENEW] = [ServerError(409, "you do not hold a live host lease")]

        self.assertFalse(self.lease.renew_once())
        self.assertTrue(self.stop.is_set())

    def test_a_server_error_is_treated_as_a_missed_renewal(self) -> None:
        self.lease.acquire()
        self.server.answers[RENEW] = [ServerError(503, "busy")]
        self.now += 10

        self.assertTrue(self.lease.renew_once())
        self.assertFalse(self.stop.is_set())

    def test_a_good_renewal_extends_what_a_later_miss_may_rely_on(self) -> None:
        self.lease.acquire()
        self.now += 60
        self.assertTrue(self.lease.renew_once())
        self.server.answers[RENEW] = [Unreachable("down")]
        self.now += 60

        self.assertTrue(self.lease.renew_once())

    def test_release_tells_the_server_and_survives_its_failing(self) -> None:
        self.lease.acquire()
        self.server.answers[RELEASE] = [Unreachable("down")]

        self.lease.release()

        self.assertEqual(RELEASE, self.server.calls[-1][0])
