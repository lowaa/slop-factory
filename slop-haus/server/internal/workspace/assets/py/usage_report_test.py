import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from contextlib import redirect_stderr, redirect_stdout

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "usage-report.py")
# go-llm-usage's shared bash fixture, which every client's extractor runs.
BASH_FIXTURE = os.path.join(HERE, *[".."] * 6, "go-llm-usage", "fixtures", "bash_commands.json")
# ... and the one for the label fields the server refuses a whole report over.
LABELS_FIXTURE = os.path.join(HERE, *[".."] * 6, "go-llm-usage", "fixtures", "record_labels.json")

# No cached bytecode: a .pyc beside the script is trusted on its source's
# mtime and size, so an edit undone within the same second -- a negative
# test run -- would go on testing the edit.
sys.dont_write_bytecode = True
_spec = importlib.util.spec_from_file_location("usage_report", SCRIPT)
usage_report = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(usage_report)


def assistant(message_id, model, at, content, **usage):
    return {
        "type": "assistant",
        "timestamp": at,
        "message": {"id": message_id, "model": model, "role": "assistant", "content": content, "usage": usage},
    }


def tool_use(tool_id, name, **tool_input):
    return {"type": "tool_use", "id": tool_id, "name": name, "input": tool_input}


def tool_results(at, *results):
    return {"type": "user", "timestamp": at, "message": {"role": "user", "content": list(results)}}


def tool_result(tool_id, content, is_error=None):
    block = {"type": "tool_result", "tool_use_id": tool_id, "content": content}
    if is_error is not None:
        block["is_error"] = is_error
    return block


def usage(input_tokens, output_tokens, cache_read=0, cache_write=0, thinking=0):
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "cache_read_input_tokens": cache_read,
        "cache_creation_input_tokens": cache_write,
        "output_tokens_details": {"thinking_tokens": thinking},
    }


class TranscriptCase(unittest.TestCase):
    def write(self, *entries, raw_tail=""):
        fd, path = tempfile.mkstemp(suffix=".jsonl")
        self.addCleanup(os.remove, path)
        with os.fdopen(fd, "w") as f:
            for entry in entries:
                f.write((entry if isinstance(entry, str) else json.dumps(entry)) + "\n")
            f.write(raw_tail)
        return path

    def report(self, path, event="SubagentStop", **limits):
        payload = {
            "hook_event_name": event,
            "session_id": "sess-1",
            "transcript_path": "/nonexistent/main.jsonl",
            "agent_id": "a1b2",
            "agent_type": "reviewer",
            "agent_transcript_path": path,
        }
        if event == "Stop":
            payload["transcript_path"] = path
        report, reason = usage_report.build_report(payload, **limits)
        self.assertIsNotNone(report, reason)
        return report

    def record(self, report, model):
        matches = [r for r in report["records"] if r["model"] == model]
        self.assertEqual(len(matches), 1, [r["model"] for r in report["records"]])
        return matches[0]

    def tools(self, record):
        return {(t["tool"], t.get("command", "")): t for t in record["tools"]}


