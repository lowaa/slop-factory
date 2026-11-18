"""Fixtures for the host's tests: a fake harness that speaks Claude Code's stream-json over stdin and
stdout (so the host's subprocess handling, flags and environment run for real), a real git working
copy with a real origin, and a fake reporter.

Nothing here waits for a time to pass: the idle timeout is a fake clock the test moves, and the
host's inbox is read until what the test wants has happened (FAILSAFE only keeps a hang from
hanging the suite, and is never what a test measures).
"""

from __future__ import annotations

import json
import os
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from collections.abc import Callable
from pathlib import Path
from typing import Any
from unittest import mock

from fakes import FakeClock
from slop_haus_supervisor.remote_control.host.claude import RemoteControlClaude
from slop_haus_supervisor.remote_control.host.conversation import WorkItem
from slop_haus_supervisor.remote_control.host.host import Host, SessionHarness
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from slop_haus_supervisor.remote_control.host.prompt import SYSTEM_PROMPT
from slop_haus_supervisor.remote_control.host.settings import Settings

CONV = "11111111-2222-4333-8444-555555555555"
OTHER_CONV = "99999999-2222-4333-8444-555555555555"
FAILSAFE = 30
SERVER = "https://slop-haus.example"

FAKE_HARNESS = r'''
import json, os, signal, sys

log_dir = os.environ["FAKE_HARNESS_DIR"]
with open(os.path.join(log_dir, "invocations.jsonl"), "a") as f:
    f.write(json.dumps({"argv": sys.argv[1:], "cwd": os.getcwd(),
                        "env": {k: v for k, v in os.environ.items() if k.startswith(("SLOP_HAUS", "CLAUDE_", "KEEP"))}}) + "\n")
mode = os.environ.get("FAKE_HARNESS_MODE", "")
if mode == "session-in-use" and "--session-id" in sys.argv:
    sys.stderr.write("Error: Session ID x is already in use.\n")
    sys.exit(1)
if mode == "no-conversation" and "--resume" in sys.argv:
    # As Claude Code does (2.1.291): a result on stdout, before any init, then
    # the refusal on stderr.
    print(json.dumps({"type": "result", "subtype": "error_during_execution", "is_error": True, "num_turns": 0,
                      "usage": {"input_tokens": 0, "output_tokens": 0}}), flush=True)
    sys.stderr.write("No conversation found with session ID: x\n")
    sys.exit(1)

if mode == "close-stdin":
    # Alive but unable to take a message; the init record, printed after the
    # close, tells the test the host's next write will fail.
    os.close(0)
    print(json.dumps({"type": "system", "subtype": "init", "apiKeySource": "none"}), flush=True)
    signal.pause()

results = 0
# Claude Code's modelUsage is the session's cumulative, so a process that resumes the session
# starts from what the ones before it spent.
session_file = os.path.join(log_dir, "session_usage.json")
try:
    with open(session_file) as f:
        usage = json.load(f)
except OSError:
    usage = {"in": 0, "out": 0, "results": 0}
for line in sys.stdin:
    message = json.loads(line)
    text = message["message"]["content"][0]["text"]
    with open(os.path.join(log_dir, "received.jsonl"), "a") as f:
        f.write(json.dumps({"text": text, "results_before": results}) + "\n")
    if text.startswith("crash"):
        sys.stderr.write("boom: https://user:secret@example.com/x\n")
        sys.exit(3)
    if text.startswith("hang:"):
        # Does its work, then never answers: the turn is cut short by whoever stops it.
        import subprocess
        print(json.dumps({"type": "system", "subtype": "init", "apiKeySource": "none"}), flush=True)
        subprocess.run(text[5:], shell=True, cwd=os.getcwd(), check=True, capture_output=True)
        open(os.path.join(log_dir, "hanging"), "w").close()
        signal.pause()
    if text.startswith("run:"):
        # The model working in the checkout: the rest of the message is a shell command.
        import subprocess
        subprocess.run(text[4:], shell=True, cwd=os.getcwd(), check=True, capture_output=True)
    reply = "echo: " + text
    out = lambda record: print(json.dumps(record), flush=True)
    out({"type": "system", "subtype": "init", "apiKeySource": os.environ.get("FAKE_KEY_SOURCE", "none")})
    if text == "/compact" and os.environ.get("FAKE_COMPACT_FAIL"):
        out({"type": "system", "subtype": "status", "status": None, "compact_result": "failed", "compact_error": "Not enough messages to compact."})
    mid = "msg_%d" % (results + 1)
    out({"type": "stream_event", "event": {"type": "message_start", "message": {"id": mid, "model": "claude-test-1"}}})
    if text.startswith("tool"):
        out({"type": "assistant", "message": {"id": mid, "model": "claude-test-1", "content": [
            {"type": "tool_use", "id": "tu_1", "name": "Read", "input": {}}]}})
        out({"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": "tu_1", "is_error": True, "content": "denied"}]}})
    if os.environ.get("FAKE_HARNESS_NO_PARTIALS"):
        out({"type": "assistant", "message": {"id": "other_%d" % results, "model": "claude-test-1", "content": [{"type": "text", "text": reply}]}})
    else:
        mid2 = mid
        for part in (reply[:3], reply[3:]):
            out({"type": "stream_event", "event": {"type": "content_block_delta", "delta": {"type": "text_delta", "text": part}}})
        out({"type": "assistant", "message": {"id": mid2, "model": "claude-test-1", "content": [{"type": "text", "text": reply}],
                                              "usage": {"input_tokens": 3, "cache_read_input_tokens": 40 + results, "cache_creation_input_tokens": 7, "output_tokens": 2}}})
    results += 1
    usage["in"] += 100
    usage["out"] += 7
    usage["results"] += 1
    with open(session_file, "w") as f:
        json.dump(usage, f)
    is_error = text.startswith("error")
    if text.startswith("nousage"):
        # A turn that spent nothing, as one the API refused outright.
        out({"type": "result", "subtype": "success", "is_error": False, "result": reply, "num_turns": 0})
        continue
    out({"type": "result", "subtype": "error_during_execution" if is_error else "success", "is_error": is_error,
         "result": "API Error: 529 overloaded" if is_error else reply, "num_turns": 1,
         "usage": {"input_tokens": 100, "output_tokens": 7},
         "modelUsage": {"claude-test-1": {"inputTokens": usage["in"], "outputTokens": usage["out"], "cacheReadInputTokens": 10 * usage["results"],
                                          "cacheCreationInputTokens": 0, "thinkingTokens": 0, "costUSD": 0.001 * usage["results"]}}})
'''


