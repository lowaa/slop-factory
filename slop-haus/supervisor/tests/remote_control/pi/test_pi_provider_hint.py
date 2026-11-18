from __future__ import annotations

import os
from unittest import mock

from host_fixtures import CONV
from pi_fixtures import PiHostCase


class PiProviderHintTest(PiHostCase):
    """pi runs with the host's own agent directory, so a provider that is only in the owner's
    ~/.pi/agent is unknown to it; pi does not say that."""

    BENIGN = f"Warning: No project session found with id '{CONV}'; creating a new session with that id."

    def exits_with(self, stderr: str) -> str:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "exit-at-start", "FAKE_ERROR": stderr}):
            self.start()
            self.live().session.exit_code()
            self.pump(lambda: "the harness exited" in self.reporter.said())
        return self.reporter.said()

    def refused_with(self, error: str) -> str:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "reject", "FAKE_ERROR": error}):
            self.start()
            self.converse(2, "hello")
        return self.reporter.said()

    def assert_hint(self, said: str) -> None:
        self.assertIn(str(self.agent_dir), said)
        for name in ("models.json", "auth.json", "run.env", "~/.pi/agent"):
            self.assertIn(name, said)

    def test_an_unknown_provider_at_start_up_says_whose_directory_it_is(self) -> None:
        said = self.exits_with(self.BENIGN + '\nError: Unknown provider "scripted". Use --list-models to see available providers/models.')
        self.assertIn('Unknown provider "scripted"', said)
        self.assert_hint(said)

    def test_no_models_at_start_up_gets_the_hint(self) -> None:
        self.assert_hint(self.exits_with("Error: No models available. Check your installation or add models to models.json."))

    def test_no_api_key_at_start_up_gets_the_hint(self) -> None:
        self.assert_hint(self.exits_with('No API key found for "anthropic"'))

    def test_the_no_key_wording_for_a_model_gets_the_hint(self) -> None:
        self.assert_hint(self.refused_with("No API key for anthropic/claude-test-1"))

    def test_a_refused_message_without_a_model_gets_the_hint(self) -> None:
        self.assert_hint(self.refused_with("No model selected"))

    def test_a_refused_message_without_a_key_gets_the_hint(self) -> None:
        self.assert_hint(self.refused_with('No API key found for "anthropic"'))

    def test_the_first_start_notice_is_dropped_and_every_other_line_kept(self) -> None:
        said = self.exits_with(self.BENIGN + '\nsomething else pi said\nError: Unknown provider "x". Use --list-models.')
        self.assertNotIn("No project session found", said)
        self.assertIn("something else pi said", said)
        self.assertIn('Unknown provider "x"', said)

    def test_a_similar_warning_that_is_not_the_first_start_notice_is_kept(self) -> None:
        said = self.exits_with("Warning: No project session found for another reason, giving up")
        self.assertIn("giving up", said)

    def test_an_unrelated_error_gets_no_hint(self) -> None:
        said = self.exits_with("Error: out of memory")
        self.assertIn("out of memory", said)
        self.assertNotIn("models.json", said)
        self.assertNotIn(str(self.agent_dir), said)

    def test_an_unrelated_refusal_gets_no_hint(self) -> None:
        said = self.refused_with("session is busy")
        self.assertIn("session is busy", said)
        self.assertNotIn("models.json", said)
