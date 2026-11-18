from __future__ import annotations

import unittest
from types import SimpleNamespace
from typing import Any
from unittest import mock

from slop_haus_supervisor.remote_control.host.claude_records import ClaudeRecords
from slop_haus_supervisor.remote_control.host.activity import CallLines
from host_fixtures import FakeReporter


def start(message_id: str) -> dict[str, Any]:
    return {"type": "stream_event", "event": {"type": "message_start", "message": {"id": message_id}}}


def block_start(index: int) -> dict[str, Any]:
    return {"type": "stream_event", "event": {"type": "content_block_start", "index": index, "content_block": {"type": "text", "text": ""}}}


def delta(index: int, text: str) -> dict[str, Any]:
    return {"type": "stream_event", "event": {"type": "content_block_delta", "index": index, "delta": {"type": "text_delta", "text": text}}}


def assistant(message_id: str, *content: dict[str, Any]) -> dict[str, Any]:
    return {"type": "assistant", "message": {"id": message_id, "model": "m", "content": list(content)}}


def text(body: str) -> dict[str, Any]:
    return {"type": "text", "text": body}


def tool(tool_id: str) -> dict[str, Any]:
    return {"type": "tool_use", "id": tool_id, "name": "Bash", "input": {}}


def tool_result(tool_id: str) -> dict[str, Any]:
    return {"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": tool_id, "content": "ok"}]}}


SECRET = "sk-live-0123456789abcdef"


def described_tool(tool_id: str, name: str, **tool_input: Any) -> dict[str, Any]:
    return {"type": "tool_use", "id": tool_id, "name": name, "input": tool_input}


def failed_result(tool_id: str) -> dict[str, Any]:
    return {"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": tool_id, "is_error": True, "content": SECRET}]}}


class ClaudeRecordsTextTest(unittest.TestCase):
    def setUp(self) -> None:
        self.reporter = FakeReporter()
        self.records = ClaudeRecords(self.reporter, mock.MagicMock())
        self.conv: Any = SimpleNamespace(state=SimpleNamespace(id="c", turns=0), streamed={}, streaming_id=None, tool_uses={}, calls=CallLines(), usage=mock.MagicMock())

    def feed(self, *records: dict[str, Any]) -> None:
        for record in records:
            self.records.on_record(self.conv, mock.MagicMock(), record)

    def test_every_block_of_a_streamed_turn_is_sent_once(self) -> None:
        self.feed(
            start("m1"), block_start(0), delta(0, "Long "), delta(0, "explanation."),
            assistant("m1", text("Long explanation.")), assistant("m1", tool("t1")), tool_result("t1"),
            start("m2"), block_start(0), delta(0, "Done."),
            assistant("m2", text("Done.")),
        )
        self.assertEqual(["Long ", "explanation.", "Done."], [e["text"] for e in self.reporter.of("assistant")])

    def test_a_block_whose_deltas_never_arrived_is_sent_from_the_finished_message(self) -> None:
        self.feed(
            start("m1"), assistant("m1", text("Long explanation.")), assistant("m1", tool("t1")), tool_result("t1"),
            start("m2"), block_start(0), delta(0, "Done."), assistant("m2", text("Done.")),
        )
        self.assertEqual(["Long explanation.", "Done."], [e["text"] for e in self.reporter.of("assistant")])

    def test_a_message_without_any_partial_events_is_sent(self) -> None:
        self.feed(assistant("m1", text("Hello")), assistant("m1", tool("t1")), assistant("m2", text("Bye")))
        self.assertEqual("HelloBye", self.reporter.said())

    def test_a_block_streamed_only_in_part_is_completed(self) -> None:
        self.feed(start("m1"), block_start(0), delta(0, "Long "), assistant("m1", text("Long explanation.")))
        self.assertEqual("Long explanation.", self.reporter.said())

    def test_two_text_blocks_in_one_message_are_each_sent_once(self) -> None:
        self.feed(
            start("m1"), block_start(0), delta(0, "A"), block_start(2), delta(2, "B"),
            assistant("m1", text("A")), assistant("m1", tool("t1")), assistant("m1", text("B")),
        )
        self.assertEqual("AB", self.reporter.said())

    def test_a_short_streamed_block_does_not_claim_a_longer_blocks_text(self) -> None:
        self.feed(
            start("m1"), block_start(0), delta(0, "Yes"), block_start(2), delta(2, "Yes, sure"),
            assistant("m1", text("Yes, sure")), assistant("m1", text("Yes")),
        )
        self.assertEqual(["Yes", "Yes, sure"], [e["text"] for e in self.reporter.of("assistant")])

    def test_text_carries_the_id_of_its_message_streamed_or_not(self) -> None:
        self.feed(
            start("m1"), block_start(0), delta(0, "Streamed"), assistant("m1", text("Streamed")),
            assistant("m2", text("Whole")),
        )
        self.assertEqual([("Streamed", "m1"), ("Whole", "m2")], [(e["text"], e["block"]) for e in self.reporter.of("assistant")])

    def test_a_tool_call_is_reported_with_the_models_description_and_then_how_it_ended(self) -> None:
        self.feed(
            assistant("m1", described_tool("t1", "Bash", command=f"curl -H 'Authorization: {SECRET}' x", description="Check pilot-3")),
            tool_result("t1"),
            assistant("m1", described_tool("t2", "Read", file_path="/home/me/src/app/main.py")),
            failed_result("t2"),
            assistant("m1", described_tool("t3", "Grep", pattern=SECRET)),
        )
        got = [(e["call_id"], e["tool"], e["line"], e["status"]) for e in self.reporter.of("activity")]
        self.assertEqual([
            ("t1", "Bash", "Check pilot-3", "running"), ("t1", "Bash", "Check pilot-3", "done"),
            ("t2", "Read", "main.py", "running"), ("t2", "Read", "main.py", "failed"),
            ("t3", "Grep", "", "running"),
        ], got)

    def test_no_event_holds_a_commands_text_or_a_results_content(self) -> None:
        self.feed(
            assistant("m1", described_tool("t1", "Bash", command=f"export TOKEN={SECRET}")),
            failed_result("t1"),
            assistant("m1", described_tool("t2", "Write", file_path="a/b.txt", content=SECRET)),
            tool_result("t2"),
        )
        self.assertGreaterEqual(len(self.reporter.of("activity")), 4)
        self.assertNotIn(SECRET, repr(self.reporter.events))

    def test_calls_still_running_when_the_turn_ends_are_reported_failed(self) -> None:
        self.feed(assistant("m1", described_tool("t1", "Bash", description="Run it")), {"type": "result"})
        self.assertEqual(["running", "failed"], [e["status"] for e in self.reporter.of("activity")])
        self.assertEqual({}, self.conv.calls.calls)

    def test_a_line_is_one_short_clean_line(self) -> None:
        self.feed(assistant("m1", described_tool("t1", "Bash", description="a\nb\x07 see https://u:pw@host/ " + "x" * 300)))
        [event] = self.reporter.of("activity")
        self.assertNotIn("\n", event["line"])
        self.assertNotIn("\x07", event["line"])
        self.assertNotIn("pw@", event["line"])
        self.assertLessEqual(len(event["line"]), 120)


if __name__ == "__main__":
    unittest.main()