def git(cwd: str, *args: str) -> str:
    out = subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True)
    if out.returncode != 0:
        raise AssertionError(f"git {' '.join(args)}: {out.stderr}")
    return out.stdout.strip()


def process_running(pid: int) -> bool:
    """Whether the process is there and not a zombie waiting to be reaped."""
    try:
        with open(f"/proc/{pid}/stat", encoding="utf-8") as f:
            return f.read().rsplit(")", 1)[1].split()[0] != "Z"
    except OSError:
        return False


def process_gone(pid: int) -> bool:
    """Whether the process is gone within FAILSAFE. A SIGKILL returns before the kernel has finished
    the target, so a check straight after a stop can still see it."""
    settled = threading.Event()
    for _ in range(FAILSAFE * 200):
        if not process_running(pid):
            return True
        settled.wait(0.005)
    return False


def ended(live: LiveHarness) -> bool:
    """Whether the harness process has exited, without blocking on one that has not."""
    done = threading.Event()

    def wait() -> None:
        live.session.exit_code()
        done.set()

    threading.Thread(target=wait, daemon=True).start()
    return done.wait(FAILSAFE)


def read_file(path: str) -> str:
    with open(path, encoding="utf-8") as f:
        return f.read()


def wait_for_file(path: str) -> None:
    ready = threading.Event()
    for _ in range(FAILSAFE * 200):
        if os.path.exists(path):
            return
        ready.wait(0.005)
    raise AssertionError(f"{path} never appeared")


