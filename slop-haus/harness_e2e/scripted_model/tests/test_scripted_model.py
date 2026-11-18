from __future__ import annotations

import json
import threading
import time
import unittest
import urllib.error
import urllib.request
from typing import Any

from ..apis.anthropic import AnthropicMessages
from ..apis.openai_chat import OpenAIChat
from ..apis.openai_responses import OpenAIResponses
from ..model import ModelRequest, ScriptedModel, UnexpectedRequest
from ..scenario import ScenarioError, ToolCall, Turn, Usage, scenario_from_dict
from ..server import ScriptedModelServer, make_server

TOOLS = [{"name": "bash"}, {"name": "mcp__slop-haus__ticket_next"}]


def message_body(tools: list[dict[str, Any]] | None = None, stream: bool = False) -> dict[str, Any]:
    body: dict[str, Any] = {
        "model": "scripted-1",
        "system": [{"type": "text", "text": "the briefing"}],
        "messages": [{"role": "user", "content": "go"}],
        "stream": stream,
    }
    if tools is not None:
        body["tools"] = tools
    return body


def parse_sse(raw: bytes) -> list[tuple[str, dict[str, Any]]]:
    events = []
    for block in raw.decode().strip().split("\n\n"):
        lines = block.split("\n")
        events.append((lines[0].removeprefix("event: "), json.loads(lines[1].removeprefix("data: "))))
    return events


class ScenarioTest(unittest.TestCase):
    def test_parses_turns(self) -> None:
        scenario = scenario_from_dict(
            {
                "name": "s",
                "turns": [
                    {"tool_calls": [{"name": "bash", "input": {"command": "ls"}}], "usage": {"input_tokens": 5}},
                    {"text": "done"},
                ],
            }
        )
        self.assertEqual(scenario.turns[0].tool_calls, (ToolCall("bash", {"command": "ls"}),))
        self.assertEqual(scenario.turns[0].usage, Usage(input_tokens=5))
        self.assertEqual(scenario.turns[1].text, "done")

    def test_rejects_an_empty_turn(self) -> None:
        with self.assertRaisesRegex(ScenarioError, "needs text or at least one tool call"):
            scenario_from_dict({"turns": [{}]})

    def test_rejects_unknown_fields(self) -> None:
        with self.assertRaisesRegex(ScenarioError, "unknown turn field"):
            scenario_from_dict({"turns": [{"text": "x", "tools": []}]})


