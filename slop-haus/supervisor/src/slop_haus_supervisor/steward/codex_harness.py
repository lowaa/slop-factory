"""The Codex steward: CodexHarness under the read-only profile of `codex_policy`, with a tool-call budget."""

from __future__ import annotations

import json
from collections.abc import Callable
from dataclasses import replace
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.base import LaunchPlan, OutcomeKind, RunOutcome, RunSpec
from slop_haus_supervisor.harness.codex import CodexHarness, CodexRun
from slop_haus_supervisor.harness.codex_protocol import THREAD, TURN, CodexProtocol
from slop_haus_supervisor.harness.process import HarnessProcess
from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.harness.variant import parse_variant
from slop_haus_supervisor.steward.codex_policy import PROFILE, CodexStewardPolicy
from slop_haus_supervisor.usage.billing import codex_login_billing_mode

INTERRUPT = 4
# Items that are the model talking or the user's prompt; anything else is a tool call and spends the budget.
NOT_TOOL_ITEMS = frozenset({"userMessage", "agentMessage", "reasoning", "plan", "hookPrompt"})


class StewardProtocol(CodexProtocol):
    """CodexProtocol that refuses a thread whose permission profile is not the steward's and counts tool calls.

    Codex has no `--max-turns`, so the supervisor counts tool-call items and sends `turn/interrupt` once `max_tool_calls`
    is passed. The runner's wall-clock timeout is the backstop if the interrupt is never honoured.
    """

    def __init__(self, *args: Any, max_tool_calls: int, **kw: Any) -> None:
        super().__init__(*args, **kw)
        self._max = max_tool_calls
        self.tool_calls = 0
        self.over_budget = False
        self._turn_id = ""

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        result = as_dict(record.get("result"))
        if record.get("id") == THREAD and result is not None:
            profile = as_dict(result.get("activePermissionProfile")) or {}
            if profile.get("id") != PROFILE:
                return RunOutcome(OutcomeKind.CRASHED, f"codex did not apply the {PROFILE} permission profile (got {profile.get('id')!r}); the steward is not run unconfined")
        if record.get("id") == TURN and result is not None:
            self._remember_turn(as_dict(result.get("turn")))
        method, params = record.get("method"), as_dict(record.get("params")) or {}
        if method == "turn/started":
            self._remember_turn(as_dict(params.get("turn")))
        elif method == "item/started" and record.get("id") is None:
            item = as_dict(params.get("item")) or {}
            if item.get("type") not in NOT_TOOL_ITEMS:
                self.tool_calls += 1
                if self.tool_calls > self._max and not self.over_budget and self._turn_id:
                    self.over_budget = True
                    write(json.dumps({"id": INTERRUPT, "method": "turn/interrupt", "params": {"threadId": self.thread_id, "turnId": self._turn_id}}))
        outcome = super().on_record(record, write)
        # A turn that finished anyway (the interrupt lost the race) still spent more than its budget.
        if outcome is not None and self.over_budget:
            return RunOutcome(OutcomeKind.OVER_BUDGET, f"the steward passed its budget of {self._max} tool calls", outcome.usage)
        return outcome

    def _remember_turn(self, turn: dict[str, Any] | None) -> None:
        if turn and isinstance(turn.get("id"), str):
            self._turn_id = turn["id"]


class CodexStewardHarness(CodexHarness):
    """A CodexHarness that can only run as the steward: its thread config and process flags come from the policy."""

    def __init__(self, root: Path, server: str, policy: CodexStewardPolicy, max_tool_calls: int, **kw: Any) -> None:
        super().__init__(root, server, **kw)
        self.policy = policy
        self._max_tool_calls = max_tool_calls

    def thread_params(self, spec: RunSpec) -> dict[str, Any]:
        params = super().thread_params(spec)
        # No `sandbox`: the profile is the sandbox, and the legacy field would replace it.
        params.pop("sandbox", None)
        config = dict(params["config"])
        config.pop("mcp_servers.slop-haus.disabled_tools", None)
        config.update(self.policy.config())
        params["config"] = config
        return params

    def plan(self, spec: RunSpec) -> LaunchPlan:
        plan = super().plan(spec)
        # Process-level too, so a thread that carried no config of its own could not run wider than the policy.
        return replace(plan, argv=[*self._command, *self.policy.flags(), "app-server"])

    def _launch(self, spec: RunSpec, resume_id: str) -> CodexRun:
        plan = self.plan(spec)
        protocol = StewardProtocol(
            spec.prompt, self._clock, self.thread_params(spec), resume_id, parse_variant(spec.variant).effort,
            codex_login_billing_mode(self.config_dir(self._root)), max_tool_calls=self._max_tool_calls,
        )
        return CodexRun(HarnessProcess(plan.argv, plan.env, plan.cwd), protocol, self._grace)
