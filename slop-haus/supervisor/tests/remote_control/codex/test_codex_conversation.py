from __future__ import annotations

import json
import os
from unittest import mock

from codex_fixtures import THREAD, CodexHostCase
from host_fixtures import CONV, item


class CodexConversationTest(CodexHostCase):
    def test_an_interrupt_from_the_owner_ends_the_turn_as_stopped_by_you(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "hold"}):
            self.start()
            self.message(2, "work")
            # Codex names the turn at turn/started; an interrupt before that has no turn to name.
            self.pump(lambda: self.host.records.state_of(self.live()).turn_started)  # pyright: ignore[reportAttributeAccessIssue, reportUnknownLambdaType, reportUnknownMemberType]
            self.handle(item(3, "interrupt", turn=1))
            self.pump(lambda: self.turns() == 1)
        self.assertEqual(1, len(self.requests("turn/interrupt")))
        self.assertIn("stopped by you", self.reporter.said())
        self.assertNotIn("the turn ended", self.reporter.said())

    def test_a_message_is_a_turn_and_the_reply_is_reported(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.assertEqual(["hello"], [p["input"][0]["text"] for p in self.requests("turn/start")])
        self.assertEqual("echo: hello", self.reporter.said())
        self.assertEqual([1], [e["turn"] for e in self.reporter.of("turn_end")])

    def test_the_handshake_comes_before_the_first_turn_and_carries_the_briefing(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.assertEqual(["initialize", "initialized", "thread/start", "turn/start"], [r["method"] for r in self.received()])
        params = self.requests("thread/start")[0]
        self.assertEqual("the briefing", params["developerInstructions"])
        self.assertEqual(("never", "danger-full-access"), (params["approvalPolicy"], params["sandbox"]))

    def test_a_message_sent_before_the_thread_exists_is_held_for_it(self) -> None:
        """begin_turn writes at once; the handshake is still in flight."""
        self.start()
        self.message(2, "one")
        self.message(3, "two")
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(["one", "two"], [p["input"][0]["text"] for p in self.requests("turn/start")])
        self.assertEqual("echo: oneecho: two", self.reporter.said())

    def test_the_thread_is_resumed_after_a_restart_and_the_briefing_goes_again(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        self.converse(3, "two")
        (resume,) = self.requests("thread/resume")
        self.assertEqual(THREAD, resume["threadId"])
        self.assertEqual("the briefing", resume["developerInstructions"])
        self.assertEqual(1, len(self.requests("thread/start")))

    def test_a_thread_codex_lost_continues_in_a_new_one_and_the_owner_is_told(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        with mock.patch.dict(os.environ, {"FAKE_MODE": "resume-missing"}):
            self.converse(3, "two")
        self.assertEqual(2, len(self.requests("thread/start")))
        self.assertIn("continues in a new one", self.reporter.said())
        self.assertIn("echo: two", self.reporter.said())

    def test_a_conversation_the_host_thinks_exists_but_has_no_thread_file_starts_a_new_thread(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        for name in os.listdir(self.state_dir / "codex-threads"):
            os.unlink(self.state_dir / "codex-threads" / name)
        self.converse(3, "two")
        self.assertEqual([], self.requests("thread/resume"))
        self.assertEqual(2, len(self.requests("thread/start")))
        self.assertIn("continues in a new one", self.reporter.said())

    def test_a_turn_codex_refuses_to_start_ends_with_the_reason(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "turn-error", "FAKE_ERROR": "model not found"}):
            self.start()
            self.converse(2, "hello")
        self.assertIn("model not found", self.reporter.said())

    def test_a_thread_the_host_never_recorded_as_created_is_still_found(self) -> None:
        """The host died after Codex named the thread and before it saved the conversation."""
        self.start()
        self.converse(2, "one")
        self.host.shutdown()
        path = self.host.store.path(CONV)
        with open(path) as f:
            state = json.load(f)
        state["created"] = False
        with open(path, "w") as f:
            json.dump(state, f)
        self.restart_host()
        self.converse(3, "two")
        self.assertEqual(THREAD, self.requests("thread/resume")[0]["threadId"])
        self.assertEqual(1, len(self.requests("thread/start")))

    def test_a_thread_that_cannot_be_kept_ends_the_turn_with_the_reason(self) -> None:
        self.state_dir.mkdir(parents=True, exist_ok=True)
        (self.state_dir / "codex-threads").write_text("in the way")
        self.start()
        self.converse(2, "hello")
        self.assertIn("could not keep the codex thread", self.reporter.said())
        self.assertEqual(1, self.turns())

    def test_a_failed_turn_is_shown_to_the_owner_and_ends(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "fail", "FAKE_ERROR": "usage limit reached"}):
            self.start()
            self.converse(2, "hello")
        self.assertIn("usage limit reached", self.reporter.said())
        self.assertEqual(1, self.turns())

    def test_a_request_for_approval_is_declined_and_the_turn_goes_on(self) -> None:
        with mock.patch.dict(os.environ, {"FAKE_MODE": "ask"}):
            self.start()
            self.converse(2, "hello")
        self.assertEqual("echo: hello", self.reporter.said())
        with open(os.path.join(self.logs, "answers.jsonl")) as f:
            self.assertIn('"decline"', f.read())

    def test_the_process_is_codexs_app_server_in_the_hosts_own_home(self) -> None:
        self.start()
        (invocation,) = self.invocations(1)
        self.assertEqual(["app-server"], invocation["argv"])
        self.assertEqual(str(self.codex_home), invocation["env"]["CODEX_HOME"])
        self.assertEqual(self.root, invocation["cwd"])

    def test_a_conversation_id_that_is_no_uuid_is_refused_before_it_names_a_file(self) -> None:
        harness = self.host.harness
        with self.assertRaises(ValueError):
            harness.open_session(mock.MagicMock(), "../../x")

    def test_usage_is_the_turns_change_in_the_threads_totals_and_not_the_totals(self) -> None:
        self.login({"auth_mode": "chatgpt", "tokens": {"id_token": "x"}})
        self.start()
        self.converse(2, "one")
        self.converse(3, "two")
        record = self.usage_record()
        self.assertEqual("codex", self.reporter.usage[-1]["harness"])
        self.assertEqual(CONV, self.reporter.usage[-1]["conversation_id"])
        self.assertEqual(("openai", "gpt-test-1", "subscription"), (record["provider"], record["model"], record["billing_mode"]))
        # Cached input is counted apart from input: 100 - 20 a turn.
        self.assertEqual((160, 14, 40, 2), (record["input_tokens"], record["output_tokens"], record["cache_read_tokens"], record["requests"]))

    def test_usage_after_a_restart_goes_on_from_the_threads_totals(self) -> None:
        self.start()
        self.converse(2, "one")
        self.restart_host()
        self.converse(3, "two")
        self.assertEqual(160, self.usage_record()["input_tokens"], "not 240, which is what adding the totals up would say")

    def test_an_api_key_login_is_metered(self) -> None:
        self.login({"auth_mode": "apikey"})
        self.start()
        self.converse(2, "one")
        self.assertEqual("metered", self.usage_record()["billing_mode"])
