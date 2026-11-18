"""pi, run headless: `pi --mode rpc`, one prompt to its `agent_settled`."""

from __future__ import annotations

import json
import os
import shutil
import uuid
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness import classify
from slop_haus_supervisor.harness.base import AgentEnv, LaunchPlan, OutcomeKind, RunHandle, RunOutcome, RunSpec, Usage
from slop_haus_supervisor.harness.process import HarnessProcess, HarnessSession, ProcessRun
from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.harness.variant import PI_THINKING, InvalidVariant, Variant, check_choice, parse_variant
from slop_haus_supervisor.ports import Clock, SystemClock
from slop_haus_supervisor.service.run_env import RUN_SETTINGS
from slop_haus_supervisor.usage.billing import SUBSCRIPTION, counter_change, pi_billing_mode, pi_counters
from slop_haus_supervisor.usage.conversation import ConversationUsage

DETAIL_CHARS = 1500
DIALOGS = ("select", "confirm", "input", "editor")

# pi's built-in tools. --tools is an allowlist of every tool pi would offer, extensions' included.
PI_TOOLS = ("read", "bash", "edit", "write", "grep", "find", "ls")
# slop-haus's own tools, which the extension registers once it has asked the server. They have to
# be named in --tools too, because pi drops a tool the allowlist does not name before the
# extension's registration reaches it (checked against pi 0.99.1). A name the server no longer has
# is simply absent. Nothing in harness.denied.DENIED_TOOLS is listed (tested). contracts/tools.json's console role is the source: supervisor/tests/contracts asserts this list equals it (not derived at import, because the installed package has no repo checkout to read), and the server's mcpserver test holds the role to what it offers.
SLOP_HAUS_TOOLS = (
    "checkin", "ticket_get", "ticket_attachment_get", "ticket_attach", "ticket_search", "ticket_comment", "ticket_edit", "ticket_create",
    "ticket_discuss", "ticket_prepare", "ticket_release", "ticket_finish",
    "ticket_scope_extend", "ticket_scope_narrow", "ticket_blocker_add", "ticket_blocker_remove",
    "integration_lock", "integration_unlock", "change_open", "change_checks", "change_merge",
    "list_proposals", "propose_discussion_answer", "propose_ready", "propose_status", "propose_review_decision",
    "propose_ticket_edit", "propose_manifest_edit", "propose_close_merged", "withdraw_proposal", "working_on", "workspace_get",
    "landing_batches", "review_request", "review_findings", "review_label",
)


def prompt_message(text: str) -> str:
    return json.dumps({"type": "prompt", "message": text})


class PiSession(HarnessSession):
    """pi's RPC session, which can be told to abort the run in flight."""

    def interrupt(self) -> bool:
        # The turn ends at agent_settled as any other; the command's own response is not a record the host reads.
        self.send_line(json.dumps({"type": "abort"}))
        return True