class FakeReporter:
    """Records what the host reports. It decides nothing."""

    def __init__(self) -> None:
        self.events: list[dict[str, Any]] = []
        self.usage: list[dict[str, Any]] = []
        self.waited: list[tuple[str, int]] = []
        self.confirms = True
        self.counter = 0

    def record(self, kind: str, **fields: Any) -> int:
        self.counter += 1
        self.events.append(dict(kind=kind, **fields))
        return self.counter

    def text(self, conversation: str, text: str, block: str = "") -> int:
        return self.record("assistant", conversation=conversation, text=text, block=block)

    def activity(self, conversation: str, call_id: str, tool: str, line: str, status: str) -> int:
        return self.record("activity", conversation=conversation, call_id=call_id, tool=tool, line=line, status=status)

    def turn_end(self, conversation: str, turn: int) -> int:
        return self.record("turn_end", conversation=conversation, turn=turn)

    def ready(self, conversation: str, work_seq: int) -> int:
        return self.record("ready", conversation=conversation, work_seq=work_seq)

    def failed(self, conversation: str, reason: str, work_seq: int) -> int:
        return self.record("failed", conversation=conversation, reason=reason, work_seq=work_seq)

    def unsupported(self, conversation: str, work_kind: str) -> int:
        return self.record("unsupported", conversation=conversation, work_kind=work_kind)

    def context(self, conversation: str, tokens: int) -> int:
        return self.record("context", conversation=conversation, tokens=tokens)

    def context_cleared(self, conversation: str) -> int:
        return self.record("context_cleared", conversation=conversation)

    def context_compacted(self, conversation: str) -> int:
        return self.record("context_compacted", conversation=conversation)

    def report_usage(self, conversation: str, body: dict[str, Any]) -> None:
        self.usage.append(body)

    def wait_delivered(self, conversation: str, token: int, timeout: float) -> bool:
        self.waited.append((conversation, token))
        return self.confirms

    def repo_state(self, conversation: str, repo: dict[str, Any], work_seq: int, turn: int | None = None) -> int:
        return self.record("repo_state", conversation=conversation, repo=repo, work_seq=work_seq, turn=turn)

    def of(self, kind: str) -> list[dict[str, Any]]:
        return [e for e in self.events if e["kind"] == kind]

    def said(self) -> str:
        return "".join(e["text"] for e in self.of("assistant"))


def item(seq: int, kind: str, conversation: str = CONV, **fields: Any) -> WorkItem:
    raw = dict(fields, seq=seq, type=kind, conversation=conversation)
    return WorkItem(seq=seq, type=kind, conversation=conversation, raw=raw)


def start_item(seq: int, conversation: str = CONV, **fields: Any) -> WorkItem:
    return item(seq, "start", conversation, **fields)


def flag_value(argv: list[str], flag: str) -> str:
    return argv[argv.index(flag) + 1]


def make_harness(root: str, script: str, token_script: str = "/w/.slop-haus/token.sh",
                 command: list[str] | None = None) -> RemoteControlClaude:
    return RemoteControlClaude(
        Path(root, ".slop-haus", "conductor"), SERVER, SYSTEM_PROMPT, command=command or [sys.executable, script],
        token_script=lambda _spec: Path(token_script), grace=5.0,
    )


