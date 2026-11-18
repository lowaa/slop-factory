"""A host over pi: the fixtures of the Claude host's tests, with the shared fake `pi --mode rpc` in place of Claude Code."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Any

from fake_pi import FAKE_PI
from host_fixtures import HostCase
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.prompt import SYSTEM_PROMPT
from slop_haus_supervisor.remote_control.host.settings import Settings
from slop_haus_supervisor.remote_control.pi.errors import PiErrors
from slop_haus_supervisor.remote_control.pi.harness import RemoteControlPi
from slop_haus_supervisor.remote_control.pi.rpc import PiRpc



class PiHostCase(HostCase):
    """The host of HostCase, with pi in place of Claude Code."""

    def setUp(self) -> None:
        super().setUp()
        os.environ["FAKE_DIR"] = self.logs  # restored with the rest of the environment HostCase patched
        self.pi_script = os.path.join(self.tmp, "fake_pi.py")
        with open(self.pi_script, "w") as f:
            f.write(FAKE_PI)
        self.extension = Path(self.root, ".slop-haus", "pi", "slop-haus.ts")
        self.extension.parent.mkdir(parents=True)
        self.extension.write_text("export default function () {}\n")
        self.state_dir = Path(self.root, ".slop-haus", "conductor")
        self.agent_dir = self.state_dir / "pi-agent"
        self.host = self.new_pi_host()

    def new_pi_host(self, variant: str = "") -> Host:
        harness = RemoteControlPi(
            self.state_dir, self.extension, SYSTEM_PROMPT, variant, command=[sys.executable, self.pi_script], grace=5.0,
        )
        host = Host(
            self.root, self.reporter, harness, Settings(harness_grace=5, idle_timeout=300), clock=self.clock,
            records=lambda h: PiRpc(h.reporter, h, PiErrors(self.agent_dir), self.agent_dir),
        )
        self.addCleanup(host.shutdown)
        return host

    def restart_host(self) -> None:
        self.host.shutdown()
        self.host = self.new_pi_host()

    def usage_record(self) -> dict[str, Any]:
        (record,) = self.reporter.usage[-1]["records"]
        return record

    def auth(self, body: dict[str, Any]) -> None:
        self.agent_dir.mkdir(parents=True, exist_ok=True)
        (self.agent_dir / "auth.json").write_text(json.dumps(body))
