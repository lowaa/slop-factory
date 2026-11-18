"""Runs the supervisor's Codex steward code (policy, sandbox probe, harness) for a scenario, under the root's own
copy of the package and its Python, as the supervisor would. The scenario passes one JSON object as argv[1] and reads
one JSON object from the last line of stdout.

  mode "probe": SandboxProbe(...).check() under the steward's policy, optionally with one protection taken out
                (`breakage`), so a scenario can show the probe refusing: {"ok": bool, "problems": [...]}
  mode "run":   the probe (unless `skip_probe`, which a negative test uses to run a steward the probe would refuse), then a steward run of the scripted model's turns:
                {"ok": bool, "problems": [...], "kind": ..., "detail": ..., "allowed_tools": [...]}
"""

from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.steward import codex_probe
from slop_haus_supervisor.steward.codex_harness import CodexStewardHarness
from slop_haus_supervisor.steward.codex_policy import PROFILE, CodexStewardPolicy, allowed_mcp_tools
from slop_haus_supervisor.steward.codex_probe import CodexAppServerExec, SandboxProbe, SandboxUnsafe


@dataclass(frozen=True)
class BrokenPolicy(CodexStewardPolicy):
    """The steward's policy with one protection taken out."""

    breakage: str = ""
    forbidden_dir: str = ""

    def config(self) -> dict[str, Any]:
        config = dict(super().config())
        filesystem = dict(config[f"permissions.{PROFILE}.filesystem"])
        if self.breakage == "readable":
            filesystem[self.forbidden_dir] = "read"
        elif self.breakage == "writable":
            filesystem[str(self.scratch)] = "write"
        elif self.breakage == "environment":
            config["shell_environment_policy.inherit"] = "all"
        elif self.breakage == "network":
            config[f"permissions.{PROFILE}.network.enabled"] = True
        elif self.breakage:
            raise SystemExit(f"unknown breakage {self.breakage!r}")
        config[f"permissions.{PROFILE}.filesystem"] = filesystem
        return config


def main() -> None:
    args = json.loads(sys.argv[1])
    root, scratch = Path(args["root"]), Path(args["scratch"])
    # The credential the steward's shell must not see, put where Codex (not its shell) holds it.
    os.environ[args["secret_name"]] = args["secret_value"]
    # A host the harness network allows, so "the network is reachable" can be shown without the internet.
    codex_probe.NETWORK_TARGET = args["network_target"]
    policy = BrokenPolicy(
        Path(os.path.realpath(args["codex"])), scratch, tuple(Path(p) for p in args["review_roots"]),
        breakage=args.get("breakage", ""), forbidden_dir=args["forbidden"][0],
    )
    harness = CodexStewardHarness(root, args["server"], policy, args.get("max_tool_calls", 20), credentials=args["credentials"])
    out: dict[str, Any] = {"ok": True, "problems": [], "allowed_tools": allowed_mcp_tools()}
    try:
        if args.get("skip_probe"):
            raise SandboxUnsafe([])
        SandboxProbe(CodexAppServerExec((args["codex"],), policy, harness.env()), policy, [Path(p) for p in args["forbidden"]]).check()
    except SandboxUnsafe as err:
        out.update(ok=bool(args.get("skip_probe")), problems=err.problems)
    if args["mode"] == "run" and out["ok"]:
        scratch.mkdir(parents=True, exist_ok=True)
        run = harness.start(RunSpec("steward", scratch, 0, "", args["prompt"], variant=f"model={args['model']}", system_prompt="You are the steward."))
        outcome = run.wait(args["timeout"])
        out["kind"] = outcome.kind.value if outcome else "still running"
        out["detail"] = outcome.detail if outcome else ""
        if outcome is None:
            run.kill()
    print(json.dumps(out))


if __name__ == "__main__":
    main()