class PiProtocol:
    """pi's RPC protocol, and the one place that knows it.

    A `prompt` command is answered at once and its turn ends at `agent_settled` (`agent_end` can be
    followed by a retry or a compaction); the turn's usage is `get_session_stats`, asked for at that
    point. The session's totals span every process that resumed it, so the run's usage is the stats
    at the end less the stats asked for before the prompt.
    """

    def __init__(self, prompt: str, clock: Clock, agent_dir: Path, provider: str = "", model: str = "") -> None:
        self._prompt = prompt
        self._agent_dir = agent_dir
        # What the variant asked for, until a message says which model really answered.
        self._provider = provider
        self._model = model
        self._clock = clock
        # pi prints nothing until asked, and has no session to guess about.
        self.started = True
        self.session_missing = False
        self._next_id = 0
        self._baseline_id = ""
        self._prompt_id = ""
        self._stats_id = ""
        self._baseline: dict[str, int] | None = None
        self._error = ""

    def _new_id(self, prefix: str) -> str:
        self._next_id += 1
        return f"{prefix}{self._next_id}"

    def begin(self, write: Callable[[str], None]) -> None:
        self._baseline_id = self._new_id("baseline-")
        write(json.dumps({"id": self._baseline_id, "type": "get_session_stats"}))
        self._prompt_id = self._new_id("prompt-")
        write(json.dumps({"id": self._prompt_id, "type": "prompt", "message": self._prompt}))

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        kind = record.get("type")
        if kind == "response":
            return self._on_response(record)
        if kind == "agent_settled" and not self._stats_id:
            self._stats_id = self._new_id("stats-")
            write(json.dumps({"id": self._stats_id, "type": "get_session_stats"}))
        elif kind == "message_end":
            message = as_dict(record.get("message")) or {}
            self._note_model(message)
            if message.get("role") == "assistant" and message.get("stopReason") == "error":
                self._error = str(message.get("errorMessage") or "the model request failed")
        elif kind == "auto_retry_end" and record.get("success") is False:
            self._error = str(record.get("finalError") or "the model request failed")
        elif kind == "extension_ui_request" and record.get("method") in DIALOGS:
            # Nobody is at a terminal to answer; left unanswered it would hold the turn for ever.
            write(json.dumps({"type": "extension_ui_response", "id": record.get("id"), "cancelled": True}))
        return None

    def _on_response(self, record: dict[str, Any]) -> RunOutcome | None:
        rid, success = record.get("id"), record.get("success") is True
        data = as_dict(record.get("data")) or {}
        if rid == self._baseline_id:
            self._baseline = pi_counters(data) if success else None
        elif rid == self._prompt_id:
            if not success:
                self._error = str(record.get("error") or "pi did not take the message")
                return self._finish(None)
            if data.get("disposition") == "handled":
                # An extension command took it: no run started, so no agent_settled is coming.
                return self._finish(None)
        elif self._stats_id and rid == self._stats_id:
            return self._finish(data if success else None)
        return None

    def _finish(self, stats: dict[str, Any] | None) -> RunOutcome:
        # Without a baseline the session's whole history would be taken for this run's.
        usage = self._usage_of(stats)
        if not self._error:
            return RunOutcome(OutcomeKind.FINISHED, usage=usage)
        detail = clean_text(self._error, DETAIL_CHARS)
        failure = classify.classify_text(self._error, self._clock.now())
        if failure is None:
            return RunOutcome(OutcomeKind.CRASHED, detail, usage)
        return RunOutcome(failure.kind, detail, usage, failure.reset_at)

    def _note_model(self, message: dict[str, Any]) -> None:
        provider, model = message.get("provider"), message.get("model")
        if message.get("role") == "assistant" and isinstance(provider, str) and isinstance(model, str):
            self._provider, self._model = provider, model

    def _usage_of(self, stats: dict[str, Any] | None) -> Usage:
        # Without a baseline the session's whole history would be taken for this run's, and without a
        # model the server could not price it.
        if stats is None or self._baseline is None or not self._model:
            return Usage()
        usage = ConversationUsage()
        usage.add_delta(self._provider, self._model, pi_billing_mode(self._agent_dir, self._provider),
                        counter_change(pi_counters(stats), self._baseline))
        return usage.as_usage(SUBSCRIPTION)

    def on_exit(self, code: int, stderr: str) -> RunOutcome:
        detail = clean_text(f"pi exited (status {code}) before the turn ended: {stderr}", DETAIL_CHARS)
        failure = classify.classify_text(stderr, self._clock.now())
        if failure is not None:
            return RunOutcome(failure.kind, detail, reset_at=failure.reset_at)
        return RunOutcome(OutcomeKind.CRASHED, detail)


