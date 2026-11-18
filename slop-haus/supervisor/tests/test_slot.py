from __future__ import annotations

import logging
import tempfile
from datetime import UTC, datetime
import unittest
from dataclasses import replace
from pathlib import Path
from contextlib import contextmanager
from collections.abc import Callable, Generator

from fakes import CLAIM, CLAIMABLE, Rig
from slop_haus_supervisor.fleet import login_failure, policy
from slop_haus_supervisor.fleet.pause import PAUSE_RECHECK_INTERVAL
from slop_haus_supervisor.fleet.slot import Phase, Slot, SlotDeps
from slop_haus_supervisor.fleet.slot_log import IDLE_LOG_INTERVAL, REFUSAL_LOG_INTERVAL
from slop_haus_supervisor.harness.base import ModelUsage, OutcomeKind, RunOutcome, Usage
from slop_haus_supervisor.workspace.briefing import BriefingUnavailable
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.workspace.handover import Prepared, SafetyPush
from slop_haus_supervisor.slophaus.client import (
    ChecksState,
    Claim,
    ClaimRefused,
    Event,
    EventKind,
    LockState,
    WorkerState,
)

FINISHED = RunOutcome(OutcomeKind.FINISHED, usage=Usage((ModelUsage("anthropic", "m", "subscription", {"output": 42}),)))
NO_LEASE = WorkerState(False, "done")
LEASED = WorkerState(True)


def started(rig: Rig, *script: RunOutcome | None, state: WorkerState = LEASED, claim: Claim = CLAIM) -> None:
    """Claim the ticket and begin a first run scripted with `script`."""
    rig.client.events = [CLAIMABLE]
    rig.client.claims = [claim]
    rig.client.states = [state]
    rig.script_run(*script)
    rig.slot.step()


def started_with(rig: Rig, state: WorkerState = LEASED, claim: Claim = CLAIM) -> None:
    """Like `started` for a run already scripted with `script_run`."""
    rig.client.events = [CLAIMABLE]
    rig.client.claims = [claim]
    rig.client.states = [state]
    rig.slot.step()


class ClaimTest(unittest.TestCase):
    def test_the_run_starts_in_the_tickets_repo_of_a_nested_workspace(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "app" / ".git").mkdir(parents=True)
            rig = Rig()
            rig.slot.directory = Path(tmp)
            started(rig, None, claim=replace(CLAIM, paths=("app/src",)))
            self.assertEqual(rig.harness.calls[0].spec.directory, Path(tmp) / "app")
            self.assertEqual(rig.harness.calls[0].spec.workspace, Path(tmp), "the harness finds the workspace's settings and sign-in from it")
            self.assertEqual(rig.repo.prepared_in, [Path(tmp) / "app"], "git runs in the repo, not the plain root")

    def test_the_claim_reports_the_workspace_bundle_hash(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / ".slop-haus").mkdir()
            (Path(tmp) / ".slop-haus" / "bundle-hash").write_text("abc123\n", encoding="ascii")
            rig = Rig()
            rig.slot.directory = Path(tmp)
            started(rig, None)
            self.assertEqual(rig.client.claimed_bundle_hashes, ["abc123"])

    def test_claims_on_offer_records_the_session_and_starts(self) -> None:
        rig = Rig()
        started(rig, None)
        self.assertEqual(rig.slot.phase, Phase.RUNNING)
        self.assertIn(("record_session", "fake", "s1"), rig.client.calls)
        spec = rig.harness.calls[0].spec
        self.assertEqual((spec.ticket_id, spec.branch, spec.variant), (7, "ticket-7-a1", "opus-high"))
        self.assertEqual(rig.client.claimed_ids, [7])
        self.assertEqual(rig.harness.verbs, ["start"])

    def test_a_run_uses_the_variant_of_the_agents_own_harness_config(self) -> None:
        rig = Rig(variant_of=lambda agent: f"model=for-{agent}")
        started(rig, None)
        self.assertEqual("model=for-agent-1", rig.harness.calls[0].spec.variant)

    def test_stays_idle_without_work(self) -> None:
        rig = Rig()
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.client.count("claim"), 0)

    def test_refused_claim_stays_idle(self) -> None:
        rig = Rig()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [ClaimRefused()]
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.harness.calls, [])

    def test_affinity_claim_resumes_the_recorded_session(self) -> None:
        rig = Rig()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [Claim(7, "ticket-7-a1", "dead", session_id="old")]
        rig.client.states = [LEASED]
        rig.script_run(None, session_id="old")
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["resume"])
        self.assertEqual(rig.harness.calls[0].session_id, "old")
        self.assertEqual(rig.client.count("record_session"), 0)

    def test_missing_session_starts_a_fresh_one(self) -> None:
        rig = Rig()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [Claim(7, "ticket-7-a1", "dead", session_id="gone")]
        rig.client.states = [LEASED]
        rig.harness.resume_missing = True
        rig.script_run(None, session_id="fresh")
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["resume-missing", "start"])
        self.assertIn(("record_session", "fake", "fresh"), rig.client.calls)


