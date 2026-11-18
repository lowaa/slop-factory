from __future__ import annotations

import unittest

from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.fleet.policy import ActionKind, AttemptHistory, PolicyConfig
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.slophaus.client import ChecksState, LockState, WorkerState

CFG = PolicyConfig()
NOW = 1000.0
LEASED = WorkerState(True)


def after(kind: OutcomeKind, state: WorkerState = LEASED, history: AttemptHistory | None = None, **kw: float):
    outcome = RunOutcome(kind, reset_at=kw.get("reset_at"))
    return policy.decide_after_run(CFG, NOW, outcome, state, history or AttemptHistory())


class StateTableTest(unittest.TestCase):
    def test_no_lease_frees_the_slot(self) -> None:
        for status in ("done", "review", "discussion", "preparing", "open"):
            d = policy.decide_from_state(CFG, WorkerState(False, status), AttemptHistory())
            self.assertEqual(d.action.kind, ActionKind.FREE, status)

    def test_lock_rows(self) -> None:
        def kind(lock: LockState, checks: ChecksState) -> ActionKind:
            return policy.decide_from_state(CFG, WorkerState(True, lock=lock, checks=checks), AttemptHistory()).action.kind

        self.assertEqual(kind(LockState.QUEUED, ChecksState.NONE), ActionKind.WAIT_LOCK)
        self.assertEqual(kind(LockState.OFFERED, ChecksState.NONE), ActionKind.TAKE_LOCK)
        self.assertEqual(kind(LockState.HELD, ChecksState.PENDING), ActionKind.WAIT_CHECKS)
        self.assertEqual(kind(LockState.HELD, ChecksState.PASSED), ActionKind.RESUME)
        self.assertEqual(kind(LockState.HELD, ChecksState.FAILED), ActionKind.RESUME)

    def test_pending_checks_wait_without_the_lock_and_cost_no_nudge(self) -> None:
        h = AttemptHistory()
        for _ in range(5):
            d = policy.decide_from_state(CFG, WorkerState(True, checks=ChecksState.PENDING), h)
            self.assertEqual(d.action.kind, ActionKind.WAIT_CHECKS)
            self.assertEqual(d.history.nudges, 0)
            h = d.history

    def test_finished_checks_without_the_lock_resume_with_the_prompt_within_the_nudge_cap(self) -> None:
        for checks in (ChecksState.PASSED, ChecksState.FAILED):
            state = WorkerState(True, checks=checks)
            # The first sight of the checks is the supervisor's own wake-up and costs nothing.
            d = policy.decide_from_state(CFG, state, AttemptHistory())
            self.assertEqual(d.action.kind, ActionKind.RESUME)
            self.assertEqual(d.action.prompt, f"checks finished: {checks.value}")
            self.assertEqual(d.history.nudges, 0)
            h = d.history
            for expected in (1, 2):
                d = policy.decide_from_state(CFG, state, h)
                self.assertEqual(d.action.kind, ActionKind.RESUME)
                self.assertEqual(d.action.prompt, f"checks finished: {checks.value}")
                self.assertEqual(d.history.nudges, expected)
                h = d.history
            self.assertEqual(policy.decide_from_state(CFG, state, h).action.kind, ActionKind.HANDOVER)

    def test_two_ci_rounds_outside_the_lock_are_not_a_stall(self) -> None:
        h = AttemptHistory()
        for lock, checks in (
            (LockState.HELD, ChecksState.NONE),
            (LockState.NONE, ChecksState.PENDING),
            (LockState.NONE, ChecksState.PASSED),
            (LockState.NONE, ChecksState.PENDING),
            (LockState.NONE, ChecksState.PASSED),
            (LockState.NONE, ChecksState.PENDING),
            (LockState.NONE, ChecksState.FAILED),
        ):
            d = policy.decide_from_state(CFG, WorkerState(True, lock=lock, checks=checks), h)
            expected = ActionKind.WAIT_CHECKS if checks == ChecksState.PENDING else ActionKind.RESUME
            if lock == LockState.HELD:
                expected = ActionKind.RESUME if checks != ChecksState.NONE else d.action.kind
            self.assertEqual(d.action.kind, expected, (lock, checks))
            h = d.history

    def test_a_run_that_ends_with_nothing_changed_twice_still_hands_over(self) -> None:
        state = WorkerState(True, lock=LockState.NONE, checks=ChecksState.NONE)
        first = policy.decide_from_state(CFG, state, AttemptHistory())
        self.assertEqual(first.action.kind, ActionKind.RESUME)
        h = first.history
        kinds: list[ActionKind] = []
        for _ in range(2):
            d = policy.decide_from_state(CFG, state, h)
            kinds.append(d.action.kind)
            h = d.history
        self.assertEqual(kinds, [ActionKind.RESUME, ActionKind.HANDOVER])

    def test_progress_clears_earlier_nudges(self) -> None:
        h = AttemptHistory(nudges=2, seen=("in_progress", LockState.NONE, ChecksState.NONE))
        d = policy.decide_from_state(CFG, WorkerState(True, "in_progress", checks=ChecksState.PASSED), h)
        self.assertEqual((d.action.kind, d.history.nudges), (ActionKind.RESUME, 0))

    def test_handover_prompt_names_the_reason_and_ticket_release(self) -> None:
        prompt = policy.handover_prompt("gave up after 3 resumes")
        self.assertIn("gave up after 3 resumes", prompt)
        self.assertIn("ticket_release", prompt)
        self.assertIn("not a person's request", prompt)
        self.assertIn("Commit and push", prompt)
        self.assertIn("write the handoff", prompt)
        self.assertIn("ticket_release", policy.HANDOVER_PROMPT)

    def test_pending_after_a_finished_run_waits(self) -> None:
        d = after(OutcomeKind.FINISHED, WorkerState(True, checks=ChecksState.PENDING))
        self.assertEqual(d.action.kind, ActionKind.WAIT_CHECKS)

    def test_finished_checks_resume_names_the_state(self) -> None:
        for checks in (ChecksState.PASSED, ChecksState.FAILED):
            d = policy.decide_from_state(CFG, WorkerState(True, lock=LockState.HELD, checks=checks), AttemptHistory())
            self.assertEqual(d.action.prompt, f"checks finished: {checks.value}")

    def test_nothing_pending_nudges_twice_then_hands_over(self) -> None:
        h = AttemptHistory()
        for expected in (1, 2):
            d = policy.decide_from_state(CFG, LEASED, h)
            self.assertEqual(d.action.kind, ActionKind.RESUME)
            self.assertEqual(d.action.prompt, policy.NUDGE_PROMPT)
            self.assertEqual(d.history.nudges, expected)
            h = d.history
        self.assertEqual(policy.decide_from_state(CFG, LEASED, h).action.kind, ActionKind.HANDOVER)

    def test_held_lock_with_no_checks_is_not_left_alone(self) -> None:
        d = policy.decide_from_state(CFG, WorkerState(True, lock=LockState.HELD), AttemptHistory())
        self.assertEqual(d.action.kind, ActionKind.RESUME)