class DedupeTest(TranscriptCase):
    # Claude Code writes one line per content block, each repeating the
    # message's usage -- and while it streams, the later lines carry more
    # output. Summed per line, this transcript's two requests would count
    # as five.
    def test_a_message_split_over_lines_counts_once_with_its_final_usage(self):
        path = self.write(
            assistant("msg_1", "claude-opus-5-5", "2026-09-30T10:00:00.000Z",
                      [{"type": "thinking", "thinking": ""}], **usage(3, 10, 1000, 200, thinking=8)),
            assistant("msg_1", "claude-opus-5-5", "2026-09-30T10:00:01.000Z",
                      [{"type": "text", "text": "Looking."}], **usage(3, 40, 1000, 200, thinking=8)),
            assistant("msg_1", "claude-opus-5-5", "2026-09-30T10:00:02.000Z",
                      [tool_use("toolu_1", "Read", file_path="/x")], **usage(3, 90, 1000, 200, thinking=8)),
            tool_results("2026-09-30T10:00:03.000Z", tool_result("toolu_1", "file body")),
            assistant("msg_2", "claude-opus-5-5", "2026-09-30T10:00:04.000Z",
                      [{"type": "text", "text": "Done"}], **usage(5, 7, 1200, 0)),
            assistant("msg_2", "claude-opus-5-5", "2026-09-30T10:00:05.000Z",
                      [{"type": "text", "text": "."}], **usage(5, 7, 1200, 0)),
        )
        record = self.record(self.report(path), "claude-opus-5-5")
        self.assertEqual(record["requests"], 2)
        self.assertEqual(record["input_tokens"], 3 + 5)
        self.assertEqual(record["output_tokens"], 90 + 7)
        self.assertEqual(record["cache_read_tokens"], 1000 + 1200)
        self.assertEqual(record["cache_write_tokens"], 200)
        self.assertEqual(record["reasoning_tokens"], 8)
        self.assertEqual(self.tools(record)[("Read", "")]["calls"], 1)

    # Totals rather than increments: the same transcript reported twice is
    # the same report, which the server's upsert then makes a no-op.
    def test_reading_a_transcript_twice_gives_the_same_report(self):
        path = self.write(
            assistant("msg_1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)),
        )
        self.assertEqual(self.report(path), self.report(path))


class ModelsTest(TranscriptCase):
    def test_each_model_gets_its_own_record_with_its_own_tools_and_window(self):
        path = self.write(
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z",
                      [tool_use("t1", "Grep", pattern="x")], **usage(1, 10)),
            tool_results("2026-09-30T10:00:02Z", tool_result("t1", "hit")),
            assistant("m2", "claude-sonnet-5", "2026-09-30T10:05:00Z",
                      [tool_use("t2", "Glob", pattern="*")], **usage(2, 20)),
            tool_results("2026-09-30T10:05:01Z", tool_result("t2", "a.go")),
            assistant("m3", "claude-opus-5-5", "2026-09-30T10:10:00Z", [], **usage(3, 30)),
            # Claude Code's own notices: no request was made.
            assistant("m4", "<synthetic>", "2026-09-30T10:11:00Z", [], **usage(0, 0)),
        )
        report = self.report(path)
        self.assertEqual(sorted(r["model"] for r in report["records"]), ["claude-opus-5-5", "claude-sonnet-5"])

        opus = self.record(report, "claude-opus-5-5")
        self.assertEqual((opus["requests"], opus["input_tokens"], opus["output_tokens"]), (2, 4, 40))
        self.assertEqual(set(self.tools(opus)), {("Grep", "")})
        self.assertEqual((opus["started_at"], opus["ended_at"]), ("2026-09-30T10:00:00.000Z", "2026-09-30T10:10:00.000Z"))

        sonnet = self.record(report, "claude-sonnet-5")
        self.assertEqual((sonnet["requests"], sonnet["input_tokens"], sonnet["output_tokens"]), (1, 2, 20))
        self.assertEqual(set(self.tools(sonnet)), {("Glob", "")})
        # A tool's result extends its model's window.
        self.assertEqual((sonnet["started_at"], sonnet["ended_at"]), ("2026-09-30T10:05:00.000Z", "2026-09-30T10:05:01.000Z"))

        for record in report["records"]:
            self.assertEqual(
                {k: record[k] for k in ("schema_version", "provider", "harness", "billing_mode")},
                {"schema_version": 1, "provider": "anthropic", "harness": "claude", "billing_mode": "subscription"},
            )


class ToolsTest(TranscriptCase):
    def test_a_tool_error_is_paired_with_its_call_and_timed(self):
        path = self.write(
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00.000Z",
                      [tool_use("t1", "Edit", file_path="/x"), tool_use("t2", "Edit", file_path="/y")], **usage(1, 1)),
            tool_results("2026-09-30T10:00:01.500Z",
                         tool_result("t1", "String to replace not found", is_error=True),
                         tool_result("t2", [{"type": "text", "text": "édité"}], is_error=False)),
        )
        edit = self.tools(self.record(self.report(path), "claude-opus-5-5"))[("Edit", "")]
        self.assertEqual(edit["calls"], 2)
        self.assertEqual(edit["errors"], 1)
        self.assertEqual(edit["duration_ms"], 3000)
        self.assertEqual(edit["result_bytes"], len("String to replace not found") + len("édité".encode()))

    # Only program names leave the machine: the command line may hold a
    # secret, and nothing of it but what extract_commands makes of it may
    # appear anywhere in the report.
    def test_bash_is_reported_by_program_and_its_command_line_never_is(self):
        secret = "hunter2-supersecret"
        line = "curl -H 'Authorization: Bearer %s' https://x && git -C /repo status | tail -5" % secret
        path = self.write(
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z",
                      [tool_use("t1", "Bash", command=line, description="check " + secret)], **usage(1, 1)),
            tool_results("2026-09-30T10:00:02Z", tool_result("t1", "Exit code 1\n" + secret, is_error=True)),
        )
        report = self.report(path)
        self.assertNotIn(secret, json.dumps(report))
        tools = self.tools(self.record(report, "claude-opus-5-5"))
        self.assertEqual(set(tools), {("Bash", "curl"), ("Bash", "git status"), ("Bash", "tail")})
        # One call: each program counts it, and the last carries its outcome.
        self.assertEqual([tools[k]["calls"] for k in sorted(tools)], [1, 1, 1])
        self.assertEqual(tools[("Bash", "tail")]["errors"], 1)
        self.assertEqual(tools[("Bash", "tail")]["duration_ms"], 2000)
        self.assertEqual(tools[("Bash", "curl")]["errors"], 0)

    def test_a_call_with_no_result_yet_still_counts(self):
        path = self.write(
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [tool_use("t1", "Agent", prompt="p")], **usage(1, 1)),
        )
        agent = self.tools(self.record(self.report(path), "claude-opus-5-5"))[("Agent", "")]
        self.assertEqual((agent["calls"], agent["errors"], agent["duration_ms"]), (1, 0, 0))