class CheckoutGuardTest(unittest.TestCase):
    def test_checkout_is_prepared_on_the_claimed_branch_before_the_run_starts(self) -> None:
        rig = Rig()
        started(rig, None)
        self.assertEqual(rig.repo.prepared, ["ticket-7-a1"])
        self.assertEqual(rig.repo.events, ["prepare", "snapshot", "launch"])

    def test_ticket_without_paths_starts_on_the_current_checkout_without_a_switch(self) -> None:
        rig = Rig()
        started(rig, None, claim=replace(CLAIM, has_paths=False))
        self.assertEqual(rig.repo.prepared, [])
        self.assertEqual(rig.repo.events, ["check_clean", "snapshot", "launch"])
        self.assertEqual(rig.client.count("escalate"), 0)
        self.assertEqual(rig.harness.calls[0].spec.branch, CLAIM.branch)
        self.assertIn("no branch", rig.harness.calls[0].spec.prompt)

    def test_ticket_without_paths_never_touches_branch_state_through_a_run_and_handover(self) -> None:
        rig = Rig()
        rig.repo.stray = ["something"]
        rig.repo.unpushed = True
        started(rig, RunOutcome(OutcomeKind.PERMISSION_REFUSED, "x"), claim=replace(CLAIM, has_paths=False))
        rig.script_run(FINISHED)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.repo.pushes, [])
        self.assertEqual(rig.alerts.messages, [])
        self.assertEqual(rig.client.count("escalate"), 1)
        self.assertIsNone(rig.slot._last_branch)  # pyright: ignore[reportPrivateUsage]

    def test_ticket_without_paths_on_a_dirty_checkout_is_still_refused(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "the slot checkout has uncommitted changes")
        started(rig, None, claim=replace(CLAIM, has_paths=False))
        self.assertEqual(rig.harness.calls, [])
        self.assertEqual(rig.slot.phase, Phase.PAUSED)

    def test_claim_base_commit_is_passed_so_a_missing_branch_can_be_cut(self) -> None:
        rig = Rig()
        started(rig, None)
        self.assertEqual(rig.repo.prepared_base, [CLAIM.base_commit])

    def test_unprepared_slot_refuses_the_run_hands_the_ticket_back_and_pauses(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "the slot checkout has uncommitted changes")
        started(rig, None)
        self.assertEqual(rig.harness.calls, [])
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        self.assertEqual(rig.client.count("escalate"), 1)
        self.assertIn("uncommitted changes", rig.client.notes()[0])
        self.assertIn("uncommitted changes", rig.alerts.messages[0])

    def test_work_left_outside_the_branch_alerts_and_keeps_the_ticket(self) -> None:
        rig = Rig()
        rig.repo.stray = ["ticket-6-a1 gained 1 commit(s), tip abc"]
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        self.assertTrue(any("ticket-6-a1 gained" in m for m in rig.alerts.messages))
        self.assertEqual(rig.client.count("escalate"), 0)
        self.assertEqual(rig.repo.pushes, [])

    def test_a_clean_run_raises_no_alert(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        self.assertEqual(rig.alerts.messages, [])


class AfterRunTest(unittest.TestCase):
    def test_finished_without_lease_frees_the_slot_and_reports_usage(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertIn(("report_usage", "42"), rig.client.calls)

    def test_stops_heartbeating_once_the_ticket_is_let_go(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.clock.advance(3600)
        rig.slot.step()
        self.assertEqual(rig.client.count("heartbeat"), 0)

    def test_nudges_twice_then_escalates(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        for session in ("s2", "s3"):
            rig.script_run(FINISHED, session_id=session)
            rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start", "resume", "resume"])
        self.assertEqual(rig.harness.calls[1].spec.prompt, policy.NUDGE_PROMPT)
        rig.script_run(FINISHED)  # the handover's push-and-handoff resume
        rig.slot.step()
        self.assertEqual(rig.harness.calls[3].spec.prompt, policy.HANDOVER_PROMPT)
        rig.slot.step()
        self.assertEqual(len(rig.client.notes()), 1)
        self.assertEqual(rig.slot.phase, Phase.IDLE)


class AfterRunFailureTest(unittest.TestCase):
    """A call after the run raises (the scheduler logs and steps again); the slot must not wedge."""

    def test_state_failure_after_the_run_is_retried_without_reporting_usage_twice(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.client.fail_once["state"] = RuntimeError("403")
        with self.assertRaises(RuntimeError):
            rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.client.count("report_usage"), 1)

    def test_usage_failure_after_the_run_is_retried(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.client.fail_once["report_usage"] = RuntimeError("boom")
        with self.assertRaises(RuntimeError):
            rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.client.count("report_usage"), 2)

    def test_retry_counts_the_attempt_once(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        rig.client.fail_once["state"] = RuntimeError("403")
        rig.script_run(None, session_id="s2")
        with self.assertRaises(RuntimeError):
            rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start", "resume"])
        self.assertEqual(rig.harness.calls[1].spec.prompt, policy.NUDGE_PROMPT)


    def test_failure_applying_the_decision_is_retried_with_the_same_decision(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        rig.slot.step()  # nothing scripted: the nudge's resume fails
        self.assertEqual(rig.slot.phase, Phase.BACKOFF)
        rig.script_run(None, session_id="s2")
        rig.clock.advance(rig.config.backoffs[0])
        rig.slot.step()
        self.assertEqual(rig.harness.calls[-1].spec.prompt, policy.NUDGE_PROMPT)
        self.assertEqual(rig.slot.history.nudges, 1)
        self.assertEqual(rig.client.count("report_usage"), 1)

    def test_lock_is_taken_once_when_the_resume_after_it_fails(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.OFFERED))
        rig.slot.step()  # nothing scripted: the resume fails
        self.assertEqual(rig.slot.phase, Phase.BACKOFF)
        rig.script_run(None, session_id="s2")
        rig.clock.advance(rig.config.backoffs[0])
        rig.slot.step()
        self.assertEqual(rig.client.count("take_lock"), 1)
        self.assertEqual(rig.harness.calls[-1].spec.prompt, policy.LOCK_HELD_PROMPT)
        self.assertEqual(rig.slot.phase, Phase.RUNNING)

    def test_handover_failure_is_retried(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        for session in ("s2", "s3"):
            rig.script_run(FINISHED, session_id=session)
            rig.slot.step()
        rig.script_run(FINISHED)
        rig.slot.step()
        rig.client.fail_once["escalate"] = RuntimeError("boom")
        with self.assertRaises(RuntimeError):
            rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)


class WaitingTest(unittest.TestCase):
    def test_queued_waits_then_takes_the_offer_and_resumes(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_LOCK)
        rig.client.states = [WorkerState(True, lock=LockState.OFFERED)]
        rig.script_run(None)
        rig.clock.advance(rig.config.state_poll_interval)
        rig.slot.step()
        self.assertEqual(rig.client.count("take_lock"), 1)
        self.assertEqual(rig.harness.calls[1].spec.prompt, policy.LOCK_HELD_PROMPT)
        self.assertEqual(rig.slot.phase, Phase.RUNNING)

    def test_waits_for_checks_then_resumes_with_the_result(self) -> None:
        rig = Rig()
        held = WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING)
        started(rig, FINISHED, state=held)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_CHECKS)
        rig.client.states = [WorkerState(True, lock=LockState.HELD, checks=ChecksState.FAILED)]
        rig.script_run(None)
        rig.clock.advance(rig.config.state_poll_interval)
        rig.slot.step()
        self.assertEqual(rig.harness.calls[1].spec.prompt, "checks finished: failed")

    def test_does_not_look_again_before_the_poll_interval(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        looks = rig.client.count("state")
        rig.clock.advance(rig.config.state_poll_interval - 1)
        rig.slot.step()
        self.assertEqual(rig.client.count("state"), looks)

    def test_heartbeats_through_the_lock_queue(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        for _ in range(25):
            rig.clock.advance(60)
            rig.slot.step()
        self.assertGreaterEqual(rig.client.count("heartbeat"), 2)
        self.assertEqual(rig.slot.phase, Phase.WAIT_LOCK)

    def test_a_person_message_while_waiting_resumes_with_checkin(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.events = [[Event(EventKind.MESSAGE)]]
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(rig.harness.calls[1].spec.prompt, policy.MESSAGE_PROMPT)

    def test_a_message_seen_while_a_run_is_alive_is_not_lost(self) -> None:
        rig = Rig()
        started(rig, None, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.client.events = [[Event(EventKind.MESSAGE)]]
        rig.slot.step()  # run alive: the poll sees the message and keeps it
        rig.script_run(None)
        rig.slot.step()  # run ends, slot waits for the lock
        rig.slot.step()  # waiting: the kept message resumes the session
        self.assertEqual(rig.harness.calls[1].spec.prompt, policy.MESSAGE_PROMPT)

    def test_revocation_beats_a_message_in_the_same_poll(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.events = [[Event(EventKind.MESSAGE), Event(EventKind.LEASE_REVOKED)]]
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.harness.verbs, ["start"])

    def test_revoked_during_backoff_does_not_resume(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.CRASHED))
        rig.slot.step()
        rig.client.events = [[Event(EventKind.LEASE_REVOKED)]]
        rig.clock.advance(60)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.harness.verbs, ["start"])

    def test_lease_lost_while_waiting_frees_the_slot(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.states = [NO_LEASE]
        rig.clock.advance(rig.config.state_poll_interval)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)


class FinishedChecksWhileHoldingLockTest(unittest.TestCase):
    def test_held_lock_with_finished_checks_is_resumed_at_once(self) -> None:
        # The stranded-lock shape: lock held, nobody running, CI already done.
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.states = [WorkerState(True, lock=LockState.HELD, checks=ChecksState.FAILED)]
        rig.script_run(None)
        rig.clock.advance(rig.config.state_poll_interval)
        rig.slot.step()
        self.assertEqual(rig.harness.calls[1].spec.prompt, "checks finished: failed")


class RunningTest(unittest.TestCase):
    def test_heartbeats_every_ten_minutes_while_the_run_is_alive(self) -> None:
        rig = Rig()
        started(rig, *[None] * 100)
        for _ in range(60):  # 30 minutes in 30 second slices
            rig.slot.step()
        self.assertEqual(rig.client.count("heartbeat"), 3)

    def test_lease_revoked_kills_the_run(self) -> None:
        rig = Rig()
        handle = rig.script_run(None, None)
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.client.states = [LEASED]
        rig.slot.step()
        rig.client.events = [[Event(EventKind.LEASE_REVOKED)]]
        rig.slot.step()
        self.assertTrue(handle.killed)
        self.assertEqual(rig.slot.phase, Phase.IDLE)

    def test_run_over_budget_is_killed_then_resumed(self) -> None:
        rig = Rig()
        first = rig.script_run(*[None] * 1000)
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.client.states = [LEASED]
        rig.slot.step()
        rig.script_run(None, session_id="s2")
        for _ in range(int(3 * 3600 / 30) + 1):
            if first.killed:
                break
            rig.slot.step()
        self.assertTrue(first.killed)
        self.assertEqual(rig.slot.history.resumes, 1)
        self.assertEqual(rig.harness.verbs, ["start", "resume"])

    def test_an_abandoned_run_is_killed_and_the_slot_acts_on_nothing_after_it(self) -> None:
        rig = Rig()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.client.states = [NO_LEASE]
        handle = rig.script_run(FINISHED)
        rig.slot.step()
        rig.slot.abandon_run()

        rig.slot.step()

        self.assertTrue(handle.killed)
        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        self.assertNotIn(("report_usage", "42"), rig.client.calls)

    def test_an_abandoned_run_is_saved_before_the_kill_and_again_after_it(self) -> None:
        rig = Rig()
        handle = rig.script_run(FINISHED)
        handle.on_kill = lambda: rig.repo.events.append("kill")
        started_with(rig)

        rig.slot.abandon_run()

        events = rig.repo.events
        self.assertEqual(["ticket-7-a1", "ticket-7-a1"], rig.repo.interrupted_pushes)
        self.assertLess(events.index("interrupted_push"), events.index("kill"))
        self.assertGreater(len(events) - 1 - events[::-1].index("interrupted_push"), events.index("kill"))

    def test_a_failed_interrupted_push_warns_names_the_branch_and_still_kills(self) -> None:
        rig = Rig()
        handle = rig.script_run(FINISHED)
        started_with(rig)
        rig.repo.push_result = SafetyPush(False, error="rejected")

        with self.assertLogs("slop_haus_supervisor.fleet.slot", "WARNING") as logs:
            rig.slot.abandon_run()

        self.assertTrue(any("ticket-7-a1" in line and "rejected" in line for line in logs.output))
        self.assertTrue(handle.killed)

    def test_withheld_files_are_warned_about_by_name(self) -> None:
        rig = Rig()
        rig.script_run(FINISHED)
        started_with(rig)
        rig.repo.push_result = SafetyPush(True, commit="abc123456", untracked=["secrets.env"])

        with self.assertLogs("slop_haus_supervisor.fleet.slot", "WARNING") as logs:
            rig.slot.abandon_run()

        self.assertTrue(any("secrets.env" in line for line in logs.output))

    def test_resume_prompt_names_withheld_files(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(True, interrupted=True, withheld=["secrets.env"])
        started(rig, FINISHED)

        self.assertIn("secrets.env", rig.harness.calls[0].spec.prompt)

    def test_a_ticket_without_paths_has_no_interrupted_push(self) -> None:
        rig = Rig()
        started(rig, FINISHED, claim=replace(CLAIM, has_paths=False))
        rig.repo.unpushed = True

        rig.slot.abandon_run()

        self.assertEqual([], rig.repo.interrupted_pushes)

    def test_a_restarted_slot_resumes_the_interrupted_ticket_without_escalating(self) -> None:
        first = Rig()
        first.script_run(FINISHED)
        started_with(first)
        first.slot.abandon_run()

        second = Rig()
        second.repo.prepare_result = Prepared(True, interrupted=True)
        started(second, FINISHED, claim=replace(CLAIM, session_id="s1"))

        self.assertEqual(["resume"], [c.verb for c in second.harness.calls])
        self.assertIn("interrupted", second.harness.calls[0].spec.prompt)
        self.assertEqual([], second.client.notes())
        self.assertEqual(Phase.RUNNING, second.slot.phase)

    def test_a_clean_claim_gets_no_interrupted_note(self) -> None:
        rig = Rig()
        started(rig, FINISHED)

        self.assertNotIn("interrupted", rig.harness.calls[0].spec.prompt)

    def test_lease_found_gone_at_heartbeat_kills_the_run(self) -> None:
        rig = Rig()
        handle = rig.script_run(*[None] * 100)
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.client.states = [NO_LEASE]
        rig.slot.step()
        for _ in range(25):
            rig.slot.step()
        self.assertTrue(handle.killed)
        self.assertEqual(rig.slot.phase, Phase.IDLE)


class FailureOutcomesTest(unittest.TestCase):
    def test_crash_backs_off_then_resumes(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.CRASHED))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.BACKOFF)
        rig.script_run(None)
        rig.clock.advance(29)
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start"])
        rig.clock.advance(1)
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start", "resume"])

    def test_fourth_crash_hands_over_and_escalates(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.CRASHED))
        for delay in (30, 120, 600):
            rig.slot.step()
            rig.script_run(RunOutcome(OutcomeKind.CRASHED))
            rig.clock.advance(delay)
            rig.slot.step()
        rig.script_run(FINISHED)  # the handover's push-and-handoff resume
        rig.slot.step()  # fourth crash: hand over
        rig.slot.step()
        self.assertEqual(len(rig.client.notes()), 1)
        self.assertIn("gave up", rig.client.notes()[0])

    def test_usage_limit_keeps_the_lease_and_resumes_after_the_reset(self) -> None:
        rig = Rig()
        reset = rig.clock.now() + 3600
        started(rig, RunOutcome(OutcomeKind.USAGE_LIMIT, reset_at=reset))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_RESET)
        rig.script_run(None)
        for _ in range(70):
            rig.clock.advance(60)
            rig.slot.step()
            if rig.harness.verbs == ["start", "resume"]:
                break
        self.assertEqual(rig.harness.verbs, ["start", "resume"])
        self.assertGreaterEqual(rig.clock.now(), reset)
        self.assertGreaterEqual(rig.client.count("heartbeat"), 1)
        self.assertEqual(rig.client.notes(), [])

    def test_auth_failure_pushes_escalates_and_pauses_without_resuming(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        started(rig, RunOutcome(OutcomeKind.AUTH_FAILED))
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start"])
        self.assertEqual(rig.repo.pushes, ["ticket-7-a1"])
        self.assertEqual(len(rig.client.notes()), 1)
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        self.assertEqual(len(rig.alerts.messages), 1)
        rig.client.events = [CLAIMABLE]
        rig.slot.step()
        self.assertEqual(rig.client.count("claim"), 1)  # paused: no new claim
        rig.slot.unpause()
        self.assertEqual(rig.slot.phase, Phase.IDLE)

    def test_auth_failure_comment_and_pause_name_the_harness_fix_and_mask_the_detail(self) -> None:
        for harness, fix in (("claude", "harness login claude"), ("pi", "harness login pi"), ("other", "slot unpause")):
            with self.subTest(harness):
                rig = Rig(masker=SecretMasker(["tok-12345"]))
                rig.harness.name = harness
                # The secret straddles the detail's cut: masking must come first.
                detail = "401 for tok-12345 " + "x" * (login_failure.DETAIL_CHARS - 22) + "tok-12345"
                started(rig, RunOutcome(OutcomeKind.AUTH_FAILED, detail))
                rig.slot.step()
                note = rig.client.notes()[0]
                pause = rig.slot.snapshot().pause_reason
                for text in (note, pause):
                    self.assertIn("supervisor's host", text)
                    self.assertIn("401 for ***", text)
                    self.assertNotIn("tok-", text)
                    self.assertIn(fix, text)
                self.assertIn("slot unpause", pause)
                self.assertLessEqual(len(pause), 512, "the board cuts a longer reason")

    def test_permission_refusal_names_the_tool_in_the_escalation(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.PERMISSION_REFUSED, "Bash(rm -rf)"))
        rig.script_run(FINISHED)
        rig.slot.step()
        rig.slot.step()
        self.assertIn("Bash(rm -rf)", rig.client.notes()[0])


class HandoverTest(unittest.TestCase):
    def _hand_over(self, rig: Rig) -> None:
        started(rig, RunOutcome(OutcomeKind.PERMISSION_REFUSED, "x"))
        rig.script_run(FINISHED)
        rig.slot.step()  # decides handover, resumes once to push

    def test_no_safety_push_when_the_model_pushed(self) -> None:
        rig = Rig()
        self._hand_over(rig)
        rig.slot.step()
        self.assertEqual(rig.repo.pushes, [])
        self.assertEqual(len(rig.client.notes()), 1)

    def test_safety_push_when_the_model_did_not(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        self._hand_over(rig)
        rig.slot.step()
        self.assertEqual(rig.repo.pushes, ["ticket-7-a1"])
        self.assertIn("abc123", rig.client.notes()[0])

    def test_failed_safety_push_is_reported_and_alerts(self) -> None:
        from slop_haus_supervisor.workspace.handover import SafetyPush

        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, untracked=["notes.txt"], error="rejected")
        self._hand_over(rig)
        rig.slot.step()
        note = rig.client.notes()[0]
        self.assertIn("could NOT be pushed", note)
        self.assertIn("notes.txt", note)
        self.assertEqual(len(rig.alerts.messages), 1)

    def test_a_slot_with_unpushed_work_that_cannot_push_is_never_reused(self) -> None:
        from slop_haus_supervisor.workspace.handover import SafetyPush

        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="rejected")
        self._hand_over(rig)
        rig.slot.step()
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        self.assertEqual(rig.client.count("claim"), 1)  # only the original claim

    def test_unpushed_work_is_pushed_before_the_next_ticket(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.repo.unpushed = True
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(rig.repo.pushes, ["ticket-7-a1"])
        self.assertEqual(rig.slot.phase, Phase.RUNNING)


class LifecycleLogTest(unittest.TestCase):
    """Every lifecycle event leaves one INFO line, so the screen log alone says what a slot is doing."""

    @contextmanager
    def logged(self) -> Generator[list[str]]:
        lines: list[str] = []
        with self.assertLogs("slop_haus_supervisor.fleet.slot", level=logging.INFO) as captured:
            yield lines
            # assertLogs fails on no output; a marker keeps the empty case assertable.
            logging.getLogger("slop_haus_supervisor.fleet.slot").info("marker")
        lines.extend(r.getMessage() for r in captured.records if r.getMessage() != "marker")

    def lines_for(self, rig: Rig, action: Callable[[], object]) -> list[str]:
        with self.logged() as lines:
            action()
        return lines

    def test_claim_and_run_start_are_logged(self) -> None:
        rig = Rig()
        lines = self.lines_for(rig, lambda: started(rig, None))
        self.assertIn("slot=agent-1 claimed ticket #7 on branch ticket-7-a1", lines)
        self.assertIn("slot=agent-1 run started on ticket #7: fresh session, initial prompt", lines)
        self.assertIn("slot=agent-1 phase=running from=idle ticket=7", lines)

    def test_resume_and_prompt_kind_are_logged(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        rig.script_run(None, session_id="s2")
        lines = self.lines_for(rig, rig.slot.step)
        self.assertIn("slot=agent-1 run ended on ticket #7: finished after 30s", lines)
        self.assertIn("slot=agent-1 decision after run: resume", lines)
        self.assertIn("slot=agent-1 run started on ticket #7: resumed session, nudge prompt", lines)

    def test_refused_claim_logs_once_per_window(self) -> None:
        rig = Rig()
        rig.client.claims = [ClaimRefused("already claimed")] * 3
        rig.client.events = [CLAIMABLE, CLAIMABLE, CLAIMABLE]
        lines = self.lines_for(rig, lambda: [rig.slot.step() for _ in range(2)])
        self.assertEqual(["slot=agent-1 claim of ticket #7 refused: already claimed"], lines)
        rig.clock.advance(REFUSAL_LOG_INTERVAL)
        self.assertEqual(1, len(self.lines_for(rig, rig.slot.step)))

    def test_idle_heartbeat_reports_the_last_poll(self) -> None:
        rig = Rig()
        self.assertEqual([], self.lines_for(rig, rig.slot.step))
        rig.clock.advance(IDLE_LOG_INTERVAL)
        self.assertEqual(["slot=agent-1 idle; last poll: no claimable work"], self.lines_for(rig, rig.slot.step))
        self.assertEqual([], self.lines_for(rig, rig.slot.step))

    def test_idle_heartbeat_names_a_refused_offer(self) -> None:
        rig = Rig()
        self.assertEqual(Phase.IDLE, rig.slot.phase)  # builds the slot before the clock moves
        rig.clock.advance(IDLE_LOG_INTERVAL)
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [ClaimRefused("scope held")]
        lines = self.lines_for(rig, rig.slot.step)
        self.assertEqual(["slot=agent-1 idle; last poll: offered #7 and refused: scope held"], [l for l in lines if "idle;" in l])

    def test_each_phase_change_logs_exactly_one_line(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        lines = self.lines_for(rig, rig.slot.step)
        self.assertEqual(["slot=agent-1 phase=wait_lock from=running ticket=7"], [l for l in lines if "phase" in l])
        rig.clock.advance(rig.config.state_poll_interval)
        # Looking again from the same phase is not a change.
        self.assertEqual([], [l for l in self.lines_for(rig, rig.slot.step) if "phase" in l])

    def test_lock_taken_and_free_are_logged(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.states = [WorkerState(True, lock=LockState.OFFERED)]
        rig.script_run(FINISHED)
        rig.clock.advance(rig.config.state_poll_interval)
        lines = self.lines_for(rig, rig.slot.step)
        self.assertIn("slot=agent-1 took the integration lock offered to ticket #7", lines)
        self.assertIn("slot=agent-1 run started on ticket #7: resumed session, lock held prompt", lines)
        rig.client.states = [NO_LEASE]
        lines = self.lines_for(rig, rig.slot.step)
        self.assertIn("slot=agent-1 freed ticket #7: finished on the server", lines)

    def test_message_and_checks_prompt_kinds_are_logged(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED))
        rig.slot.step()
        rig.client.events = [[Event(EventKind.MESSAGE)]]
        rig.script_run(FINISHED)
        self.assertIn("slot=agent-1 run started on ticket #7: resumed session, message prompt", self.lines_for(rig, rig.slot.step))
        rig.client.states = [WorkerState(True, lock=LockState.HELD, checks=ChecksState.FAILED)]
        rig.script_run(None)
        rig.clock.advance(rig.config.state_poll_interval)
        lines = self.lines_for(rig, rig.slot.step)
        self.assertIn("slot=agent-1 run started on ticket #7: resumed session, checks finished prompt", lines)

    def test_backoff_decision_names_the_wake_time(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.CRASHED))
        lines = self.lines_for(rig, rig.slot.step)
        wake = datetime.fromtimestamp(rig.clock.now() + rig.config.backoffs[0], UTC).strftime("%H:%M:%SZ")
        self.assertIn(f"slot=agent-1 decision after run: backoff until {wake}", lines)
        self.assertIn("slot=agent-1 phase=backoff from=running ticket=7", lines)

    def test_handback_logs_reason_and_safety_push(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        started(rig, FINISHED)
        for session in ("s2", "s3"):
            rig.script_run(FINISHED, session_id=session)
            rig.slot.step()
        rig.script_run(FINISHED)
        resume = self.lines_for(rig, rig.slot.step)
        self.assertIn("slot=agent-1 run started on ticket #7: resumed session, handover prompt", resume)
        lines = self.lines_for(rig, rig.slot.step)
        self.assertTrue(
            any(l.startswith("slot=agent-1 handed ticket #7 back: the model stopped without a closing call") and l.endswith("safety push ok") for l in lines),
            lines,
        )
        self.assertIn("slot=agent-1 freed ticket #7: handed back", lines)

    def test_failed_safety_push_pauses_with_a_warning(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="rejected")
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        with self.assertLogs("slop_haus_supervisor.fleet.slot", level=logging.INFO) as captured:
            rig.slot.step()
        warnings = [r.getMessage() for r in captured.records if r.levelno == logging.WARNING]
        self.assertEqual(["slot=agent-1 paused: unpushed work for ticket-7-a1 could not be pushed: rejected"], warnings)
        with self.assertLogs("slop_haus_supervisor.fleet.slot", level=logging.INFO) as captured:
            rig.slot.unpause()
        self.assertIn("slot=agent-1 unpaused (was: unpushed work for ticket-7-a1 could not be pushed: rejected)", [r.getMessage() for r in captured.records])

    def test_pause_from_clearing_previous_work_alerts_and_logs_a_warning(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="rejected")
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(["agent-1 paused: unpushed work for ticket-7-a1 could not be pushed: rejected"], rig.alerts.messages)

    def test_rescued_previous_work_alerts_but_does_not_pause(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(True, commit="abc", rescue_ref="refs/heads/ticket-7-a1-rescue-1")
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertNotEqual(Phase.PAUSED, rig.slot.phase)
        self.assertEqual(["agent-1: ticket-7-a1 had diverged from origin; its work is on refs/heads/ticket-7-a1-rescue-1"], rig.alerts.messages)

    def test_clean_previous_branch_is_checked_for_supersession_before_reuse(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(["ticket-7-a1"], rig.repo.superseded_checked)
        self.assertEqual(["ticket-7-a1"], rig.repo.released)

    def test_escalation_note_names_the_rescue_ref(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(True, commit="abc", rescue_ref="refs/heads/ticket-7-a1-rescue-1")
        started(rig, FINISHED)
        for session in ("s2", "s3"):
            rig.script_run(FINISHED, session_id=session)
            rig.slot.step()
        rig.script_run(FINISHED)
        rig.slot.step()
        rig.slot.step()
        self.assertIn("pushed to refs/heads/ticket-7-a1-rescue-1 as abc", rig.client.notes()[0])

    def test_cleared_previous_work_releases_its_worktrees(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(["ticket-7-a1"], rig.repo.released)

    def test_reasons_are_masked_and_stripped_of_url_credentials(self) -> None:
        rig = Rig()
        deps = SlotDeps(rig.client, rig.harness, rig.repo, rig.clock, rig.alerts, rig.config, rig.briefing, masker=SecretMasker(["tok-12345"]))
        slot = Slot("agent-1", Path("/slots/agent-1"), deps)
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [ClaimRefused("bad tok-12345\nfor https://user:pw@host/x.git")]
        with self.logged() as lines:
            slot.step()
        self.assertEqual(["slot=agent-1 claim of ticket #7 refused: bad *** for https://host/x.git"], lines)

    def test_pause_warning_is_masked(self) -> None:
        rig = Rig()
        deps = SlotDeps(rig.client, rig.harness, rig.repo, rig.clock, rig.alerts, rig.config, rig.briefing, masker=SecretMasker(["tok-12345"]))
        slot = Slot("agent-1", Path("/slots/agent-1"), deps)
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="push to https://x:tok-12345@host/r.git rejected")
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        rig.client.states = [NO_LEASE]
        rig.script_run(FINISHED)
        slot.step()
        slot.step()
        with self.assertLogs("slop_haus_supervisor.fleet.slot", level=logging.INFO) as captured:
            slot.step()
        text = "\n".join(r.getMessage() for r in captured.records)
        self.assertIn("paused: unpushed work for ticket-7-a1 could not be pushed: push to https://host/r.git rejected", text)
        self.assertNotIn("tok-12345", text)
        self.assertEqual(["agent-1 paused: unpushed work for ticket-7-a1 could not be pushed: push to https://host/r.git rejected"], rig.alerts.messages)

    def test_secrets_in_the_fakes_never_reach_the_log(self) -> None:
        secret = "sk-secret-value"
        rig = Rig()
        rig.briefing.default = f"briefing {secret}"
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [Claim(7, "ticket-7-a1", secret, variant=secret, session_id=None)]
        rig.client.states = [NO_LEASE]
        rig.script_run(RunOutcome(OutcomeKind.FINISHED, detail=secret))
        with self.logged() as lines:
            rig.slot.step()
            rig.slot.step()
        self.assertGreater(len(lines), 3)
        self.assertNotIn(secret, "\n".join(lines))


if __name__ == "__main__":
    unittest.main()


class BriefingTest(unittest.TestCase):
    def test_each_run_carries_its_slots_own_briefing(self) -> None:
        first, second = Rig(), Rig()
        first.briefing.default = "briefing of agent-1"
        second.briefing.default = "briefing of agent-2"
        started(first, None)
        started(second, None)
        self.assertEqual(first.harness.calls[0].spec.system_prompt, "briefing of agent-1")
        self.assertEqual(second.harness.calls[0].spec.system_prompt, "briefing of agent-2")

    def test_a_missing_briefing_refuses_the_run_before_claiming(self) -> None:
        rig = Rig()
        rig.briefing.default = None
        rig.client.events = [CLAIMABLE]
        rig.client.claims = [CLAIM]
        with self.assertRaises(BriefingUnavailable):
            rig.slot.step()
        self.assertEqual(rig.client.count("claim"), 0)
        self.assertEqual(rig.harness.calls, [])
        self.assertEqual(rig.slot.phase, Phase.IDLE)

    def test_the_briefing_is_loaded_again_for_a_resumed_run(self) -> None:
        rig = Rig()
        started(rig, FINISHED)
        rig.briefing.default = "updated on main"
        rig.script_run(None, session_id="s2")
        rig.slot.step()
        self.assertEqual(rig.harness.verbs, ["start", "resume"])
        self.assertEqual(rig.harness.calls[1].spec.system_prompt, "updated on main")


class PauseWiringTest(unittest.TestCase):
    """The slot ends a pause on the schedule PauseState gives it; PauseState's own rules are tested on their own."""

    def _paused_on_dirty_checkout(self) -> Rig:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "the slot checkout has uncommitted changes")
        started(rig, None)
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        return rig

    def test_dirty_checkout_pause_clears_itself_once_clean(self) -> None:
        rig = self._paused_on_dirty_checkout()
        rig.clock.advance(PAUSE_RECHECK_INTERVAL + 1)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        rig.repo.prepare_result = Prepared(True)
        rig.clock.advance(PAUSE_RECHECK_INTERVAL + 1)
        with self.assertLogs("slop_haus_supervisor.fleet.slot", level=logging.INFO) as captured:
            rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertTrue(any("slot=agent-1 unpaused: " in r.getMessage() and "cleared" in r.getMessage() for r in captured.records))

    def test_no_recheck_before_the_interval(self) -> None:
        rig = self._paused_on_dirty_checkout()
        rig.repo.prepare_result = Prepared(True)
        rig.clock.advance(PAUSE_RECHECK_INTERVAL - 1)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)

    def test_unpushed_work_pause_clears_when_the_work_is_pushed(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="rejected")
        started(rig, FINISHED, state=NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        rig.clock.advance(PAUSE_RECHECK_INTERVAL + 1)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        rig.repo.unpushed = False
        rig.clock.advance(PAUSE_RECHECK_INTERVAL + 1)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)

    def test_login_failure_pause_is_not_cleared_automatically_but_by_request(self) -> None:
        rig = Rig()
        started(rig, RunOutcome(OutcomeKind.AUTH_FAILED))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        rig.clock.advance(10 * PAUSE_RECHECK_INTERVAL)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.PAUSED)
        self.assertEqual(rig.slot.try_unpause(manual=True), "")
        self.assertEqual(rig.slot.phase, Phase.IDLE)

    def test_the_snapshot_carries_the_masked_reason_while_paused(self) -> None:
        rig = Rig()
        rig.repo.prepare_result = Prepared(False, "git fetch failed: https://user:tok@host/r.git\nrefused")
        started(rig, None)
        self.assertNotIn("tok", rig.slot.snapshot().pause_reason)
        rig.slot.unpause()
        self.assertEqual("", rig.slot.snapshot().pause_reason)
        self.assertIsNone(rig.slot.pause_info())


HELD_LEASE = WorkerState(True, ticket_id=7, attempt=1, session_id="old-session")


class AdoptLiveLeaseTest(unittest.TestCase):
    def _restarted(self, state: WorkerState) -> Rig:
        rig = Rig()
        rig.client.states = [state]
        return rig

    def test_a_lock_holder_resumes_its_session_with_the_lock_held_prompt(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, lock=LockState.HELD, checks=ChecksState.PASSED))
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.RUNNING)
        self.assertEqual(rig.client.count("poll"), 0)
        self.assertEqual(rig.client.count("claim"), 0)
        call = rig.harness.calls[0]
        self.assertEqual((call.verb, call.session_id, call.spec.prompt), ("resume", "old-session", policy.LOCK_HELD_PROMPT))
        self.assertEqual((call.spec.ticket_id, call.spec.branch), (7, "ticket-7-a1"))
        self.assertEqual(rig.repo.prepared, ["ticket-7-a1"])

    def test_an_adopted_lease_runs_in_the_tickets_repo_of_a_nested_workspace(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "app" / ".git").mkdir(parents=True)
            rig = self._restarted(replace(HELD_LEASE, lock=LockState.HELD, checks=ChecksState.PASSED, paths=("app/src",)))
            rig.slot.directory = Path(tmp)
            rig.script_run(None)
            rig.slot.step()
            self.assertEqual(rig.harness.calls[0].spec.directory, Path(tmp) / "app")

    def test_a_resumed_lock_holder_still_trips_the_slow_queue_alert(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, lock=LockState.HELD, checks=ChecksState.PASSED))
        rig.script_run(None)
        rig.slot.step()
        rig.clock.advance(rig.config.lock_alert_after)
        rig.slot.step()
        self.assertTrue(any("held the integration lock" in m for m in rig.alerts.messages))

    def test_pending_checks_without_the_lock_wait_for_checks(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, checks=ChecksState.PENDING))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_CHECKS)
        self.assertEqual(rig.harness.calls, [])

    def test_a_queued_slot_waits_for_the_lock(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, lock=LockState.QUEUED, checks=ChecksState.PASSED))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_LOCK)
        self.assertEqual(rig.client.count("take_lock"), 0)

    def test_passed_checks_with_no_lock_and_no_place_queue_again(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, checks=ChecksState.PASSED))
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_LOCK)
        self.assertEqual(rig.client.count("take_lock"), 1)
        self.assertEqual(rig.harness.calls, [])

    def test_a_lease_without_an_attempt_is_a_pathless_ticket_and_gets_no_branch(self) -> None:
        rig = self._restarted(replace(HELD_LEASE, attempt=None, lock=LockState.HELD))
        rig.script_run(None)
        rig.slot.step()
        self.assertEqual(rig.repo.prepared, [])
        self.assertEqual(rig.harness.calls[0].spec.branch, "")

    def test_no_live_lease_polls_for_work_as_before(self) -> None:
        rig = self._restarted(NO_LEASE)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.IDLE)
        self.assertEqual(rig.client.count("poll"), 1)

    def test_the_lease_is_looked_for_once(self) -> None:
        rig = self._restarted(NO_LEASE)
        rig.slot.step()
        rig.slot.step()
        self.assertEqual(rig.client.count("state"), 1)

    def test_a_failed_read_is_tried_again_before_any_poll(self) -> None:
        rig = self._restarted(NO_LEASE)
        rig.client.fail_once["state"] = OSError("api down")
        with self.assertRaises(OSError):
            rig.slot.step()
        self.assertEqual(rig.client.count("poll"), 0)
        rig.slot.step()
        self.assertEqual(rig.client.count("poll"), 1)