class OutcomeTableTest(unittest.TestCase):
    def test_killed_and_lost_lease_free_the_slot(self) -> None:
        self.assertEqual(after(OutcomeKind.KILLED).action.kind, ActionKind.FREE)
        for kind in OutcomeKind:
            self.assertEqual(after(kind, WorkerState(False)).action.kind, ActionKind.FREE, kind)

    def test_transient_and_crashed_back_off_30s_2m_10m_then_hand_over(self) -> None:
        for kind in (OutcomeKind.TRANSIENT, OutcomeKind.CRASHED):
            h = AttemptHistory()
            for delay in (30, 120, 600):
                d = after(kind, history=h)
                self.assertEqual(d.action.kind, ActionKind.BACKOFF)
                self.assertEqual(d.action.until, NOW + delay)
                h = d.history
            self.assertEqual(h.resumes, 3)
            self.assertEqual(after(kind, history=h).action.kind, ActionKind.HANDOVER)

    def test_over_budget_resumes_at_once_and_counts_toward_the_cap(self) -> None:
        d = after(OutcomeKind.OVER_BUDGET, history=AttemptHistory(resumes=2))
        self.assertEqual(d.action.kind, ActionKind.RESUME)
        self.assertEqual(d.history.resumes, 3)
        self.assertEqual(after(OutcomeKind.OVER_BUDGET, history=d.history).action.kind, ActionKind.HANDOVER)

    def test_usage_limit_waits_for_the_reset(self) -> None:
        d = after(OutcomeKind.USAGE_LIMIT, reset_at=NOW + 3600)
        self.assertEqual((d.action.kind, d.action.until), (ActionKind.WAIT_RESET, NOW + 3600))
        self.assertEqual(d.history.resumes, 0)

    def test_usage_limit_wait_is_capped_at_five_hours(self) -> None:
        d = after(OutcomeKind.USAGE_LIMIT, reset_at=NOW + 9 * 3600)
        self.assertEqual(d.action.until, NOW + 5 * 3600)

    def test_usage_limit_without_a_reset_probes_later(self) -> None:
        d = after(OutcomeKind.USAGE_LIMIT)
        self.assertEqual(d.action.until, NOW + CFG.usage_limit_probe)

    def test_usage_limit_past_the_cap_hands_over(self) -> None:
        h = AttemptHistory(limit_since=NOW - 5 * 3600)
        self.assertEqual(after(OutcomeKind.USAGE_LIMIT, history=h).action.kind, ActionKind.HANDOVER)

    def test_a_finished_run_clears_the_usage_limit_clock(self) -> None:
        d = after(OutcomeKind.FINISHED, history=AttemptHistory(limit_since=NOW - 100))
        self.assertIsNone(d.history.limit_since)

    def test_auth_failed_hands_over_without_the_model(self) -> None:
        d = after(OutcomeKind.AUTH_FAILED)
        self.assertEqual(d.action.kind, ActionKind.HANDOVER)
        self.assertTrue(d.action.model_unavailable)

    def test_auth_failed_blames_the_hosts_harness_login_and_carries_the_detail(self) -> None:
        outcome = RunOutcome(OutcomeKind.AUTH_FAILED, "API Error: 401")
        d = policy.decide_after_run(CFG, NOW, outcome, LEASED, AttemptHistory())
        self.assertIn("supervisor's host", d.action.reason)
        self.assertIn("not your slop-haus sign-in", d.action.reason)
        self.assertEqual("API Error: 401", d.action.detail)

    def test_permission_refused_hands_over_with_the_model(self) -> None:
        d = policy.decide_after_run(CFG, NOW, RunOutcome(OutcomeKind.PERMISSION_REFUSED, "Bash(rm)"), LEASED, AttemptHistory())
        self.assertEqual(d.action.kind, ActionKind.HANDOVER)
        self.assertFalse(d.action.model_unavailable)
        self.assertIn("Bash(rm)", d.action.reason)

    def test_finished_decides_from_state(self) -> None:
        d = after(OutcomeKind.FINISHED, WorkerState(True, lock=LockState.QUEUED))
        self.assertEqual(d.action.kind, ActionKind.WAIT_LOCK)


class LockAlertTest(unittest.TestCase):
    def test_alert_threshold(self) -> None:
        cfg = PolicyConfig(lock_alert_after=100)
        self.assertFalse(policy.lock_alert_due(cfg, 199, 100, False))
        self.assertTrue(policy.lock_alert_due(cfg, 200, 100, False))
        self.assertFalse(policy.lock_alert_due(cfg, 200, 100, True))
        self.assertFalse(policy.lock_alert_due(cfg, 200, None, False))

    def test_default_is_45_minutes(self) -> None:
        self.assertEqual(PolicyConfig().lock_alert_after, 45 * 60)


if __name__ == "__main__":
    unittest.main()
