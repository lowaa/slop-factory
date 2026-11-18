#!/usr/bin/env python3
"""The Codex checks that need a real model, run by hand on a host with a ChatGPT login.

    python3 harness_e2e/real_model/check_codex.py [--codex-home DIR] [--report FILE]

Not part of test.sh and not run per change: it spends the subscription's quota (a handful of short turns) and needs
a login, which nothing in CI has. It covers what the scripted model cannot: the shape of a real turn's items, which requests a real model provokes of a headless client,
whether the usage numbers are real and thread-cumulative across a resume, whether a real turn can be interrupted,
and what a real model does when asked to write in a read-only thread.

Every check prints what it saw; a failed check makes the exit status 1. The report is JSON, for the ticket.
The default CODEX_HOME is the login's own (`~/.codex`): a copy of a ChatGPT login would present its refresh token
a second time (see harness/codex_login.py). Its threads are left in that home's sessions."""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from support.codex_app_server import AppServer  # noqa: E402
from support.waiting import wait_for  # noqa: E402

TURN_TIMEOUT = 240.0


class Checks:
    def __init__(self, codex_home: Path, work: Path) -> None:
        self.codex_home, self.work = codex_home, work
        self.report: dict[str, Any] = {}
        self.failures: list[str] = []
        self.servers: list[AppServer] = []

    def server(self) -> AppServer:
        env = {k: v for k, v in os.environ.items() if k in ("PATH", "HOME", "LANG", "LC_ALL")}
        server = AppServer(self.codex_home, self.work, env, self.work / f"app-server-{len(self.servers)}.log")
        self.servers.append(server)
        return server

    def check(self, name: str, ok: bool, saw: Any) -> None:
        self.report[name] = {"ok": ok, "saw": saw}
        print(f"{'ok  ' if ok else 'FAIL'} {name}: {json.dumps(saw)[:400]}")
        if not ok:
            self.failures.append(name)

    def run(self, name: str, check: Callable[[], None]) -> None:
        try:
            check()
        except Exception as err:  # noqa: BLE001 -- a check that cannot run is a failed check, and the rest still run
            self.check(name, False, f"{type(err).__name__}: {err}")

    def turn(self, server: AppServer, thread: str, prompt: str, count: int = 1) -> dict[str, Any]:
        server.start_turn(thread, prompt)
        return wait_for("the turn to complete", lambda: self._done(server, thread, count), timeout=TURN_TIMEOUT)

    @staticmethod
    def _done(server: AppServer, thread: str, count: int) -> dict[str, Any] | None:
        done = [p for p in server.notifications("turn/completed") if p.get("threadId") == thread]
        return done[count - 1] if len(done) >= count else None

    def thread(self, server: AppServer, **params: Any) -> str:
        answer = server.start_thread(self.work, "", params.pop("config", None), params.pop("resume_id", ""))
        assert "error" not in answer, answer
        return answer["result"]["thread"]["id"]

    def login(self) -> None:
        server = self.server()
        answer = server.request("account/read", {"refreshToken": False})
        account = (answer.get("result") or {}).get("account") or {}
        self.check("login is ChatGPT", account.get("type") == "chatgpt", {"type": account.get("type"), "planType": account.get("planType")})

    def shell_turn_and_usage(self) -> str:
        server = self.server()
        thread = self.thread(server)
        done = self.turn(server, thread, "Run the shell command `echo real-model-check` and then reply with exactly the word: done")
        items = [r["params"]["item"] for r in server.records if r.get("method") == "item/completed"]
        commands = [i for i in items if i.get("type") == "commandExecution"]
        self.check("a real turn ran a shell command", done["turn"]["status"] == "completed" and any("real-model-check" in str(c.get("aggregatedOutput")) for c in commands),
                   {"status": done["turn"]["status"], "item_types": sorted({i.get("type") for i in items})})
        # An observation, not a check: what the design leaves open is which requests a real model provokes.
        self.report["requests_a_real_model_provokes_of_a_headless_client"] = sorted(set(server.server_requests))
        print("saw  requests of the client:", sorted(set(server.server_requests)))
        usage = [r["params"]["tokenUsage"] for r in server.records if r.get("method") == "thread/tokenUsage/updated"]
        total = usage[-1]["total"] if usage else {}
        self.check("usage is real and uses the field names the supervisor reads", bool(usage) and total.get("inputTokens", 0) > 0 and total.get("outputTokens", 0) > 0, total)
        self.report["usage_after_first_turn"] = total
        # One writer per thread: the supervisor's next run is a new process.
        server.close()
        return thread

    def resume_usage(self, thread: str) -> None:
        server = self.server()
        self.thread(server, resume_id=thread)
        self.turn(server, thread, "Reply with exactly the word: again")
        records = server.records
        updates = [(n, r["params"]["tokenUsage"]) for n, r in enumerate(records) if r.get("method") == "thread/tokenUsage/updated"]
        started = next(n for n, r in enumerate(records) if r.get("method") == "turn/started")
        replay = [u for n, u in updates if n < started]
        final = updates[-1][1]
        before = replay[-1]["total"]["inputTokens"] if replay else 0
        self.check(
            "a resume replays the thread's usage before the turn, and the run's usage is the final total less that",
            bool(replay) and final["total"]["inputTokens"] - before == final["last"]["inputTokens"] > 0,
            {"replayed": bool(replay), "baseline_input": before, "final_total_input": final["total"]["inputTokens"], "last_input": final["last"]["inputTokens"]},
        )

    def interrupt(self) -> None:
        server = self.server()
        thread = self.thread(server)
        turn = server.start_turn(thread, "Run the shell command `sleep 120` and wait for it to finish.")["result"]["turn"]["id"]
        wait_for("the command to start", lambda: any(r.get("method") == "item/started" and r["params"]["item"].get("type") == "commandExecution" for r in server.records), timeout=TURN_TIMEOUT)
        answer = server.request("turn/interrupt", {"threadId": thread, "turnId": turn})
        done = wait_for("the turn to end", lambda: self._done(server, thread, 1), timeout=60)
        self.check("a real turn is interrupted while a command runs", "error" not in answer and done["turn"]["status"] == "interrupted", {"status": done["turn"]["status"]})

    def read_only_refuses_a_write(self) -> None:
        target = self.work / "written-by-the-model.txt"
        server = self.server()
        answer = server.request("thread/start", {"cwd": str(self.work), "approvalPolicy": "never", "sandbox": "read-only", "config": {}})
        thread = answer["result"]["thread"]["id"]
        self.turn(server, thread, f"You must create the file {target} containing the word hello, now, with the shell or apply_patch. Do not ask first and do not explain: run the tool.")
        items = [r["params"]["item"] for r in server.records if r.get("method") == "item/completed"]
        types = sorted({i.get("type") for i in items})
        # Seen on 0.165.0: the model declines without trying, because its instructions say the sandbox is read-only. So
        # this shows no file was written, and what the model said and did; that the sandbox refuses a write that is
        # tried is test_codex_steward.py's, against the real bubblewrap.
        tried = any(t in ("commandExecution", "fileChange") for t in types)
        said = [i.get("text") for i in items if i.get("type") == "agentMessage"]
        self.report["read_only_thread_reply"] = said
        self.check("a read-only thread leaves no file a real model was asked to write", not target.exists(), {"model_tried_to_write": tried, "file_exists": target.exists(), "item_types": types})

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--codex-home", type=Path, default=Path.home() / ".codex")
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="codex-real-") as tmp:
        checks = Checks(args.codex_home, Path(tmp))
        try:
            checks.run("login", checks.login)
            thread: list[str] = []
            checks.run("a real turn", lambda: thread.append(checks.shell_turn_and_usage()))
            if thread:
                checks.run("a resume", lambda: checks.resume_usage(thread[0]))
            checks.run("an interrupt", checks.interrupt)
            checks.run("a read-only thread", checks.read_only_refuses_a_write)
        finally:
            for server in checks.servers:
                server.close()
    if args.report:
        args.report.write_text(json.dumps(checks.report, indent=2))
    print(f"\n{len(checks.report) - len(checks.failures)} ok, {len(checks.failures)} failed")
    return 1 if checks.failures else 0


if __name__ == "__main__":
    sys.exit(main())
