"""The test's own client of `codex app-server`: JSON-RPC lines over stdio, as the supervisor speaks it, kept to
what a scenario asserts about the protocol. It asserts the CLI's behaviour the supervisor's code is written against,
so it does not use that code."""

from __future__ import annotations

import json
import os
import signal
import subprocess
import threading
from pathlib import Path
from typing import Any, Callable

from .waiting import wait_for

# What a headless client answers a server request with (the supervisor's DECLINES): nobody is there to ask.
DECLINE = {"decision": "decline"}


class AppServer:
    def __init__(self, codex_home: Path, cwd: Path, env: dict[str, str], log_path: Path, args: list[str] | None = None) -> None:
        self.records: list[dict[str, Any]] = []
        # The methods of the requests Codex made of this client, each of which was declined.
        self.server_requests: list[str] = []
        self._cond = threading.Condition()
        # The reader answers server requests while the test is writing its own: one line at a time.
        self._write_lock = threading.Lock()
        self._next_id = 0
        self._log = open(log_path, "wb")
        self._proc = subprocess.Popen(
            ["codex", *(args or []), "app-server"],
            cwd=cwd, env={**env, "CODEX_HOME": str(codex_home)}, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=self._log, text=True, bufsize=1, start_new_session=True,
        )
        threading.Thread(target=self._read, daemon=True).start()
        self.request("initialize", {"clientInfo": {"name": "harness-e2e", "version": "1"}, "capabilities": {}})
        self._write({"method": "initialized"})

    def _write(self, message: dict[str, Any]) -> None:
        assert self._proc.stdin is not None
        with self._write_lock:
            self._proc.stdin.write(json.dumps(message) + "\n")
            self._proc.stdin.flush()

    def _read(self) -> None:
        assert self._proc.stdout is not None
        for line in self._proc.stdout:
            try:
                record = json.loads(line)
            except ValueError:
                continue
            if "method" in record and "id" in record:
                self.server_requests.append(record["method"])
                self._write({"id": record["id"], "result": DECLINE})
            with self._cond:
                self.records.append(record)
                self._cond.notify_all()

    def request(self, method: str, params: dict[str, Any], timeout: float | None = None) -> dict[str, Any]:
        """The response record: `result` or `error`."""
        self._next_id += 1
        rid = self._next_id
        self._write({"id": rid, "method": method, "params": params})
        return self.wait(f"the answer to {method}", lambda r: r.get("id") == rid and "method" not in r, timeout)

    def wait(self, what: str, match: Callable[[dict[str, Any]], bool], timeout: float | None = None) -> dict[str, Any]:
        def probe() -> dict[str, Any] | None:
            with self._cond:
                return next((r for r in self.records if match(r)), None)

        return wait_for(what, probe, timeout, diagnose=lambda: f"records so far: {json.dumps(self.records)[-1500:]}")

    def notifications(self, method: str) -> list[dict[str, Any]]:
        with self._cond:
            return [r["params"] for r in self.records if r.get("method") == method and "id" not in r]

    def start_thread(self, cwd: Path, model: str, config: dict[str, Any] | None = None, resume_id: str = "") -> dict[str, Any]:
        """thread/start (or thread/resume): a worker's own settings, no sandbox and no approvals. The model is a name
        Codex has no metadata for, which is what keeps its tools plain (a model it knows gets a JavaScript `exec` tool
        and its tools in an `additional_tools` input item, which the scripted model does not play)."""
        params: dict[str, Any] = {"cwd": str(cwd), "approvalPolicy": "never", "sandbox": "danger-full-access", "config": config or {}}
        if model:
            params["model"] = model
        if resume_id:
            params["threadId"] = resume_id
        return self.request("thread/resume" if resume_id else "thread/start", params)

    def start_turn(self, thread_id: str, text: str, effort: str = "") -> dict[str, Any]:
        params: dict[str, Any] = {"threadId": thread_id, "input": [{"type": "text", "text": text}]}
        if effort:
            params["effort"] = effort
        return self.request("turn/start", params)

    def wait_turn_completed(self, thread_id: str, count: int = 1) -> dict[str, Any]:
        """The `count`th turn/completed of the thread."""

        def probe() -> dict[str, Any] | None:
            done = [p for p in self.notifications("turn/completed") if p.get("threadId") == thread_id]
            return done[count - 1] if len(done) >= count else None

        return wait_for("the turn to complete", probe, diagnose=lambda: f"records so far: {json.dumps(self.records)[-1500:]}")

    def close(self) -> None:
        if self._proc.poll() is None:
            os.killpg(self._proc.pid, signal.SIGKILL)
            self._proc.wait()
        for pipe in (self._proc.stdin, self._proc.stdout):
            if pipe:
                pipe.close()
        self._log.close()