class HostCase(unittest.TestCase):
    """A host in a real working copy, with a fake harness and a fake reporter."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="remote-control-host-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.origin = os.path.join(self.tmp, "origin")
        self.root = os.path.join(self.tmp, "workspace")
        self.logs = os.path.join(self.tmp, "logs")
        os.makedirs(self.logs)
        os.makedirs(self.origin)
        git(self.origin, "init", "-q", "-b", "main")
        self.first = self.commit_in_origin("a.txt", "one")
        subprocess.run(["git", "clone", "-q", self.origin, self.root], check=True)
        os.makedirs(os.path.join(self.root, ".slop-haus"))
        with open(os.path.join(self.root, ".git", "info", "exclude"), "a") as f:
            f.write(".slop-haus/\n")
        # So the model can push to main, which origin has checked out.
        git(self.origin, "config", "receive.denyCurrentBranch", "updateInstead")
        self.script = os.path.join(self.tmp, "fake_harness.py")
        with open(self.script, "w") as f:
            f.write(FAKE_HARNESS)
        patcher = mock.patch.dict(os.environ, {
            "FAKE_HARNESS_DIR": self.logs, "KEEP_ME": "yes", "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
            "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
        })
        patcher.start()
        self.addCleanup(patcher.stop)
        self.reporter = FakeReporter()
        self.clock = FakeClock(1000.0)
        self.host = self.new_host()

    def new_host(self, harness: SessionHarness | None = None) -> Host:
        host = Host(self.root, self.reporter, harness or make_harness(self.root, self.script),
                    Settings(harness_grace=5, idle_timeout=300), clock=self.clock)
        self.addCleanup(host.shutdown)
        return host

    def restart_host(self) -> None:
        """The host process ending and a new one starting: what is on disk is all that carries over."""
        self.host.shutdown()
        self.host = self.new_host()

    def commit_in_origin(self, name: str, body: str, branch: str | None = None) -> str:
        if branch:
            git(self.origin, "checkout", "-q", "-B", branch)
        with open(os.path.join(self.origin, name), "w") as f:
            f.write(body)
        git(self.origin, "add", name)
        git(self.origin, "commit", "-q", "-m", "add " + name)
        return git(self.origin, "rev-parse", "HEAD")

    # -- driving the host --

    def conv(self) -> Any:
        assert self.host.conv is not None, "the host holds no conversation"
        return self.host.conv

    def live(self) -> LiveHarness:
        live = self.conv().live
        assert live is not None, "the conversation has no harness"
        return live

    def handle(self, *items: WorkItem) -> None:
        self.host.on_items(list(items))

    def pump(self, until: Callable[[], bool]) -> None:
        """Runs the host's events until what the test is waiting for has happened."""
        while not until():
            try:
                event = self.host.inbox.get(timeout=FAILSAFE)
            except queue.Empty:
                self.fail(f"the host did not do what the test waited for; it said {self.reporter.events!r}")
            self.host.dispatch(event)

    def turns(self) -> int:
        return len(self.reporter.of("turn_end"))

    def message(self, seq: int, text: str) -> None:
        self.handle(item(seq, "message", text=text, message_seq=seq))

    def converse(self, seq: int, text: str) -> None:
        """A message, and the host run until its turn has ended."""
        before = self.turns()
        self.message(seq, text)
        self.pump(lambda: self.turns() > before)

    def invocations(self, count: int = 0) -> list[dict[str, Any]]:
        """What the fake harness recorded about how it was started. The process writes it a moment
        after the host spawned it, so a test that expects `count` of them waits for that many."""
        path = os.path.join(self.logs, "invocations.jsonl")
        waited = threading.Event()
        for _ in range(FAILSAFE * 200):
            lines: list[dict[str, Any]] = []
            if os.path.exists(path):
                with open(path) as f:
                    lines = [json.loads(line) for line in f if line.strip()]
            if len(lines) >= count:
                return lines
            waited.wait(0.005)
        self.fail("the harness never started")

    def received(self) -> list[dict[str, Any]]:
        path = os.path.join(self.logs, "received.jsonl")
        if not os.path.exists(path):
            return []
        with open(path) as f:
            return [json.loads(line) for line in f]

    def start(self, seq: int = 1, **fields: Any) -> None:
        self.handle(start_item(seq, **fields))
        self.assertEqual([], self.reporter.of("failed"), "start failed")

    def origin_branch(self, name: str, filename: str = "b.txt", body: str = "two") -> str:
        """A commit on a new branch of origin, which origin then leaves, so
        pushes to its other branches are accepted."""
        commit = self.commit_in_origin(filename, body, branch=name)
        git(self.origin, "checkout", "-q", "main")
        return commit

    def branch(self) -> str:
        return git(self.root, "branch", "--show-current")

    def repo(self, index: int = -1) -> dict[str, Any]:
        return self.reporter.of("repo_state")[index]["repo"]

    def write(self, name: str, body: str) -> None:
        with open(os.path.join(self.root, name), "w") as f:
            f.write(body)