class PiHarness:
    name = "pi"

    def __init__(
        self,
        root: Path,
        extension: Path,
        system_prompt: str,
        *,
        command: Sequence[str] = ("pi",),
        environ: Mapping[str, str] | None = None,
        credentials: Mapping[str, str] | None = None,
        agent_env: AgentEnv | None = None,
        clock: Clock | None = None,
        grace: float = 5.0,
    ) -> None:
        self._root = root
        self._extension = extension
        self._system_prompt = system_prompt
        self._command = list(command)
        self._environ = environ if environ is not None else os.environ
        # Provider keys, from the root's secrets.env.
        self._credentials = dict(credentials or {})
        self._agent_env = agent_env
        self._clock = clock or SystemClock()
        self._grace = grace

    def check_installed(self) -> None:
        if shutil.which(self._command[0]) is None:
            raise FileNotFoundError(f"{self._command[0]} is not installed")

    def missing_credentials(self) -> str:
        # Provider keys differ per provider, so any *_API_KEY will do (secrets.env also holds unrelated
        # worker variables); pi's own /login is the other way in.
        if any(k.endswith("_API_KEY") and v for k, v in self._credentials.items()) or (self.config_dir(self._root) / "auth.json").is_file():
            return ""
        return "a provider *_API_KEY (or log pi in under " + str(self.config_dir(self._root)) + ")"

    def config_dir(self, root: Path) -> Path:
        return root / "harness" / "pi"

    def start(self, spec: RunSpec) -> RunHandle:
        return self._launch(spec, str(uuid.uuid4()))

    def resume(self, spec: RunSpec, session_id: str) -> RunHandle:
        # pi's --session-id creates a session that is missing, so there is no SessionNotFound.
        return self._launch(spec, session_id)

    def argv(self, session_id: str, variant: Variant, system_prompt: str = "") -> list[str]:
        flags: list[str] = []
        for flag, value in (("--provider", variant.provider), ("--model", variant.model), ("--thinking", variant.thinking)):
            if value:
                flags += [flag, value]
        return self._command + [
            "--mode", "rpc",
            "--tools", ",".join(PI_TOOLS + SLOP_HAUS_TOOLS),
            "--no-extensions", "-e", str(self._extension),
            "--no-approve",
            "--append-system-prompt", system_prompt or self._system_prompt,
            "--session-id", session_id,
        ] + flags

    def env(self, agent: str = "") -> dict[str, str]:
        env = {k: v for k, v in self._environ.items() if not k.startswith("SLOP_HAUS_") or k in RUN_SETTINGS}
        env.update(self._credentials)
        # Before the settings below, which a layer must not change.
        if self._agent_env is not None and agent:
            env.update(self._agent_env(agent))
        env["SLOP_HAUS_HOST_SESSION"] = "1"
        env["PI_CODING_AGENT_DIR"] = str(self.config_dir(self._root))
        return env

    def plan(self, spec: RunSpec, session_id: str) -> LaunchPlan:
        """Validates the spec and prepares the config directory the process needs."""
        variant = parse_variant(spec.variant)
        check_choice("thinking", variant.thinking, PI_THINKING)
        if variant.effort or variant.subagent_model:
            raise InvalidVariant("effort and subagent_model are claude settings")
        self.config_dir(self._root).mkdir(mode=0o700, parents=True, exist_ok=True)
        return LaunchPlan(self.argv(session_id, variant, spec.system_prompt), self.env(spec.agent), spec.directory)

    def open_session(self, spec: RunSpec, session_id: str | None = None) -> HarnessSession:
        """A live process for the caller to converse with; `spec.prompt` is not sent."""
        sid = session_id or str(uuid.uuid4())
        plan = self.plan(spec, sid)
        return PiSession(HarnessProcess(plan.argv, plan.env, plan.cwd), prompt_message, sid, self._grace)

    def _launch(self, spec: RunSpec, session_id: str) -> ProcessRun:
        plan = self.plan(spec, session_id)
        variant = parse_variant(spec.variant)
        process = HarnessProcess(plan.argv, plan.env, plan.cwd)
        return ProcessRun(process, PiProtocol(spec.prompt, self._clock, self.config_dir(self._root), variant.provider, variant.model), session_id, self._grace)