class MalformedTest(TranscriptCase):
    def test_malformed_and_truncated_lines_are_skipped(self):
        path = self.write(
            "not json at all",
            "[1, 2]",
            '{"type": "assistant", "message": "not an object"}',
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", "not a list", **usage(1, 2)),
            assistant("m2", "claude-opus-5-5", "not a time", [], **usage(1, 2)),
            {"type": "assistant", "timestamp": "2026-09-30T10:00:00Z",
             "message": {"id": "m3", "model": "claude-opus-5-5", "usage": {"input_tokens": "lots", "output_tokens": -4}}},
            # The line being written as the hook read the file.
            raw_tail='{"type": "assistant", "timestamp": "2026-09-30T10:00:09Z", "message": {"id": "m9", "model": "claude-opus-5-5", "usa',
        )
        record = self.record(self.report(path), "claude-opus-5-5")
        self.assertEqual(record["requests"], 3)
        self.assertEqual((record["input_tokens"], record["output_tokens"]), (2, 4))

    def test_an_oversized_line_is_skipped_and_the_rest_read(self):
        path = self.write(
            tool_results("2026-09-30T10:00:00Z", tool_result("t0", "x" * 5000)),
            assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)),
        )
        record = self.record(self.report(path, max_line_bytes=1000), "claude-opus-5-5")
        self.assertEqual(record["requests"], 1)

    def test_a_transcript_over_budget_is_not_reported_at_all(self):
        # Full totals replace the last report: a partial one would be worse
        # than none.
        path = self.write(*[assistant("m%d" % i, "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)) for i in range(50)])
        report, reason = usage_report.build_report(
            {"hook_event_name": "Stop", "session_id": "s", "transcript_path": path}, max_bytes=1000)
        self.assertIsNone(report)
        self.assertIn("over 1000 bytes", reason)

        report, reason = usage_report.build_report(
            {"hook_event_name": "Stop", "session_id": "s", "transcript_path": path}, time_budget=-1)
        self.assertIsNone(report)
        self.assertIn("to read", reason)

    def test_a_transcript_with_no_model_calls_is_not_reported(self):
        # An empty report would wipe what the server has stored.
        path = self.write({"type": "user", "timestamp": "2026-09-30T10:00:00Z", "message": {"content": "hi"}})
        report, reason = usage_report.build_report({"hook_event_name": "Stop", "session_id": "s", "transcript_path": path})
        self.assertIsNone(report)
        self.assertIn("no model calls", reason)


class SettleTest(TranscriptCase):
    # The hook fires before Claude Code has flushed the turn's last message,
    # so the transcript is read only once it stops growing. A fake clock and
    # sleep make that deterministic: the line lands during the first wait.
    def test_it_waits_for_the_transcript_to_stop_growing(self):
        path = self.write(assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)))
        now = [0.0]
        sleeps = []

        def sleep(seconds):
            if not sleeps:
                with open(path, "a") as f:
                    f.write(json.dumps(assistant("m2", "claude-opus-5-5", "2026-09-30T10:00:01Z", [], **usage(1, 2))) + "\n")
            sleeps.append(seconds)
            now[0] += seconds

        usage_report.wait_for_settle(path, settle=1.0, max_wait=5.0, clock=lambda: now[0], sleep=sleep)
        # Grew after the first look, so a full second of quiet from then.
        self.assertEqual(now[0], 1.25)
        self.assertEqual(self.record(self.report(path), "claude-opus-5-5")["requests"], 2)

    def test_it_gives_up_waiting_on_a_transcript_that_keeps_growing(self):
        path = self.write(assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)))
        now = [0.0]

        def sleep(seconds):
            with open(path, "a") as f:
                f.write("\n")
            now[0] += seconds

        usage_report.wait_for_settle(path, settle=1.0, max_wait=5.0, clock=lambda: now[0], sleep=sleep)
        self.assertEqual(now[0], 5.0)

    # The report reads the transcript only after the wait: a line the wait
    # sees land is in it.
    def test_the_report_reads_after_waiting(self):
        path = self.write(assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)))
        waits = []

        def wait(waited_path, settle):
            waits.append((waited_path, settle))
            with open(waited_path, "a") as f:
                f.write(json.dumps(assistant("m2", "claude-opus-5-5", "2026-09-30T10:00:01Z", [], **usage(1, 2))) + "\n")

        original = usage_report.wait_for_settle
        usage_report.wait_for_settle = wait
        self.addCleanup(setattr, usage_report, "wait_for_settle", original)
        report, reason = usage_report.build_report(
            {"hook_event_name": "Stop", "session_id": "s", "transcript_path": path}, settle=0.5)
        self.assertIsNotNone(report, reason)
        self.assertEqual(waits, [(path, 0.5)])
        self.assertEqual(report["records"][0]["requests"], 2)

    def test_main_waits_as_long_as_the_environment_says(self):
        seen = []
        original = usage_report.build_report
        usage_report.build_report = lambda payload, **kwargs: (seen.append(kwargs), (None, "stub"))[1]
        self.addCleanup(setattr, usage_report, "build_report", original)
        original_stdin = sys.stdin
        sys.stdin = io.TextIOWrapper(io.BytesIO(b'{"hook_event_name": "Stop"}'))
        self.addCleanup(setattr, sys, "stdin", original_stdin)
        with mock.patch.dict(os.environ, {"SLOP_HAUS_USAGE_SETTLE_SECONDS": "2.5"}), redirect_stderr(io.StringIO()):
            usage_report.main()
        self.assertEqual(seen, [{"time_budget": usage_report.TIME_BUDGET_SECONDS, "settle": 2.5}])

    def test_the_wait_can_be_set_for_tests_and_is_capped(self):
        self.assertEqual(usage_report.settle_seconds({}), usage_report.SETTLE_SECONDS)
        self.assertEqual(usage_report.settle_seconds({"SLOP_HAUS_USAGE_SETTLE_SECONDS": "0"}), 0.0)
        self.assertEqual(usage_report.settle_seconds({"SLOP_HAUS_USAGE_SETTLE_SECONDS": "999"}), usage_report.MAX_SETTLE_SECONDS)
        self.assertEqual(usage_report.settle_seconds({"SLOP_HAUS_USAGE_SETTLE_SECONDS": "soon"}), usage_report.SETTLE_SECONDS)


