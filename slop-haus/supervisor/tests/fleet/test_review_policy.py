from __future__ import annotations

import unittest
from dataclasses import replace

from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.fleet.policy import ActionKind, AttemptHistory, PolicyConfig
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.review.result import ReviewOutcome, ReviewResult, RunResult
from slop_haus_supervisor.slophaus.client import ChecksState, LockState, ReviewRequest, WorkerState

CFG = PolicyConfig()
HEAD = "a" * 40
REQUEST = ReviewRequest(HEAD, "Adds the thing.", "2026-11-18T10:00:00Z")
ASKED = WorkerState(True, review_request=REQUEST)


class ReviewDecisionTest(unittest.TestCase):
    def test_an_open_request_is_reviewed_and_remembered(self) -> None:
        d = policy.decide_from_state(CFG, ASKED, AttemptHistory())
        self.assertEqual(ActionKind.REVIEW, d.action.kind)
        self.assertEqual(REQUEST, d.action.review)
        self.assertIn((HEAD, REQUEST.requested_at), d.history.reviewed)

    def test_a_request_already_taken_up_is_not_reviewed_again(self) -> None:
        first = policy.decide_from_state(CFG, ASKED, AttemptHistory())
        again = policy.decide_from_state(CFG, ASKED, first.history)
        self.assertNotEqual(ActionKind.REVIEW, again.action.kind)

    def test_a_handled_request_left_open_falls_through_to_the_nudge_cap(self) -> None:
        # The server keeps a request open when not every run was posted; the agent stopping must not loop.
        h = policy.decide_from_state(CFG, ASKED, AttemptHistory()).history
        kinds: list[ActionKind] = []
        for _ in range(CFG.max_nudges + 1):
            d = policy.decide_from_state(CFG, ASKED, h)
            kinds.append(d.action.kind)
            h = d.history
        self.assertEqual([ActionKind.RESUME] * CFG.max_nudges + [ActionKind.HANDOVER], kinds)

    def test_asking_again_for_the_same_head_is_a_new_request(self) -> None:
        # The not-pushed case: the agent pushes the very commit it asked about, then asks again.
        h = policy.decide_from_state(CFG, ASKED, AttemptHistory()).history
        asked_again = replace(ASKED, review_request=replace(REQUEST, requested_at="2026-11-18T10:05:00Z"))
        self.assertEqual(ActionKind.REVIEW, policy.decide_from_state(CFG, asked_again, h).action.kind)

    def test_a_review_comes_before_waiting_on_checks_or_the_lock_queue(self) -> None:
        for lock, checks in [(LockState.QUEUED, ChecksState.NONE), (LockState.NONE, ChecksState.PENDING), (LockState.NONE, ChecksState.PASSED)]:
            d = policy.decide_from_state(CFG, replace(ASKED, lock=lock, checks=checks), AttemptHistory())
            self.assertEqual(ActionKind.REVIEW, d.action.kind, (lock, checks))

    def test_a_lock_offer_is_taken_before_the_review(self) -> None:
        d = policy.decide_from_state(CFG, replace(ASKED, lock=LockState.OFFERED), AttemptHistory())
        self.assertEqual(ActionKind.TAKE_LOCK, d.action.kind)
        self.assertEqual(frozenset(), d.history.reviewed, "the request is still to be reviewed")

    def test_a_request_made_holding_the_lock_is_sent_back_once(self) -> None:
        held = replace(ASKED, lock=LockState.HELD, checks=ChecksState.PASSED)
        d = policy.decide_from_state(CFG, held, AttemptHistory())
        self.assertEqual((ActionKind.RESUME, policy.REVIEW_LOCK_HELD_PROMPT), (d.action.kind, d.action.prompt))
        self.assertIn("integration_unlock", d.action.prompt)
        again = policy.decide_from_state(CFG, held, d.history)
        self.assertNotEqual(policy.REVIEW_LOCK_HELD_PROMPT, again.action.prompt, "the same request is not refused again")

    def test_no_lease_frees_whatever_was_asked(self) -> None:
        d = policy.decide_from_state(CFG, replace(ASKED, lease_held=False), AttemptHistory())
        self.assertEqual(ActionKind.FREE, d.action.kind)

    def test_a_finished_run_that_asked_is_reviewed(self) -> None:
        d = policy.decide_after_run(CFG, 0.0, RunOutcome(OutcomeKind.FINISHED), ASKED, AttemptHistory())
        self.assertEqual(ActionKind.REVIEW, d.action.kind)

    def test_an_adopted_lease_with_an_open_request_reviews_it_before_queueing(self) -> None:
        state = replace(ASKED, checks=ChecksState.PASSED)
        self.assertEqual(ActionKind.REVIEW, policy.decide_on_adopt(CFG, state, AttemptHistory()).action.kind)
        held = replace(ASKED, lock=LockState.HELD)
        self.assertEqual(policy.LOCK_HELD_PROMPT, policy.decide_on_adopt(CFG, held, AttemptHistory()).action.prompt)

    def test_an_adopted_lock_offer_is_taken_before_the_review(self) -> None:
        offered = replace(ASKED, lock=LockState.OFFERED)
        self.assertEqual(ActionKind.TAKE_LOCK, policy.decide_on_adopt(CFG, offered, AttemptHistory()).action.kind)

    def test_an_adopted_request_already_taken_up_is_not_reviewed_again(self) -> None:
        h = policy.decide_on_adopt(CFG, replace(ASKED, checks=ChecksState.PASSED), AttemptHistory()).history
        again = policy.decide_on_adopt(CFG, replace(ASKED, checks=ChecksState.PASSED), h)
        self.assertEqual(ActionKind.QUEUE_LOCK, again.action.kind)

    def test_the_review_budget_is_capped_by_the_run_budget(self) -> None:
        self.assertEqual(600.0, PolicyConfig(review_timeout=900.0, run_budget=600.0).review_budget)
        self.assertEqual(900.0, PolicyConfig(review_timeout=900.0, run_budget=3600.0).review_budget)


