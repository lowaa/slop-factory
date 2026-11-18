from __future__ import annotations

import unittest

from fakes import CLAIM, Rig
from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.slophaus.client import Claim
from slop_haus_supervisor.slophaus.http_client import ApiError
from slop_haus_supervisor.slophaus.transport import ApiError as TransportError
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.harness_config import HarnessConfigError
from slop_haus_supervisor.workspace.handover import SafetyPush
from test_slot import LEASED, started_with

REFUSED = EnvFileError("run.env:2: refusing SLOP_HAUS_CODEX_SHARED_LOGIN")


class ConfigurationStartFailureTest(unittest.TestCase):
    """A start that fails on the host's configuration fails every retry: the ticket goes back and the slot pauses."""

    def test_a_refused_run_env_line_pauses_the_slot_and_releases_the_ticket(self) -> None:
        rig = Rig()
        rig.harness.launch_errors = [REFUSED]
        started_with(rig)

        self.assertEqual(Phase.PAUSED, rig.slot.phase)
        pause_reason = rig.slot.snapshot().pause_reason
        self.assertIn("EnvFileError", pause_reason)
        self.assertIn("refusing SLOP_HAUS_CODEX_SHARED_LOGIN", pause_reason)
        releases = [c[1] for c in rig.client.calls if c[0] == "release"]
        self.assertEqual(1, len(releases))
        self.assertIn("refusing SLOP_HAUS_CODEX_SHARED_LOGIN", releases[0])
        self.assertEqual(0, rig.client.count("escalate"), "a host misconfiguration is no question about the ticket")
        self.assertIsNone(rig.slot.snapshot().ticket_id)
        self.assertTrue(any("paused" in m for m in rig.alerts.messages))

    def test_every_configuration_error_is_handed_back_at_once(self) -> None:
        for err in (REFUSED, ConfigError("no slots"), HarnessConfigError("unknown model")):
            with self.subTest(type(err).__name__):
                rig = Rig()
                rig.harness.launch_errors = [err]
                started_with(rig)
                self.assertEqual(Phase.PAUSED, rig.slot.phase)
                self.assertEqual(1, rig.client.count("release"))

    def test_a_refused_release_escalates_the_ticket_instead(self) -> None:
        rig = Rig()
        rig.client.fail_once["release"] = ApiError(403, "not yours")
        rig.harness.launch_errors = [REFUSED]
        started_with(rig)
        self.assertEqual(Phase.PAUSED, rig.slot.phase)
        self.assertEqual(1, rig.client.count("escalate"))
        self.assertIsNone(rig.slot.snapshot().ticket_id)

    def test_the_pause_holds_until_a_person_unpauses(self) -> None:
        rig = Rig()
        rig.harness.launch_errors = [ConfigError("set CLAUDE_CODE_OAUTH_TOKEN in secrets.env")]
        started_with(rig)
        rig.clock.advance(3600)
        rig.slot.step()
        self.assertEqual(Phase.PAUSED, rig.slot.phase)
        self.assertEqual("", rig.slot.try_unpause(manual=True))
        self.assertEqual(Phase.IDLE, rig.slot.phase)
        self.assertEqual(1, rig.client.count("claim"))

    def test_unpushed_work_is_pushed_before_the_release(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.harness.launch_errors = [REFUSED]
        started_with(rig)
        self.assertEqual(["ticket-7-a1"], rig.repo.pushes)
        self.assertEqual(1, rig.client.count("release"))

    def test_work_that_could_not_be_pushed_goes_to_a_person_instead(self) -> None:
        rig = Rig()
        rig.repo.unpushed = True
        rig.repo.push_result = SafetyPush(False, error="rejected")
        rig.harness.launch_errors = [REFUSED]
        started_with(rig)
        self.assertEqual(0, rig.client.count("release"))
        self.assertEqual(1, rig.client.count("escalate"))
        self.assertEqual(Phase.PAUSED, rig.slot.phase)

    def test_known_secrets_never_reach_the_release_note(self) -> None:
        rig = Rig(masker=SecretMasker(["hunter2-secret"]))
        rig.harness.launch_errors = [ConfigError("bad value hunter2-secret")]
        started_with(rig)
        self.assertNotIn("hunter2-secret", rig.client.notes_of("release")[0])
        self.assertNotIn("hunter2-secret", rig.slot.snapshot().pause_reason)

    def test_a_release_that_fails_is_tried_again_while_the_ticket_is_held(self) -> None:
        rig = Rig()
        rig.client.fail_once["release"] = ApiError(0, "server unreachable")
        rig.harness.launch_errors = [REFUSED, REFUSED]
        started_with(rig)
        self.assertEqual(Phase.BACKOFF, rig.slot.phase)
        self.assertEqual(7, rig.slot.snapshot().ticket_id)
        rig.clock.advance(rig.config.backoffs[-1])
        rig.slot.step()
        self.assertEqual(Phase.PAUSED, rig.slot.phase)
        self.assertEqual(2, rig.client.count("release"))


class TransientStartFailureTest(unittest.TestCase):
    """Any other start failure retries the ticket the slot holds, with backoff, never a fresh claim."""

    def test_the_held_ticket_is_retried_after_the_backoff(self) -> None:
        rig = Rig()
        rig.harness.launch_errors = [OSError("fork failed")]
        rig.script_run(None)
        started_with(rig)

        self.assertEqual(Phase.BACKOFF, rig.slot.phase)
        snapshot = rig.slot.snapshot()
        self.assertEqual((Reason.BACKOFF_AFTER_ERROR.value, 7), (snapshot.reason, snapshot.ticket_id))
        rig.clock.advance(rig.config.backoffs[0] - 1)
        rig.slot.step()
        self.assertEqual(Phase.BACKOFF, rig.slot.phase, "not before the backoff")
        rig.clock.advance(1)
        rig.slot.step()

        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        self.assertEqual(1, rig.client.count("claim"))
        idle_polls = [c for c in rig.client.calls if c[0] == "poll" and c[1] != "0"]
        self.assertEqual(1, len(idle_polls), "the retry does not look for new work")
        self.assertEqual(["start", "start"], rig.harness.verbs)
        self.assertEqual(rig.harness.calls[0].spec.prompt, rig.harness.calls[1].spec.prompt)
        self.assertIn(("record_session", "fake", "s1"), rig.client.calls)

    def test_the_retry_resumes_the_recorded_session(self) -> None:
        rig = Rig()
        rig.harness.launch_errors = [OSError("fork failed")]
        rig.script_run(None, session_id="old")
        started_with(rig, claim=Claim(7, "ticket-7-a1", "dead", session_id="old"))
        rig.clock.advance(rig.config.backoffs[0])
        rig.slot.step()
        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        self.assertEqual(["resume", "resume"], rig.harness.verbs)
        self.assertEqual("old", rig.harness.calls[1].session_id)
        self.assertEqual(0, rig.client.count("record_session"))

    def test_backoff_grows_and_the_ticket_goes_back_once_retries_are_spent(self) -> None:
        rig = Rig()
        backoffs = rig.config.backoffs
        rig.harness.launch_errors = [OSError("fork failed")] * (len(backoffs) + 1)
        started_with(rig)
        for delay in backoffs:
            self.assertEqual(Phase.BACKOFF, rig.slot.phase)
            rig.clock.advance(delay - 1)
            rig.slot.step()
            self.assertEqual(Phase.BACKOFF, rig.slot.phase)
            rig.clock.advance(1)
            rig.slot.step()
        self.assertEqual(Phase.PAUSED, rig.slot.phase)
        self.assertEqual(1, rig.client.count("release"))
        self.assertEqual(1, rig.client.count("claim"))

    def test_a_started_run_resets_the_count(self) -> None:
        rig = Rig()
        backoffs = rig.config.backoffs
        rig.harness.launch_errors = [OSError("fork failed")]
        rig.script_run(RunOutcome(OutcomeKind.FINISHED))
        started_with(rig, state=LEASED)
        rig.clock.advance(backoffs[0])
        rig.slot.step()
        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        # The run ends with the ticket still held, so the next start is a resume that fails as often as the first.
        rig.harness.launch_errors = [OSError("fork failed")] * len(backoffs)
        rig.script_run(None)
        rig.slot.step()
        for delay in backoffs:
            self.assertEqual(Phase.BACKOFF, rig.slot.phase)
            rig.clock.advance(delay)
            rig.slot.step()
        self.assertEqual(Phase.RUNNING, rig.slot.phase)
        self.assertEqual(0, rig.client.count("release"))

    def test_a_network_error_dressed_as_a_config_error_is_transient(self) -> None:
        rig = Rig()
        rig.harness.launch_errors = [TransportError("connection reset")]
        started_with(rig)
        self.assertEqual(Phase.BACKOFF, rig.slot.phase)
        self.assertEqual(0, rig.client.count("release"))


class HeldWithoutRunTest(unittest.TestCase):
    def test_an_idle_slot_holding_a_ticket_says_so_not_no_claimable(self) -> None:
        rig = Rig()

        def broken(*_args: object) -> None:
            raise RuntimeError("git crashed")

        rig.repo.prepare_branch = broken  # pyright: ignore[reportAttributeAccessIssue]
        with self.assertRaises(RuntimeError):
            started_with(rig)
        snapshot = rig.slot.snapshot()
        self.assertEqual((Phase.IDLE.value, Reason.OTHER.value, 7), (snapshot.phase, snapshot.reason, snapshot.ticket_id))
        self.assertEqual(f"holds ticket #{CLAIM.ticket_id} with no run", snapshot.reason_detail)


if __name__ == "__main__":
    unittest.main()