class PayloadTest(TranscriptCase):
    def setUp(self):
        self.path = self.write(assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)))

    def test_stop_reports_the_main_transcript(self):
        report = self.report(self.path, event="Stop")
        self.assertEqual((report["transcript_key"], report["scope"], report["session_id"], report["harness"]),
                         ("sess-1", "main", "sess-1", "claude"))

    def test_subagent_stop_reports_the_subagents_own_transcript(self):
        report = self.report(self.path)
        self.assertEqual((report["transcript_key"], report["scope"]), ("sess-1:a1b2", "subagent:reviewer"))

    def test_an_agent_type_is_made_to_fit_the_servers_scope(self):
        report, _ = usage_report.build_report({
            "hook_event_name": "SubagentStop", "session_id": "s", "agent_id": "a1",
            "agent_type": "plugin:my agent/" + "x" * 80, "agent_transcript_path": self.path,
        })
        self.assertRegex(report["scope"], r"^subagent:[A-Za-z0-9._-]{1,64}$")
        self.assertTrue(report["scope"].startswith("subagent:plugin_my_agent_x"))

    def test_payloads_it_cannot_use_report_nothing(self):
        for payload in [
            {"hook_event_name": "SessionStart", "session_id": "s", "transcript_path": self.path},
            {"hook_event_name": "Stop", "transcript_path": self.path},
            {"hook_event_name": "Stop", "session_id": "s s", "transcript_path": self.path},
            {"hook_event_name": "Stop", "session_id": "s"},
            {"hook_event_name": "Stop", "session_id": "s", "transcript_path": "/nonexistent"},
            {"hook_event_name": "SubagentStop", "session_id": "s", "agent_transcript_path": self.path},
            {"hook_event_name": "SubagentStop", "session_id": "s", "agent_id": "a1", "transcript_path": self.path},
        ]:
            report, reason = usage_report.build_report(payload)
            self.assertIsNone(report, payload)
            self.assertTrue(reason, payload)


