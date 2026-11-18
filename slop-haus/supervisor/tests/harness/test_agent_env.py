"""A run's process environment carries its agent's layered secrets and run.env, seen from a real subprocess."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.fleet.wiring import agent_env
from slop_haus_supervisor.harness.base import AgentEnv, OutcomeKind, RunSpec
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.pi import PiHarness
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.workspace.layered_env import AgentKind
from slop_haus_supervisor.workspace.layout import Layout

# Writes the whole environment it was started with, then ends the run.
PRINT_ENV = r'''
import json, os
with open(os.path.join(os.environ["FAKE_DIR"], "env.json"), "w") as f:
    json.dump(dict(os.environ), f)
print(json.dumps({"type": "system", "subtype": "init"}), flush=True)
print(json.dumps({"type": "result", "subtype": "success", "is_error": False}), flush=True)
'''


class AgentEnvTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.layout = Layout(self.tmp / "root")
        self.layout.root.mkdir()
        self.work = self.tmp / "work"
        self.work.mkdir()
        self.script = self.tmp / "print_env.py"
        self.script.write_text(PRINT_ENV)
        self.layout.secrets_file.write_text("SHARED_SECRET=base-secret-0001\nOVER_KIND=base-kind-0002\nOVER_AGENT=base-agent-0003\n")
        (self.layout.root / "run.env").write_text("BASE_RUN=base-run-0004\nPATH=/never/from/a/layer\n")
        self.layout.remote_control_dir.mkdir()
        (self.layout.remote_control_dir / "secrets.env").write_text("OVER_KIND=rc-kind-0005\nRC_ONLY=rc-only-0006\n")
        (self.layout.remote_control_dir / "run.env").write_text("RC_RUN=rc-run-0007\n")
        for name in ("w1", "r1"):
            self.layout.agent_dir(name).mkdir(parents=True)
            (self.layout.agent_dir(name) / "secrets.env").write_text(f"OVER_AGENT={name}-agent-0008\n")
            (self.layout.agent_dir(name) / "run.env").write_text(f"OWN_RUN={name}-run-0009\n")

    def env_of(self, kind: AgentKind, agent: str, harness: ClaudeHarness | None = None) -> dict[str, str]:
        log = self.tmp / "log"
        log.mkdir(exist_ok=True)
        harness = harness or self.harness(agent_env(self.layout, kind))
        run = harness.start(RunSpec(agent, self.work, 7, "ticket-7-a1", "go"))
        outcome = run.wait(30.0)
        self.assertIsNotNone(outcome)
        assert outcome is not None
        self.assertEqual(OutcomeKind.FINISHED, outcome.kind)
        return json.loads((log / "env.json").read_text())

    def harness(self, source: AgentEnv) -> ClaudeHarness:
        return ClaudeHarness(
            self.layout.root, "https://s", "", command=[sys.executable, str(self.script)],
            environ={"PATH": os.environ["PATH"], "FAKE_DIR": str(self.tmp / "log")},
            credentials={"CLAUDE_CODE_OAUTH_TOKEN": "tok"}, agent_env=source, grace=2.0, drain_seconds=0.2,
        )

    def test_a_worker_gets_base_then_its_own_layer_and_never_the_remote_control_layer(self) -> None:
        env = self.env_of(AgentKind.WORKER, "w1")

        self.assertEqual("base-secret-0001", env["SHARED_SECRET"])
        self.assertEqual("base-kind-0002", env["OVER_KIND"])
        self.assertEqual("w1-agent-0008", env["OVER_AGENT"])
        self.assertEqual("base-run-0004", env["BASE_RUN"])
        self.assertEqual("w1-run-0009", env["OWN_RUN"])
        self.assertNotIn("RC_ONLY", env)
        self.assertNotIn("RC_RUN", env)

    def test_a_remote_control_slot_gets_base_then_the_kind_layer_then_its_own(self) -> None:
        env = self.env_of(AgentKind.REMOTE_CONTROL, "r1")

        self.assertEqual("base-secret-0001", env["SHARED_SECRET"])
        self.assertEqual("rc-kind-0005", env["OVER_KIND"])
        self.assertEqual("r1-agent-0008", env["OVER_AGENT"])
        self.assertEqual("rc-only-0006", env["RC_ONLY"])
        self.assertEqual("rc-run-0007", env["RC_RUN"])
        self.assertEqual("r1-run-0009", env["OWN_RUN"])

    def test_path_stays_the_hosts(self) -> None:
        self.assertEqual(os.environ["PATH"], self.env_of(AgentKind.WORKER, "w1")["PATH"])

    def test_one_agents_values_never_reach_another(self) -> None:
        env = self.env_of(AgentKind.WORKER, "w2")

        self.assertEqual("base-agent-0003", env["OVER_AGENT"])
        self.assertNotIn("OWN_RUN", env)

    def test_a_layer_cannot_change_the_harnesss_own_settings(self) -> None:
        (self.layout.agent_dir("w1") / "run.env").write_text("CLAUDE_CONFIG_DIR=/elsewhere\n")

        env = self.env_of(AgentKind.WORKER, "w1")

        self.assertEqual(str(self.layout.root / "harness" / "claude"), env["CLAUDE_CONFIG_DIR"])

    def test_a_value_set_between_runs_reaches_the_next_run_and_its_masker_first(self) -> None:
        masker = SecretMasker()
        harness = self.harness(agent_env(self.layout, maskers=lambda _agent: [masker]))
        self.assertEqual("w1-agent-0008", self.env_of(AgentKind.WORKER, "w1", harness)["OVER_AGENT"])

        (self.layout.agent_dir("w1") / "secrets.env").write_text("OVER_AGENT=changed-0010\n")

        self.assertEqual("changed-0010", self.env_of(AgentKind.WORKER, "w1", harness)["OVER_AGENT"])
        self.assertEqual("***", masker.mask("changed-0010"))

    def test_pi_runs_get_the_agents_layers_too(self) -> None:
        pi = PiHarness(
            self.layout.root, self.tmp / "ext.ts", "", environ={"PATH": "/bin"}, agent_env=agent_env(self.layout)
        )

        env = pi.env("w1")

        self.assertEqual(("w1-agent-0008", "w1-run-0009"), (env["OVER_AGENT"], env["OWN_RUN"]))