class AnthropicEncodingTest(unittest.TestCase):
    adapter = AnthropicMessages()

    def encode(self, turn: Turn, stream: bool) -> bytes:
        request = self.adapter.parse(message_body(TOOLS, stream))
        return b"".join(self.adapter.encode(turn, request, "msg_1"))

    def test_parse_extracts_what_assertions_need(self) -> None:
        request = self.adapter.parse(message_body(TOOLS))
        self.assertEqual(request.model, "scripted-1")
        self.assertEqual(request.system, "the briefing")
        self.assertEqual(request.tool_names, ("bash", "mcp__slop-haus__ticket_next"))
        self.assertFalse(request.auxiliary)

    def test_parse_flattens_messages_and_tool_results(self) -> None:
        body = message_body(TOOLS)
        body["messages"] = [
            {"role": "user", "content": "go"},
            {"role": "assistant", "content": [{"type": "tool_use", "id": "t", "name": "bash", "input": {}}]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t", "content": [{"type": "text", "text": "ticket #7"}]}]},
        ]
        request = self.adapter.parse(body)
        self.assertEqual(request.tool_results, ("ticket #7",))
        self.assertEqual(request.message_text, "go\nticket #7")

    def test_a_request_with_no_tools_is_the_harnesses_own(self) -> None:
        self.assertTrue(self.adapter.parse(message_body()).auxiliary)

    def test_json_response_carries_text_tool_calls_and_exact_usage(self) -> None:
        turn = Turn(
            text="working",
            tool_calls=(ToolCall("bash", {"command": "git status"}),),
            usage=Usage(input_tokens=123, output_tokens=45, cache_read_input_tokens=6, cache_creation_input_tokens=7),
        )
        body = json.loads(self.encode(turn, stream=False))
        self.assertEqual(body["stop_reason"], "tool_use")
        self.assertEqual(
            body["content"],
            [
                {"type": "text", "text": "working"},
                {"type": "tool_use", "id": "msg_1_0", "name": "bash", "input": {"command": "git status"}},
            ],
        )
        self.assertEqual(body["usage"]["input_tokens"], 123)
        self.assertEqual(body["usage"]["output_tokens"], 45)
        self.assertEqual(body["usage"]["cache_read_input_tokens"], 6)
        self.assertEqual(body["usage"]["cache_creation_input_tokens"], 7)

    def test_text_only_turn_ends_the_turn(self) -> None:
        body = json.loads(self.encode(Turn(text="done"), stream=False))
        self.assertEqual(body["stop_reason"], "end_turn")

    def test_sse_stream_is_a_complete_message(self) -> None:
        turn = Turn(
            text="hi",
            tool_calls=(ToolCall("bash", {"command": "ls"}),),
            usage=Usage(input_tokens=9, output_tokens=3),
        )
        events = parse_sse(self.encode(turn, stream=True))
        self.assertEqual(
            [name for name, _ in events],
            [
                "message_start",
                "content_block_start",
                "content_block_delta",
                "content_block_stop",
                "content_block_start",
                "content_block_delta",
                "content_block_stop",
                "message_delta",
                "message_stop",
            ],
        )
        self.assertEqual(events[0][1]["message"]["usage"]["input_tokens"], 9)
        self.assertEqual(events[2][1]["delta"], {"type": "text_delta", "text": "hi"})
        self.assertEqual(events[4][1]["content_block"]["name"], "bash")
        self.assertEqual(events[4][1]["content_block"]["id"], "msg_1_0")
        self.assertEqual(events[4][1]["content_block"]["type"], "tool_use")
        self.assertEqual(json.loads(events[5][1]["delta"]["partial_json"]), {"command": "ls"})
        self.assertEqual(events[7][1]["delta"]["stop_reason"], "tool_use")
        self.assertEqual(events[7][1]["usage"]["output_tokens"], 3)


OPENAI_TOOLS = [
    {"type": "function", "function": {"name": "bash", "parameters": {}}},
    {"type": "function", "function": {"name": "ticket_next", "parameters": {}}},
]


def chat_body(tools: list[dict[str, Any]] | None = None, stream: bool = False) -> dict[str, Any]:
    body: dict[str, Any] = {
        "model": "scripted-1",
        "messages": [{"role": "system", "content": "the briefing"}, {"role": "user", "content": "go"}],
        "stream": stream,
    }
    if tools is not None:
        body["tools"] = tools
    return body


def parse_data_lines(raw: bytes) -> list[Any]:
    out = []
    for block in raw.decode().strip().split("\n\n"):
        assert block.startswith("data: "), block
        payload = block.removeprefix("data: ")
        out.append(payload if payload == "[DONE]" else json.loads(payload))
    return out


class OpenAIEncodingTest(unittest.TestCase):
    adapter = OpenAIChat()

    def encode(self, turn: Turn, stream: bool) -> bytes:
        request = self.adapter.parse(chat_body(OPENAI_TOOLS, stream))
        return b"".join(self.adapter.encode(turn, request, "msg_1"))

    def test_parse_extracts_what_assertions_need(self) -> None:
        request = self.adapter.parse(chat_body(OPENAI_TOOLS))
        self.assertEqual(request.model, "scripted-1")
        self.assertEqual(request.system, "the briefing")
        self.assertEqual(request.message_text, "go")
        self.assertEqual(request.tool_names, ("bash", "ticket_next"))
        self.assertFalse(request.auxiliary)

    def test_parse_flattens_tool_results_and_part_arrays(self) -> None:
        body = chat_body(OPENAI_TOOLS)
        body["messages"] += [
            {"role": "assistant", "content": None, "tool_calls": [{"id": "t", "type": "function", "function": {"name": "bash", "arguments": "{}"}}]},
            {"role": "tool", "tool_call_id": "t", "content": [{"type": "text", "text": "ticket #7"}]},
        ]
        request = self.adapter.parse(body)
        self.assertEqual(request.tool_results, ("ticket #7",))
        self.assertIn("ticket #7", request.message_text)
        self.assertNotIn("the briefing", request.message_text)

    def test_a_request_with_no_tools_is_the_harnesses_own(self) -> None:
        self.assertTrue(self.adapter.parse(chat_body()).auxiliary)

    def test_json_response_carries_text_tool_calls_and_usage(self) -> None:
        turn = Turn(
            text="working",
            tool_calls=(ToolCall("bash", {"command": "git status"}),),
            usage=Usage(input_tokens=123, output_tokens=45, cache_read_input_tokens=6),
        )
        body = json.loads(self.encode(turn, stream=False))
        choice = body["choices"][0]
        self.assertEqual(choice["finish_reason"], "tool_calls")
        self.assertEqual(choice["message"]["content"], "working")
        call = choice["message"]["tool_calls"][0]
        self.assertEqual((call["id"], call["function"]["name"]), ("msg_1_0", "bash"))
        self.assertEqual(json.loads(call["function"]["arguments"]), {"command": "git status"})
        # Cached tokens are inside prompt_tokens, as OpenAI reports them.
        self.assertEqual(body["usage"]["prompt_tokens"], 129)
        self.assertEqual(body["usage"]["prompt_tokens_details"]["cached_tokens"], 6)
        self.assertEqual(body["usage"]["completion_tokens"], 45)

    def test_text_only_turn_stops(self) -> None:
        body = json.loads(self.encode(Turn(text="done"), stream=False))
        self.assertEqual(body["choices"][0]["finish_reason"], "stop")
        self.assertNotIn("tool_calls", body["choices"][0]["message"])

    def test_sse_stream_ends_with_usage_and_done(self) -> None:
        turn = Turn(text="hi", tool_calls=(ToolCall("bash", {"command": "ls"}),), usage=Usage(input_tokens=9, output_tokens=3))
        chunks = parse_data_lines(self.encode(turn, stream=True))
        self.assertEqual(chunks[-1], "[DONE]")
        self.assertEqual(chunks[-2]["choices"], [])
        self.assertEqual(chunks[-2]["usage"]["prompt_tokens"], 9)
        self.assertEqual(chunks[-2]["usage"]["completion_tokens"], 3)
        deltas = [c["choices"][0]["delta"] for c in chunks[:-2]]
        self.assertEqual(deltas[0], {"role": "assistant"})
        self.assertEqual(deltas[1], {"content": "hi"})
        call = deltas[2]["tool_calls"][0]
        self.assertEqual((call["index"], call["function"]["name"]), (0, "bash"))
        self.assertEqual(json.loads(call["function"]["arguments"]), {"command": "ls"})
        self.assertEqual(chunks[-3]["choices"][0]["finish_reason"], "tool_calls")


RESPONSES_TOOLS = [
    {"type": "function", "name": "exec_command", "parameters": {}},
    {"type": "namespace", "name": "mcp__slop_haus", "tools": [{"type": "function", "name": "ticket_get", "parameters": {}}]},
]


def responses_body(tools: list[dict[str, Any]] | None = None, stream: bool = True) -> dict[str, Any]:
    body: dict[str, Any] = {
        "model": "scripted-model-1",
        "instructions": "the briefing",
        "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go"}]}],
        "reasoning": {"effort": "low"},
        "stream": stream,
    }
    if tools is not None:
        body["tools"] = tools
    return body


