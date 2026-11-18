from __future__ import annotations

import logging
import unittest
from dataclasses import replace
from pathlib import Path

from fakes import CLAIM, FakeReview, Rig
from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.review.result import ReviewOutcome, ReviewResult, RunResult
from slop_haus_supervisor.slophaus.client import Event, EventKind, ReviewRequest, WorkerState
from test_slot import FINISHED, NO_LEASE, started

HEAD = "c" * 40
REQUEST = ReviewRequest(HEAD, "Makes it two.", "2026-11-18T10:00:00Z")
ASKED = WorkerState(True, review_request=REQUEST)
REVIEWED = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult("request_changes", 2, reported=True))


def reviewing(rig: Rig, *script: ReviewResult | None, state: WorkerState = ASKED) -> FakeReview:
    """A first run that ends having asked for a review, which the slot then starts."""
    review = rig.script_review(*script)
    started(rig, FINISHED, state=state)
    rig.slot.step()
    return review


class SlotReviewTest(unittest.TestCase):
    def test_a_request_starts_the_review_of_the_head_on_the_attempt_branch(self) -> None:
        rig = Rig()
        reviewing(rig, None)

        self.assertEqual(Phase.WAIT_REVIEW, rig.slot.phase)
        assert rig.reviewer is not None
        job = rig.reviewer.jobs[0]
        self.assertEqual(("agent-1", Path("/slots/agent-1"), CLAIM.branch, REQUEST), (job.agent, job.repo_dir, job.branch, job.request))
        snapshot = rig.slot.snapshot()
        self.assertEqual(("wait_review", Reason.REVIEWING.value, "ticket #7"), (snapshot.phase, snapshot.reason, snapshot.reason_detail))

    def test_the_result_resumes_the_agent_with_the_summary(self) -> None:
        rig = Rig()
        reviewing(rig, None, REVIEWED)
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(Phase.WAIT_REVIEW, rig.slot.phase, "still reviewing")

        rig.slot.step()

        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        self.assertEqual(policy.review_prompt(REVIEWED, CLAIM.branch), rig.harness.calls[1].spec.prompt)

    def test_a_request_left_open_is_not_reviewed_again(self) -> None:
        rig = Rig()
        reviewing(rig, REVIEWED)
        rig.script_run(FINISHED)
        rig.slot.step()  # the review ends: the agent is resumed
        rig.script_run(None)
        rig.slot.step()  # that run ends with the same request still open

        assert rig.reviewer is not None
        self.assertEqual(1, len(rig.reviewer.jobs))
        self.assertEqual(policy.NUDGE_PROMPT, rig.harness.calls[2].spec.prompt)

    def test_the_lease_is_heartbeated_through_a_long_review(self) -> None:
        rig = Rig()
        reviewing(rig, *[None] * 60)
        before = rig.client.count("heartbeat")
        for _ in range(60):  # 30 minutes in 30 second slices
            rig.slot.step()
        self.assertGreaterEqual(rig.client.count("heartbeat") - before, 2)
        self.assertEqual(Phase.WAIT_REVIEW, rig.slot.phase)

    def test_a_revoked_lease_kills_the_review_and_frees_the_slot(self) -> None:
        rig = Rig()
        review = reviewing(rig, None, None)
        rig.client.events = [[Event(EventKind.LEASE_REVOKED)]]

        rig.slot.step()

        self.assertTrue(review.killed)
        self.assertEqual(Phase.IDLE, rig.slot.phase)
        self.assertEqual(["start"], rig.harness.verbs)

    def test_a_lease_found_gone_at_heartbeat_kills_the_review(self) -> None:
        rig = Rig()
        review = reviewing(rig, *[None] * 100)
        rig.client.states = [NO_LEASE]
        for _ in range(30):
            if rig.slot.phase != Phase.WAIT_REVIEW:
                break
            rig.slot.step()
        self.assertTrue(review.killed)
        self.assertEqual(Phase.IDLE, rig.slot.phase)

    def test_a_review_past_its_budget_is_killed_and_the_agent_told(self) -> None:
        rig = Rig(config=replace(Rig().config, review_timeout=120.0))
        review = reviewing(rig, *[None] * 100)
        rig.script_run(None)
        for _ in range(10):
            if rig.slot.phase != Phase.WAIT_REVIEW:
                break
            rig.slot.step()
        self.assertTrue(review.killed)
        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        prompt = rig.harness.calls[1].spec.prompt
        self.assertIn("did not finish", prompt)
        self.assertIn("time budget of 2 minutes", prompt)

    def test_a_result_that_arrives_as_the_budget_runs_out_stands(self) -> None:
        rig = Rig(config=replace(Rig().config, review_timeout=60.0))
        # Each wait is one 30s slice, so the second ends on the budget with the review's own result.
        review = reviewing(rig, None, REVIEWED)
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(Phase.WAIT_REVIEW, rig.slot.phase)
        rig.slot.step()
        self.assertFalse(review.killed)
        self.assertEqual(policy.review_prompt(REVIEWED, CLAIM.branch), rig.harness.calls[1].spec.prompt)

    def test_a_stopping_supervisor_kills_the_review_and_resumes_nothing(self) -> None:
        rig = Rig()
        review = reviewing(rig, None, REVIEWED)

        rig.slot.abandon_run()
        rig.slot.step()

        self.assertTrue(review.killed)
        self.assertEqual(["start"], rig.harness.verbs)

    def test_a_supervisor_without_a_reviewer_tells_the_agent_at_once(self) -> None:
        rig = Rig(reviewer=None)
        started(rig, FINISHED, state=ASKED)
        rig.script_run(None)
        rig.slot.step()

        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        prompt = rig.harness.calls[1].spec.prompt
        self.assertIn("cannot review on this host", prompt)
        self.assertIn("ticket_discuss", prompt)

    def test_a_review_that_will_not_start_resumes_the_agent_with_the_masked_reason(self) -> None:
        rig = Rig(masker=SecretMasker(["s3cret-value"]))
        assert rig.reviewer is not None
        rig.reviewer.start_error = OSError("env file has s3cret-value in it")
        rig.script_run(FINISHED)
        rig.script_run(None)
        rig.client.events = [[Event(EventKind.CLAIMABLE, ticket_id=7)]]
        rig.client.claims = [CLAIM]
        rig.client.states = [ASKED]
        with self.assertLogs(level=logging.INFO) as logged:
            rig.slot.step()
            rig.slot.step()

        prompt = rig.harness.calls[1].spec.prompt
        self.assertIn("did not finish: OSError: env file has *** in it", prompt)
        self.assertNotIn("s3cret-value", "\n".join(logged.output))

    def test_a_run_error_is_masked_in_the_prompt_and_the_log(self) -> None:
        rig = Rig(masker=SecretMasker(["s3cret-value"]))
        failed = ReviewResult(ReviewOutcome.REVIEWED, HEAD, RunResult(error="exit 1: key s3cret-value refused"))
        with self.assertLogs(level=logging.INFO) as logged:
            reviewing(rig, failed)
            rig.script_run(None)
            rig.slot.step()
        prompt = rig.harness.calls[1].spec.prompt
        self.assertIn("failed (exit 1: key *** refused)", prompt)
        output = "\n".join(logged.output)
        self.assertNotIn("s3cret-value", output)
        self.assertIn("review started on ticket #7: head cccccccccccc", output)
        self.assertIn("review ended on ticket #7: reviewed", output)
        self.assertIn("run failed", output)

    def test_a_ticket_without_paths_has_no_branch_to_review_under(self) -> None:
        rig = Rig()
        rig.script_review(None)
        started(rig, FINISHED, state=ASKED, claim=replace(CLAIM, has_paths=False))
        rig.slot.step()
        assert rig.reviewer is not None
        self.assertEqual("", rig.reviewer.jobs[0].branch)


if __name__ == "__main__":
    unittest.main()
