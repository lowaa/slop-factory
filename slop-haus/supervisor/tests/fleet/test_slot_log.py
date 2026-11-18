from __future__ import annotations

import logging
import unittest

from fakes import FakeClock
from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.fleet.slot_log import IDLE_LOG_INTERVAL, REFUSAL_LOG_INTERVAL, SlotLog, prompt_kind
from slop_haus_supervisor.harness.text import SecretMasker

LOGGER = logging.getLogger("test.slot_log")


class SlotLogTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock()
        self.log = SlotLog("agent-1", LOGGER, SecretMasker(["hunter2-secret"]), self.clock)

    def lines(self, action: object) -> list[str]:
        with self.assertLogs(LOGGER, level=logging.INFO) as captured:
            LOGGER.info("marker")
            action()  # pyright: ignore[reportCallIssue]
        return [r.getMessage() for r in captured.records][1:]

    def test_clean_masks_secrets_credentials_and_newlines(self) -> None:
        cleaned = self.log.clean("failed hunter2-secret https://u:tok@host/r\nnext")
        self.assertNotIn("hunter2-secret", cleaned)
        self.assertNotIn("tok", cleaned)
        self.assertNotIn("\n", cleaned)

    def test_clean_bounds_the_length(self) -> None:
        self.assertTrue(self.log.clean("x" * 1000).endswith("..."))
        self.assertLess(len(self.log.clean("x" * 1000)), 520)
        self.assertEqual("x" * 500, self.log.clean("x" * 500))

    def test_a_repeated_refusal_is_logged_once_per_window(self) -> None:
        first = self.lines(lambda: [self.log.refusal(7, "already claimed") for _ in range(3)])
        self.assertEqual(["slot=agent-1 claim of ticket #7 refused: already claimed"], first)
        self.clock.advance(REFUSAL_LOG_INTERVAL)
        self.assertEqual(1, len(self.lines(lambda: self.log.refusal(7, "already claimed"))))

    def test_a_refusal_is_remembered_as_the_last_poll(self) -> None:
        self.log.refusal(7, "already claimed")
        self.assertEqual("offered #7 and refused: already claimed", self.log.last_poll)

    def test_expired_refusals_are_forgotten(self) -> None:
        self.log.refusal(7, "a")
        self.clock.advance(REFUSAL_LOG_INTERVAL)
        self.log.refusal(7, "b")
        self.assertEqual(1, self.log.refusals_remembered)

    def test_the_idle_heartbeat_comes_once_per_interval_and_names_the_last_poll(self) -> None:
        self.log.last_poll = "no claimable work"
        self.assertEqual([], self.lines(self.log.idle_heartbeat))
        self.clock.advance(IDLE_LOG_INTERVAL)
        self.assertEqual(["slot=agent-1 idle; last poll: no claimable work"], self.lines(self.log.idle_heartbeat))
        self.assertEqual([], self.lines(self.log.idle_heartbeat))

    def test_entering_idle_restarts_the_heartbeat_interval(self) -> None:
        self.clock.advance(IDLE_LOG_INTERVAL)
        self.log.idle_entered()
        self.assertEqual([], self.lines(self.log.idle_heartbeat))

    def test_prompt_kinds(self) -> None:
        self.assertEqual("initial", prompt_kind("Work ticket #7 on branch b."))
        self.assertEqual("nudge", prompt_kind(policy.NUDGE_PROMPT))
        self.assertEqual("lock held", prompt_kind(policy.LOCK_HELD_PROMPT))
        self.assertEqual("message", prompt_kind(policy.MESSAGE_PROMPT))
        self.assertEqual("handover", prompt_kind(policy.HANDOVER_PROMPT))
        self.assertEqual("checks finished", prompt_kind("checks finished: failed"))
        self.assertEqual("other", prompt_kind("anything else"))
