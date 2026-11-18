from __future__ import annotations

import unittest
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from unittest import mock

from slop_haus_supervisor.remote_control.host.activity import CallLines
from slop_haus_supervisor.remote_control.host.codex_records import CodexRecords
from host_fixtures import FakeReporter


def breakdown(input: int, cached: int, output: int, written: int = 0) -> dict[str, int]:
    return {
        "inputTokens": input, "cachedInputTokens": cached, "cacheWriteInputTokens": written,
        "outputTokens": output, "reasoningOutputTokens": 0, "totalTokens": input + output,
    }


def usage_record(total: dict[str, int], last: dict[str, int]) -> dict[str, Any]:
    return {"method": "thread/tokenUsage/updated", "params": {"tokenUsage": {"total": total, "last": last}}}


class CodexRecordsUsageTest(unittest.TestCase):
    def setUp(self) -> None:
        self.turns = mock.MagicMock()
        self.records = CodexRecords(mock.MagicMock(), self.turns, Path("/nonexistent"))
        self.live: Any = SimpleNamespace(generation=1)
        self.conv: Any = mock.MagicMock()
        self.conv.state.codex_totals = {}
        self.conv.turn_active = True

    def send(self, method: str, params: dict[str, Any] | None = None) -> None:
        self.records.on_record(self.conv, self.live, {"method": method, "params": params or {}})

    def usage(self, record: dict[str, Any]) -> None:
        self.records.on_record(self.conv, self.live, record)

    def end_turn(self) -> dict[str, int]:
        self.records.state_of(self.live).model = "gpt-test"
        self.send("turn/completed", {"turn": {"status": "completed"}})
        self.turns.finish_turn.call_args[0][1]()
        return self.conv.usage.add_delta.call_args[0][3]

    def test_the_replay_of_a_resumed_thread_is_not_the_turns_usage(self) -> None:
        standing = breakdown(1000, 400, 50)
        self.usage(usage_record(standing, standing))
        self.send("turn/started")
        self.usage(usage_record(breakdown(1100, 420, 57), breakdown(100, 20, 7)))
        delta = self.end_turn()
        self.assertEqual((80, 20, 7), (delta["input"], delta["cache_read"], delta["output"]))

    def test_without_a_replay_the_first_notification_of_the_turn_is_its_own(self) -> None:
        self.send("turn/started")
        self.usage(usage_record(breakdown(1100, 420, 57), breakdown(100, 20, 7)))
        delta = self.end_turn()
        self.assertEqual((80, 20, 7), (delta["input"], delta["cache_read"], delta["output"]))

    def test_a_replay_before_a_later_turn_is_not_taken_for_that_turns_usage(self) -> None:
        self.send("turn/started")
        self.usage(usage_record(breakdown(100, 20, 7), breakdown(100, 20, 7)))
        self.end_turn()
        # A fresh thread: the host forgets its totals, and Codex states the new thread's standing first.
        self.conv.state.codex_totals = {}
        self.usage(usage_record(breakdown(500, 0, 50), breakdown(500, 0, 50)))
        self.send("turn/started")
        self.usage(usage_record(breakdown(560, 0, 55), breakdown(60, 0, 5)))
        delta = self.end_turn()
        self.assertEqual((60, 5), (delta["input"], delta["output"]))

    def test_cache_writes_are_counted_apart_from_input(self) -> None:
        self.send("turn/started")
        self.usage(usage_record(breakdown(100, 20, 7, written=30), breakdown(100, 20, 7, written=30)))
        delta = self.end_turn()
        self.assertEqual((50, 20, 30), (delta["input"], delta["cache_read"], delta["cache_write"]))


class CodexRecordsActivityTest(unittest.TestCase):
    def setUp(self) -> None:
        self.reporter = FakeReporter()
        self.records = CodexRecords(self.reporter, mock.MagicMock(), Path("/nonexistent"))
        self.live: Any = SimpleNamespace(generation=1)
        self.conv: Any = mock.MagicMock()
        self.conv.state.id = "c"
        self.conv.calls = CallLines()

    def item(self, method: str, item: dict[str, Any]) -> None:
        self.records.on_record(self.conv, self.live, {"method": method, "params": {"item": item}})

    def test_a_message_carries_its_item_id_as_its_block(self) -> None:
        self.item("item/completed", {"type": "agentMessage", "id": "i1", "text": "One"})
        self.item("item/completed", {"type": "agentMessage", "id": "i2", "text": "Two"})
        self.assertEqual([("One", "i1"), ("Two", "i2")], [(e["text"], e["block"]) for e in self.reporter.of("assistant")])

    def test_a_tool_call_is_its_name_and_never_its_command_or_output(self) -> None:
        secret = "sk-live-0123456789abcdef"
        command = {"type": "commandExecution", "id": "c1", "command": f"echo {secret}", "aggregatedOutput": secret}
        self.item("item/started", command)
        self.item("item/completed", dict(command, status="failed"))
        change = {"type": "fileChange", "id": "f1", "changes": [{"path": "/a/b/main.py", "diff": secret}]}
        self.item("item/started", change)
        self.item("item/completed", dict(change, status="completed"))
        mcp = {"type": "mcpToolCall", "id": "m1", "tool": "ticket_get", "arguments": {"token": secret}, "result": secret}
        self.item("item/started", mcp)
        self.item("item/completed", dict(mcp, status="completed"))
        got = [(e["call_id"], e["tool"], e["line"], e["status"]) for e in self.reporter.of("activity")]
        self.assertEqual([
            ("c1", "shell", "", "running"), ("c1", "shell", "", "failed"),
            ("f1", "edit", "main.py", "running"), ("f1", "edit", "main.py", "done"),
            ("m1", "ticket_get", "", "running"), ("m1", "ticket_get", "", "done"),
        ], got)
        self.assertNotIn(secret, repr(self.reporter.events))


if __name__ == "__main__":
    unittest.main()