class ReleaseLockForStopTest(unittest.TestCase):
    def test_a_lock_holder_at_a_wait_point_unlocks_once_and_keeps_its_lease(self) -> None:
        rig = Rig()
        held = WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING)
        started(rig, FINISHED, state=held)
        rig.slot.step()
        self.assertEqual(rig.slot.phase, Phase.WAIT_CHECKS)
        rig.slot.release_lock_for_stop()
        self.assertEqual(rig.client.count("release_lock"), 1)
        self.assertEqual(rig.client.notes(), [])
        self.assertEqual(rig.slot.snapshot().ticket_id, 7)

    def test_a_slot_not_holding_the_lock_leaves_it_alone(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.QUEUED, checks=ChecksState.PASSED))
        rig.slot.step()
        rig.slot.release_lock_for_stop()
        self.assertEqual(rig.client.count("release_lock"), 0)

    def test_an_idle_slot_asks_nothing(self) -> None:
        rig = Rig()
        rig.slot.release_lock_for_stop()
        self.assertEqual(rig.client.calls, [])

    def test_a_run_cut_short_releases_the_lock_it_holds(self) -> None:
        rig = Rig()
        started(rig, None, state=WorkerState(True, lock=LockState.HELD))
        rig.slot.abandon_run()
        self.assertEqual(rig.client.count("release_lock"), 1)

    def test_a_failing_state_read_does_not_stop_the_stop(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING))
        rig.slot.step()
        rig.client.fail_once["state"] = OSError("api down")
        rig.slot.release_lock_for_stop()
        self.assertEqual(rig.client.count("release_lock"), 0)

    def test_a_failing_unlock_does_not_stop_the_stop(self) -> None:
        rig = Rig()
        started(rig, FINISHED, state=WorkerState(True, lock=LockState.HELD, checks=ChecksState.PENDING))
        rig.slot.step()
        rig.client.fail_once["release_lock"] = OSError("api down")
        rig.slot.release_lock_for_stop()
        self.assertEqual(rig.client.count("release_lock"), 1)