class ReviewPromptTest(unittest.TestCase):
    def test_a_review_names_the_runs_verdict_and_open_findings(self) -> None:
        result = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult("request_changes", 2, reported=True))
        prompt = policy.review_prompt(result, "ticket-7-a1")
        self.assertIn(HEAD[:12], prompt)
        self.assertIn("is done: request_changes, 2 open findings. ", prompt)
        for tool in ("review_findings", "review_label", "review_request"):
            self.assertIn(tool, prompt)
        self.assertNotIn(policy.REVIEW_FAILED_NOTE, prompt)
        for persona in ("standard", "security", "concurrency", "test-efficacy"):
            self.assertNotIn(persona, prompt)

    def test_one_open_finding_is_singular(self) -> None:
        result = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult("approve_with_comments", 1, reported=True))
        self.assertIn("approve_with_comments, 1 open finding. ", policy.review_prompt(result, "b"))

    def test_a_failed_run_says_why_and_how_to_retry(self) -> None:
        result = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult(error="exit 1: usage limit reached"))
        prompt = policy.review_prompt(result, "ticket-7-a1")
        self.assertIn(f"review of {HEAD[:12]} failed (exit 1: usage limit reached). ", prompt)
        self.assertIn(policy.REVIEW_FAILED_NOTE, prompt)
        self.assertNotIn(policy.REVIEW_FINDINGS_NOTE, prompt)

    def test_a_run_the_server_did_not_take_says_so_and_how_to_retry(self) -> None:
        prompt = policy.review_prompt(ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult("approve", 0, reported=False)), "b")
        self.assertIn("approve, 0 open findings, not posted to slop-haus. ", prompt)
        self.assertIn(policy.REVIEW_FAILED_NOTE, prompt)

    def test_a_reviewed_result_without_its_run_is_a_failure_not_a_crash(self) -> None:
        prompt = policy.review_prompt(ReviewResult(ReviewOutcome.REVIEWED, HEAD), "b")
        self.assertIn("failed (no result was recorded)", prompt)
        self.assertIn(policy.REVIEW_FAILED_NOTE, prompt)

    def test_an_approval_from_a_review_that_did_not_cover_everything_says_so(self) -> None:
        result = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult("approve", 0, incomplete=True, reported=True))
        self.assertIn("approve, 0 open findings, incomplete. ", policy.review_prompt(result, "b"))

    def test_a_killed_review_says_it_was_stopped(self) -> None:
        prompt = policy.review_prompt(ReviewResult(ReviewOutcome.KILLED, HEAD), "b")
        self.assertIn("did not finish: it was stopped", prompt)
        self.assertIn("Its run may not have been posted", prompt)

    def test_the_other_outcomes_say_what_to_do(self) -> None:
        not_pushed = policy.review_prompt(ReviewResult(ReviewOutcome.NOT_PUSHED, HEAD), "ticket-7-a1")
        self.assertIn("not on origin's ticket-7-a1", not_pushed)
        self.assertIn("Push the branch", not_pushed)
        missing = policy.review_prompt(ReviewResult(ReviewOutcome.NO_REVIEWER, HEAD, detail="SLOP_HAUS_REVIEW_SCRIPT is not set"), "b")
        self.assertIn("SLOP_HAUS_REVIEW_SCRIPT is not set", missing)
        self.assertIn("ticket_discuss", missing)
        self.assertIn("will not help", missing)
        failed = policy.review_prompt(ReviewResult(ReviewOutcome.FAILED, HEAD, detail="could not clone origin"), "b")
        self.assertIn("could not clone origin", failed)
        self.assertIn("review_request again", failed)