class MainTest(TranscriptCase):
    def run_script(self, stdin):
        # -I ignores PYTHON* variables only; the settle knob still reaches it.
        env = dict(os.environ, SLOP_HAUS_USAGE_SETTLE_SECONDS="0")
        return subprocess.run([sys.executable, "-I", SCRIPT], input=stdin, capture_output=True, timeout=30, env=env)

    def test_it_prints_the_report(self):
        path = self.write(assistant("m1", "claude-opus-5-5", "2026-09-30T10:00:00Z", [], **usage(1, 2)))
        run = self.run_script(json.dumps({"hook_event_name": "Stop", "session_id": "s", "transcript_path": path}).encode())
        self.assertEqual(run.returncode, 0)
        self.assertEqual(json.loads(run.stdout)["records"][0]["requests"], 1)

    def test_it_never_fails(self):
        for stdin in [b"", b"not json", b"[]", b'{"hook_event_name": "Stop", "session_id": "s", "transcript_path": "/"}']:
            run = self.run_script(stdin)
            self.assertEqual(run.returncode, 0, stdin)
            self.assertEqual(run.stdout, b"", stdin)
            self.assertIn(b"slop-haus: usage:", run.stderr, stdin)

    def test_main_swallows_an_unexpected_error(self):
        original = usage_report.build_report
        usage_report.build_report = lambda *a, **k: 1 / 0
        self.addCleanup(setattr, usage_report, "build_report", original)
        stdin = io.TextIOWrapper(io.BytesIO(b'{"hook_event_name": "Stop"}'))
        out, err = io.StringIO(), io.StringIO()
        original_stdin = sys.stdin
        sys.stdin = stdin
        self.addCleanup(setattr, sys, "stdin", original_stdin)
        with redirect_stdout(out), redirect_stderr(err):
            usage_report.main()
        self.assertEqual(out.getvalue(), "")
        self.assertIn("ZeroDivisionError", err.getvalue())


class ExtractCommandsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(BASH_FIXTURE) as f:
            cls.fixture = json.load(f)

    def test_the_shared_fixture(self):
        for case in self.fixture["cases"]:
            with self.subTest(case["name"]):
                got = usage_report.extract_commands(case["command_line"])
                self.assertEqual(got, case["expected"])
                for command in got:
                    self.assertRegex(command, r"^[a-z0-9._-]{1,32}( [a-z0-9._-]{1,32})?$")

    # The extractor's own copy of the allowlist must be the fixture's (and
    # go-llm-usage's, which its Go test checks against the fixture).
    def test_it_knows_the_fixtures_subcommand_programs(self):
        self.assertEqual(
            {p: v["value_options"] for p, v in self.fixture["subcommand_programs"].items()},
            usage_report.SUBCOMMAND_PROGRAMS,
        )
        for program, spec in self.fixture["subcommand_programs"].items():
            for option in spec["value_options"]:
                self.assertEqual(usage_report.extract_commands("%s %s v sub" % (program, option)), ["%s sub" % program])
        self.assertEqual(usage_report.extract_commands("make -C dir test"), ["make"])

    # What the fixture can't hold (it needs a program in every case), and
    # names that are dict methods or dunder attributes.
    def test_edge_cases(self):
        for name, line, expected in [
            ("empty line", "", []),
            ("dict-ish program", "__class__ x && keys y && toString z", ["__class__", "keys", "other"]),
        ]:
            with self.subTest(name):
                self.assertEqual(usage_report.extract_commands(line), expected)


class RecordLabelsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(LABELS_FIXTURE) as f:
            cls.labels = json.load(f)

    def check(self, label, pattern):
        for value in self.labels[label]["valid"]:
            self.assertIsNotNone(pattern.match(value), "%s %r is valid" % (label, value))
        for value in self.labels[label]["invalid"]:
            self.assertIsNone(pattern.match(value), "%s %r is invalid" % (label, value))

    def test_model_pattern_matches_the_servers(self):
        self.check("model", usage_report.MODEL)

    def test_tool_pattern_matches_the_servers(self):
        self.check("tool", usage_report.TOOL)

    # Not in the fixture: these only name the report (go-llm-usage's transcriptKeyPattern).
    def test_ids_refuse_a_trailing_newline(self):
        for pattern in (usage_report.SESSION_ID, usage_report.AGENT_ID):
            self.assertIsNotNone(pattern.match("abc-1"))
            self.assertIsNone(pattern.match("abc-1\n"))

    def test_the_provider_it_sends_is_one_the_server_accepts(self):
        self.assertIn(usage_report.PROVIDER, self.labels["provider"]["valid"] + ["anthropic"])
        self.assertNotIn(usage_report.PROVIDER, self.labels["provider"]["invalid"])


if __name__ == "__main__":
    unittest.main()
