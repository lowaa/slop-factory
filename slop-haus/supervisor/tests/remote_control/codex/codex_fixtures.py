"""A host over Codex: the fixtures of the Claude host's tests, with the shared fake `codex app-server` in place of Claude Code."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Any

from fake_codex_live import FAKE_CODEX_LIVE
from host_fixtures import HostCase
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex
from slop_haus_supervisor.remote_control.host.codex_records import CodexRecords
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.settings import Settings

SERVER = "https://slop-haus.example"
THREAD = "0192f5a0-1111-7222-8333-444455556666"


class CodexHostCase(HostCase):
    """The host of HostCase, with Codex in place of Claude Code."""

    def setUp(self) -> None:
        super().setUp()
        os.environ["FAKE_DIR"] = self.logs  # restored with the rest of the environment HostCase patched
        self.codex_script = os.path.join(self.tmp, "fake_codex_live.py")
        with open(self.codex_script, "w") as f:
            f.write(FAKE_CODEX_LIVE)
        self.state_dir = Path(self.root, ".slop-haus", "conductor")
        self.codex_home = self.state_dir / "codex-home"
        self.host = self.new_codex_host()

    def new_codex_host(self) -> Host:
        harness = RemoteControlCodex(self.state_dir, SERVER, "the briefing", command=[sys.executable, self.codex_script], grace=5.0)
        host = Host(
            self.root, self.reporter, harness, Settings(harness_grace=5, idle_timeout=300), clock=self.clock,
            records=lambda h: CodexRecords(h.reporter, h, self.codex_home),
        )
        self.addCleanup(host.shutdown)
        return host

    def restart_host(self) -> None:
        self.host.shutdown()
        self.host = self.new_codex_host()

    def usage_record(self) -> dict[str, Any]:
        (record,) = self.reporter.usage[-1]["records"]
        return record

    def requests(self, method: str) -> list[dict[str, Any]]:
        return [r["params"] for r in self.received() if r.get("method") == method]

    def login(self, body: dict[str, Any]) -> None:
        self.codex_home.mkdir(parents=True, exist_ok=True)
        (self.codex_home / "auth.json").write_text(json.dumps(body))
