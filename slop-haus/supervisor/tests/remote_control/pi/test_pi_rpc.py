from __future__ import annotations

import unittest
from collections.abc import Callable
from pathlib import Path
from typing import Any

from slop_haus_supervisor.remote_control.host.conversation import Conversation, ConversationState, StateStore
from slop_haus_supervisor.remote_control.pi.errors import PiErrors
from slop_haus_supervisor.remote_control.pi.rpc import PiRpc


class Recorder:
    """Stands in for the host and the reporter: it records what PiRpc asks of them."""

    def __init__(self) -> None:
        self.ended: list[Conversation] = []
        self.errors: list[str] = []
        self.texts: list[str] = []
        self.blocks: list[str] = []
        self.activities: list[tuple[str, str, str, str]] = []

    def text(self, conversation: str, text: str, block: str = "") -> int:
        self.texts.append(text)
        self.blocks.append(block)
        return 0

    def activity(self, conversation: str, call_id: str, tool: str, line: str, status: str) -> int:
        self.activities.append((call_id, tool, line, status))
        return 0

    def report_error_text(self, conv: Conversation, reason: str) -> None:
        self.errors.append(reason)

    def finish_turn(self, conv: Conversation, add_usage: Callable[[], None]) -> None:
        self.ended.append(conv)


class FakeLive:
    """Only what PiRpc reads of a LiveHarness."""

    def __init__(self, generation: int = 1) -> None:
        self.generation = generation
        self.written: list[str] = []

    def write(self, line: str) -> None:
        self.written.append(line)


class PiRpcTest(unittest.TestCase):
    def setUp(self) -> None:
        self.seen = Recorder()
        self.rpc = PiRpc(self.seen, self.seen, PiErrors(Path("/agent")), Path("/agent"))
        self.conv = Conversation(ConversationState(id="c"), StateStore("/nowhere"))
        self.live = FakeLive()

    def feed(self, record: dict[str, Any], live: FakeLive | None = None) -> None:
        self.rpc.on_record(self.conv, (live or self.live), record)  # pyright: ignore[reportArgumentType]

    def test_a_response_with_no_turn_in_flight_is_not_a_turn_end(self) -> None:
        self.conv.turn_active = False
        self.feed({"type": "response", "command": "prompt", "success": False, "error": "late"})
        self.assertEqual([], self.seen.ended)
        self.assertEqual([], self.seen.errors)
        self.assertEqual(0, self.conv.state.turns)

    def test_a_refused_prompt_in_a_turn_ends_it(self) -> None:
        self.conv.turn_active = True
        self.feed({"type": "response", "command": "prompt", "success": False, "error": "no"})
        self.assertEqual(["no"], self.seen.errors)
        self.assertEqual(1, len(self.seen.ended))

    def test_the_stats_are_asked_for_only_once_pi_has_settled(self) -> None:
        """agent_end can be followed by a retry or a compaction; only agent_settled ends the turn."""
        self.conv.turn_active = True
        for record in ({"type": "agent_start"}, {"type": "message_end", "message": {"role": "assistant"}}, {"type": "agent_end"}):
            self.feed(record)
        self.assertEqual([], self.live.written)
        self.feed({"type": "agent_settled"})
        self.assertEqual(1, len(self.live.written))

    def test_settling_asks_for_the_stats_once(self) -> None:
        self.conv.turn_active = True
        self.feed({"type": "agent_settled"})
        self.feed({"type": "agent_settled"})
        self.assertEqual(1, len(self.live.written))

    def test_a_new_process_starts_with_a_clean_slate(self) -> None:
        self.conv.turn_active = True
        self.feed({"type": "agent_settled"})
        second = FakeLive(generation=2)
        self.feed({"type": "agent_settled"}, second)
        self.assertEqual(1, len(second.written), "the first process's pending stats request is not the second's")

    def test_each_message_is_its_own_block_and_its_deltas_share_one(self) -> None:
        for text in ("Hel", "lo"):
            self.feed({"type": "message_start"})
            self.feed({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "delta": text}})
            self.feed({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "delta": "!"}})
            self.feed({"type": "message_end", "message": {"role": "assistant", "content": []}})
        first, second = self.seen.blocks[0:2], self.seen.blocks[2:4]
        self.assertEqual(1, len(set(first)))
        self.assertEqual(1, len(set(second)))
        self.assertNotEqual(first[0], second[0])
        self.assertNotIn("", self.seen.blocks)

    def test_a_tool_call_is_its_name_and_a_files_base_name_and_never_its_arguments(self) -> None:
        secret = "sk-live-0123456789abcdef"
        self.feed({"type": "tool_execution_start", "toolCallId": "t1", "toolName": "bash", "args": {"command": f"echo {secret}"}})
        self.feed({"type": "tool_execution_end", "toolCallId": "t1", "toolName": "bash", "isError": True, "result": secret})
        self.feed({"type": "tool_execution_start", "toolCallId": "t2", "toolName": "read", "args": {"path": "/x/y/main.py"}})
        self.feed({"type": "tool_execution_end", "toolCallId": "t2", "toolName": "read", "isError": False})
        self.assertEqual([
            ("t1", "bash", "", "running"), ("t1", "bash", "", "failed"),
            ("t2", "read", "main.py", "running"), ("t2", "read", "main.py", "done"),
        ], self.seen.activities)
        self.assertNotIn(secret, repr(self.seen.activities))
