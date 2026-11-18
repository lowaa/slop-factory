"""The supervisor's side of slop-haus/contracts/*.json: its own constants equal the manifest."""

from __future__ import annotations

import json
import re
import unittest
from pathlib import Path
from typing import Any

from slop_haus_supervisor.fleet.phase import Phase, Reason
from slop_haus_supervisor.harness.denied import DENIED_TOOLS
from slop_haus_supervisor.harness.mcp_config import TOOL_CALL_TIMEOUT_MS
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.pi import SLOP_HAUS_TOOLS
from slop_haus_supervisor.slophaus.token import RUN_TIMEOUT
from slop_haus_supervisor.steward.guard import STEWARD_ALLOWED_TOOLS, STEWARD_BUILTIN_TOOLS, WORKER_TOOLS
from slop_haus_supervisor.workspace.bundle import BUNDLE_HASH_FILE
from slop_haus_supervisor.workspace.layered_env import OPERATOR_SETTINGS
from slop_haus_supervisor.workspace.briefing import BRIEFING
from slop_haus_supervisor.workspace.provisioned import REFRESH_TIMEOUT, UPDATER
from slop_haus_supervisor.workspace.rebuild import IDENTITY_FILES

ROOT = Path(__file__).resolve().parents[3]
SOURCE = ROOT / "supervisor" / "src" / "slop_haus_supervisor"


def manifest(name: str) -> dict[str, Any]:
    return json.loads((ROOT / "contracts" / name).read_text())


class ContractsTest(unittest.TestCase):
    def test_harness_registry(self) -> None:
        listed = {
            h["name"]: (h["capabilities"]["worker"], h["capabilities"]["steward"], h["capabilities"]["remote_control"])
            for h in manifest("harnesses.json")["harnesses"]
        }
        mine = {h.name: (h.worker, h.steward, h.remote_control) for h in registry.HARNESS_ENTRIES}
        self.assertEqual(listed, mine)

    def setUp(self) -> None:
        tools = manifest("tools.json")
        self.prefix: str = tools["prefix"]
        self.roles: dict[str, list[str]] = tools["roles"]
        self.builtin_denied: list[str] = tools["denied_builtin_tools"]

    def prefixed(self, names: list[str]) -> list[str]:
        return [self.prefix + n for n in names]

    def test_denied_tools(self) -> None:
        self.assertCountEqual(DENIED_TOOLS, self.prefixed(self.roles["denied_to_headless"]) + self.builtin_denied)

    def test_console_tools_are_the_pi_allowlist(self) -> None:
        self.assertCountEqual(SLOP_HAUS_TOOLS, self.roles["console"])

    def test_steward_tools(self) -> None:
        self.assertCountEqual(STEWARD_ALLOWED_TOOLS, [*STEWARD_BUILTIN_TOOLS, *self.prefixed(self.roles["steward"])])

    def test_steward_denied_worker_tools(self) -> None:
        self.assertCountEqual(WORKER_TOOLS, [*DENIED_TOOLS, *self.prefixed(self.roles["denied_to_steward"])])

    def test_slot_phases(self) -> None:
        self.assertCountEqual([p.value for p in Phase], manifest("phases.json")["slot_phases"])

    def test_slot_reasons(self) -> None:
        self.assertCountEqual([r.value for r in Reason], manifest("phases.json")["slot_reasons"])

    def test_paths_the_supervisor_names(self) -> None:
        paths = manifest("paths.json")
        known = {*paths["workspace_paths"].values(), *paths["python_only_paths"].values()}
        self.assertEqual(BRIEFING.as_posix(), paths["workspace_paths"]["headless_briefing"])
        self.assertEqual(BUNDLE_HASH_FILE, paths["workspace_paths"]["bundle_hash"])
        self.assertEqual(UPDATER.as_posix(), paths["workspace_paths"]["updater"])
        self.assertTrue(set(IDENTITY_FILES) <= known, set(IDENTITY_FILES) - known)

    def test_every_dot_slop_haus_literal_is_in_the_manifest(self) -> None:
        paths = manifest("paths.json")
        known = {*paths["workspace_paths"].values(), *paths["python_only_paths"].values()}
        # `".slop-haus" / "pi" / "slop-haus.ts"` and `".slop-haus/token.sh"`, with a trailing f-string part dropped.
        chain = re.compile(r'"\.slop-haus"((?:\s*/\s*"[^"]+")+)')
        inline = re.compile(r'"(\.slop-haus/[A-Za-z0-9_./-]+)"')
        found: set[tuple[str, str]] = set()
        for source in SOURCE.rglob("*.py"):
            text = source.read_text()
            for m in chain.finditer(text):
                parts = re.findall(r'"([^"]+)"', m.group(1))
                found.add((source.name, ".slop-haus/" + "/".join(parts)))
            for m in inline.finditer(text):
                found.add((source.name, m.group(1)))
        self.assertTrue(found)
        for name, path in found:
            self.assertIn(path, known | {".slop-haus/conductor"}, f"{name} spells {path}, which contracts/paths.json lacks")

    def test_timeouts(self) -> None:
        timeouts = manifest("timeouts.json")
        self.assertEqual(TOOL_CALL_TIMEOUT_MS, timeouts["tool_call_ms"]["value"])
        self.assertEqual(RUN_TIMEOUT, timeouts["token_and_stop_script_s"]["value"])
        self.assertEqual(REFRESH_TIMEOUT, timeouts["token_and_stop_script_s"]["value"])

    def test_every_env_var_is_registered(self) -> None:
        env = manifest("env.json")
        registry = {**env["env"], **env["not_env_vars"]}
        pattern = re.compile(r"(?<![A-Za-z0-9_])(SLOP_HAUS_[A-Z][A-Z_]*[A-Z])\b")
        for source in SOURCE.rglob("*.py"):
            for name in pattern.findall(source.read_text()):
                self.assertIn(name, registry, f"{source.name} uses {name}, which contracts/env.json lacks")

    def test_every_operator_setting_declares_a_safe_default(self) -> None:
        # The base run.env drops a SLOP_HAUS_ name the supervisor does not know, which is only safe while unset is the safe state.
        registry = manifest("env.json")["env"]
        self.assertTrue(OPERATOR_SETTINGS)
        for name in sorted(OPERATOR_SETTINGS):
            self.assertIn(name, registry)
            for field in ("default", "safe_default"):
                self.assertTrue(registry[name].get(field, "").strip(), f"{name} declares no {field} in contracts/env.json")

    def test_clone_falsy_values(self) -> None:
        falsy = manifest("env.json")["env"]["SLOP_HAUS_CLONE"]["falsy_values"]
        provisioned = (SOURCE / "workspace" / "provisioned.py").read_text()
        listed = re.search(r'SLOP_HAUS_CLONE", ""\)\.lower\(\) in \(([^)]*)\)', provisioned)
        self.assertIsNotNone(listed)
        assert listed is not None
        self.assertEqual(re.findall(r'"([^"]+)"', listed.group(1)), falsy)