class ResponsesEncodingTest(unittest.TestCase):
    adapter = OpenAIResponses()

    def encode(self, turn: Turn, stream: bool = True) -> bytes:
        request = self.adapter.parse(responses_body(RESPONSES_TOOLS, stream))
        return b"".join(self.adapter.encode(turn, request, "resp_1"))

    def test_parse_extracts_what_assertions_need(self) -> None:
        request = self.adapter.parse(responses_body(RESPONSES_TOOLS))
        self.assertEqual(request.system, "the briefing")
        self.assertEqual(request.tool_names, ("exec_command", "ticket_get"))
        self.assertEqual(request.message_text, "go")
        self.assertFalse(request.auxiliary)

    def test_parse_reads_tool_results_and_developer_messages(self) -> None:
        body = responses_body(RESPONSES_TOOLS)
        body["input"] += [
            {"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "rules"}]},
            {"type": "function_call", "name": "exec_command", "arguments": '{"cmd":"ls"}', "call_id": "c1"},
            {"type": "function_call_output", "call_id": "c1", "output": "file.txt"},
        ]
        request = self.adapter.parse(body)
        self.assertEqual(request.system, "the briefing\nrules")
        self.assertEqual(request.tool_results, ("file.txt",))
        self.assertIn('{"cmd":"ls"}', request.message_text)

    def test_a_request_with_no_tools_is_the_harnesses_own(self) -> None:
        self.assertTrue(self.adapter.parse(responses_body()).auxiliary)

    def test_stream_is_created_items_then_completed_with_usage(self) -> None:
        turn = Turn(text="hi", tool_calls=(ToolCall("ticket_get", {"ticket_id": 1}),), usage=Usage(100, 10, cache_read_input_tokens=20))
        events = parse_sse(self.encode(turn))
        self.assertEqual([e for e, _ in events], ["response.created", "response.output_item.done", "response.output_item.done", "response.completed"])
        message, call = events[1][1]["item"], events[2][1]["item"]
        self.assertEqual(message["content"][0], {"type": "output_text", "text": "hi", "annotations": []})
        self.assertEqual((call["type"], call["name"], call["namespace"]), ("function_call", "ticket_get", "mcp__slop_haus"))
        self.assertEqual(json.loads(call["arguments"]), {"ticket_id": 1})
        usage = events[3][1]["response"]["usage"]
        self.assertEqual((usage["input_tokens"], usage["output_tokens"], usage["input_tokens_details"]["cached_tokens"]), (120, 10, 20))

    def test_a_top_level_tool_has_no_namespace(self) -> None:
        events = parse_sse(self.encode(Turn(tool_calls=(ToolCall("exec_command", {"cmd": "ls"}),))))
        self.assertNotIn("namespace", events[1][1]["item"])

    def test_non_streamed_response_is_one_object(self) -> None:
        body = json.loads(self.encode(Turn(text="hi", usage=Usage(5, 2)), stream=False))
        self.assertEqual((body["status"], body["output"][0]["type"], body["usage"]["total_tokens"]), ("completed", "message", 7))


class ScriptedModelTest(unittest.TestCase):
    adapter = AnthropicMessages()

    def setUp(self) -> None:
        self.model = ScriptedModel(self.adapter.summarise)
        self.model.load(scenario_from_dict({"name": "s", "turns": [{"text": "one"}, {"text": "two"}]}))

    def request(self, tools: list[dict[str, Any]] | None = TOOLS) -> ModelRequest:
        return self.adapter.parse(message_body(tools))

    def test_plays_turns_in_order_and_records_each_request(self) -> None:
        self.assertEqual(self.model.answer(self.request()).text, "one")  # type: ignore[union-attr]
        self.assertEqual(self.model.answer(self.request()).text, "two")  # type: ignore[union-attr]
        self.assertEqual(len(self.model.recording.requests), 2)
        self.assertEqual(self.model.remaining(), 0)

    def test_running_past_the_script_fails_with_the_request_in_the_message(self) -> None:
        self.model.answer(self.request())
        self.model.answer(self.request())
        with self.assertRaises(UnexpectedRequest) as raised:
            self.model.answer(self.request())
        self.assertIn("only 2 turn(s)", str(raised.exception))
        self.assertIn("last_message=", str(raised.exception))
        self.assertEqual(self.model.recording.errors, [str(raised.exception)])

    def test_a_tool_the_harness_did_not_offer_fails(self) -> None:
        self.model.load(scenario_from_dict({"turns": [{"tool_calls": [{"name": "mcp__slop-haus__ticket_finish"}]}]}))
        with self.assertRaisesRegex(UnexpectedRequest, "did not offer"):
            self.model.answer(self.request())

    def test_no_scenario_fails(self) -> None:
        self.model.reset()
        with self.assertRaisesRegex(UnexpectedRequest, "no scenario is loaded"):
            self.model.answer(self.request())

    def test_auxiliary_requests_are_recorded_but_do_not_consume_the_script(self) -> None:
        self.assertIsNone(self.model.answer(self.request(tools=None)))
        self.assertEqual(self.model.remaining(), 2)
        self.assertEqual(len(self.model.recording.requests), 1)
        self.assertEqual(self.model.recording.conversation(), [])


class ServerTest(unittest.TestCase):
    server: ScriptedModelServer

    @classmethod
    def setUpClass(cls) -> None:
        cls.server = make_server("127.0.0.1", 0)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.server.server_address[1]}"

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.shutdown()
        cls.server.server_close()

    def call(self, method: str, path: str, body: Any = None) -> tuple[int, bytes]:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as err:
            return err.code, err.read()

    def setUp(self) -> None:
        self.call("DELETE", "/_scripted/scenario")

    def test_serves_a_streamed_turn_and_exposes_the_request(self) -> None:
        self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "hello", "usage": {"input_tokens": 4}}]})
        status, raw = self.call("POST", "/v1/messages?beta=true", message_body(TOOLS, stream=True))
        self.assertEqual(status, 200)
        events = parse_sse(raw)
        self.assertEqual(events[0][1]["message"]["usage"]["input_tokens"], 4)
        self.assertEqual(events[2][1]["delta"]["text"], "hello")

        _, recorded = self.call("GET", "/_scripted/requests")
        requests = json.loads(recorded)
        self.assertEqual(requests[0]["system"], "the briefing")
        self.assertEqual(requests[0]["tool_names"], ["bash", "mcp__slop-haus__ticket_next"])

    def test_serves_the_openai_api_from_the_same_scenario(self) -> None:
        self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "hello", "usage": {"input_tokens": 4}}]})
        status, raw = self.call("POST", "/v1/chat/completions", chat_body(OPENAI_TOOLS, stream=True))
        self.assertEqual(status, 200)
        chunks = parse_data_lines(raw)
        self.assertEqual(chunks[1]["choices"][0]["delta"]["content"], "hello")
        self.assertEqual(chunks[-2]["usage"]["prompt_tokens"], 4)
        requests = json.loads(self.call("GET", "/_scripted/requests")[1])
        self.assertEqual((requests[0]["api"], requests[0]["system"]), ("openai", "the briefing"))

    def test_an_unexpected_request_is_an_error_the_harness_and_the_test_both_see(self) -> None:
        self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "only"}]})
        self.call("POST", "/v1/messages", message_body(TOOLS))
        status, raw = self.call("POST", "/v1/messages", message_body(TOOLS))
        self.assertEqual(status, 500)
        self.assertIn("only 1 turn(s)", json.loads(raw)["error"]["message"])

        _, errors = self.call("GET", "/_scripted/errors")
        self.assertEqual(len(json.loads(errors)), 1)

    def test_an_api_it_does_not_speak_is_an_error(self) -> None:
        status, _ = self.call("POST", "/v1/embeddings", {"model": "x"})
        self.assertEqual(status, 404)
        _, errors = self.call("GET", "/_scripted/errors")
        self.assertIn("/v1/embeddings", json.loads(errors)[0])

    def test_a_gated_turn_is_held_until_the_gate_opens(self) -> None:
        self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "late", "gate": "g"}]})
        result: list[tuple[int, bytes]] = []
        caller = threading.Thread(target=lambda: result.append(self.call("POST", "/v1/messages", message_body(TOOLS))))
        caller.start()
        caller.join(timeout=0.3)
        self.assertTrue(caller.is_alive(), "answered before the gate was opened")
        self.call("PUT", "/_scripted/gates/g")
        caller.join(timeout=5)
        self.assertEqual(result[0][0], 200)

    def test_replacing_the_scenario_releases_a_held_request_without_an_error(self) -> None:
        self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "late", "gate": "g"}]})
        caller = threading.Thread(target=lambda: self.call("POST", "/v1/messages", message_body(TOOLS)))
        caller.start()
        # The held request is recorded before it waits: that is the sync point.
        deadline = time.monotonic() + 5
        while not json.loads(self.call("GET", "/_scripted/requests")[1]) and time.monotonic() < deadline:
            time.sleep(0.01)
        self.call("DELETE", "/_scripted/scenario")
        caller.join(timeout=5)
        self.assertFalse(caller.is_alive(), "the held request was not released")
        self.assertEqual(json.loads(self.call("GET", "/_scripted/errors")[1]), [])

    def test_a_gate_nobody_opens_is_an_error(self) -> None:
        self.server.gate_timeout = 0.2
        try:
            self.call("PUT", "/_scripted/scenario", {"turns": [{"text": "late", "gate": "g"}]})
            status, raw = self.call("POST", "/v1/messages", message_body(TOOLS))
        finally:
            self.server.gate_timeout = 60
        self.assertEqual(status, 500)
        self.assertIn("was not opened", json.loads(raw)["error"]["message"])

    def test_a_bad_scenario_is_refused(self) -> None:
        status, raw = self.call("PUT", "/_scripted/scenario", {"turns": [{}]})
        self.assertEqual(status, 400)
        self.assertIn("needs text", json.loads(raw)["error"])


if __name__ == "__main__":
    unittest.main()
