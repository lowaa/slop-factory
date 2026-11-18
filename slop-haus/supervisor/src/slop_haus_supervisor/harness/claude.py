"""Claude Code, run headless: `claude -p` speaking stream-json, one prompt to its `result` record."""

from __future__ import annotations

import json
import os
import shutil
import threading
import uuid
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness import classify
from slop_haus_supervisor.harness.base import AgentEnv, LaunchPlan, OutcomeKind, RunHandle, RunOutcome, RunSpec, SessionNotFound, Usage
from slop_haus_supervisor.harness.mcp_config import AGENT_NAME, write_mcp_config
from slop_haus_supervisor.harness.process import DRAIN_SECONDS, HarnessProcess, HarnessSession, ProcessRun
from slop_haus_supervisor.harness.text import SecretMasker, as_dict, as_list
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.harness.variant import CLAUDE_EFFORT, InvalidVariant, Variant, check_choice, parse_variant
from slop_haus_supervisor.ports import Clock, SystemClock
from slop_haus_supervisor.service.run_env import RUN_SETTINGS
from slop_haus_supervisor.usage.baseline import SessionBaseline
from slop_haus_supervisor.usage.billing import SUBSCRIPTION, claude_billing_mode
from slop_haus_supervisor.usage.conversation import ConversationUsage

DETAIL_CHARS = 1500
START_TIMEOUT = 60.0

def credential_denials(root: Path) -> list[str]:
    """What stays out of the Read tool, and no more. It guards against a careless read, not a boundary:
    with permissions bypassed a shell reads anything, and the token is in the run's own environment."""
    # "//" starts an absolute path in a Claude Code permission rule.
    return [
        "Read(//**/.slop-haus/**)",
        "Read(//**/.slop-haus.env)",
        f"Read(/{root / 'secrets.env'})",
        f"Read(/{root / 'harness'}/**)",
    ]


def user_message(text: str) -> str:
    return json.dumps({"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": text}]}})


def slot_token_script(spec: RunSpec) -> Path:
    # The slot's sign-in is at the workspace root, which in a nested workspace is above the run's repo.
    return (spec.workspace or spec.directory) / ".slop-haus" / "token.sh"


CONTROL_RESPONSE = "control_response"


class ClaudeSession(HarnessSession):
    """Claude Code's stream-json session, which can be sent an interrupt control request."""

    def __init__(self, process: HarnessProcess, encode_user_message: Callable[[str], str], session_id: str,
                 grace: float = 5.0) -> None:
        super().__init__(process, encode_user_message, session_id, grace)
        self._requests = 0
        self._request_lock = threading.Lock()

    def interrupt(self) -> bool:
        with self._request_lock:
            self._requests += 1
            request_id = f"slop-haus-interrupt-{self._requests}"
        self.send_line(json.dumps({"type": "control_request", "request_id": request_id, "request": {"subtype": "interrupt"}}))
        return True

    def next_record(self) -> dict[str, Any] | None:
        # Answers to our own requests, not part of the conversation.
        while True:
            record = super().next_record()
            if record is None or record.get("type") != CONTROL_RESPONSE:
                return record


class ClaudeProtocol:
    """What the records of one Claude Code process mean to a run."""

    def __init__(self, prompt: str, session_id: str, clock: Clock, baseline: SessionBaseline | None = None) -> None:
        self._prompt = prompt
        self._session_id = session_id
        self._clock = clock
        self._baseline = baseline
        self.started = False
        self.session_missing = False
        # A result before init is Claude Code's answer to a --resume of a session it does not have,
        # before it exits; on_exit decides what it was once stderr says why.
        self._startup_result: dict[str, Any] | None = None
        self._rejected_reset: float | None = None
        self._rejected = False
        self._billing_mode = SUBSCRIPTION

    def begin(self, write: Callable[[str], None]) -> None:
        write(user_message(self._prompt))

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        kind = record.get("type")
        if kind == "system" and record.get("subtype") == "init":
            self.started = True
            self._billing_mode = claude_billing_mode(record.get("apiKeySource"))
        elif kind == "rate_limit_event":
            info = as_dict(record.get("rate_limit_info")) or {}
            if info.get("status") == "rejected":
                self._rejected = True
                self._rejected_reset = classify.rejected_reset(info, self._clock.now())
        elif kind == "result":
            if not self.started:
                self._startup_result = record
                return None
            return self._outcome_of(record)
        return None

    def on_exit(self, code: int, stderr: str) -> RunOutcome:
        text = " ".join(filter(None, [self._result_text(self._startup_result), stderr]))
        if classify.is_resume_failure(text):
            self.session_missing = True
        failure = classify.classify_text(text, self._clock.now())
        detail = clean_text(f"claude exited (status {code}) without a result: {text}", DETAIL_CHARS)
        if failure is not None:
            return RunOutcome(failure.kind, detail, reset_at=failure.reset_at)
        return RunOutcome(OutcomeKind.CRASHED, detail)

    @staticmethod
    def _result_text(result: dict[str, Any] | None) -> str:
        if result is None or not result.get("is_error"):
            return ""
        # Claude Code 2.1.303 puts a refusal like "No conversation found" in `errors`, with no `result` text.
        errors = [str(e) for e in as_list(result.get("errors"))]
        return " ".join(filter(None, [str(result.get("result") or ""), *errors])) or str(result.get("subtype") or "")

    def _outcome_of(self, record: dict[str, Any]) -> RunOutcome:
        usage = usage_of(record, self._billing_mode, self._baseline)
        # Claude Code ends a run that reached --max-turns with this subtype, whether or not it flags an error.
        if record.get("subtype") == "error_max_turns":
            return RunOutcome(OutcomeKind.OVER_BUDGET, "the run reached its turn budget (--max-turns)", usage)
        if not record.get("is_error"):
            return RunOutcome(OutcomeKind.FINISHED, usage=usage)
        text = self._result_text(record) or "the turn failed"
        detail = clean_text(text, DETAIL_CHARS)
        failure = classify.classify_text(text, self._clock.now())
        if failure is not None:
            reset = failure.reset_at
            if failure.kind is OutcomeKind.USAGE_LIMIT and self._rejected_reset is not None:
                reset = self._rejected_reset
            return RunOutcome(failure.kind, detail, usage, reset)
        if self._rejected:
            return RunOutcome(OutcomeKind.USAGE_LIMIT, detail, usage, self._rejected_reset)
        return RunOutcome(OutcomeKind.CRASHED, detail, usage)


def usage_of(result: Mapping[str, Any], billing_mode: str, baseline: SessionBaseline | None = None) -> Usage:
    """This run's usage, per model: the change in the result's modelUsage since the session's last run.

    modelUsage is cumulative for the session, and a resumed run starts from the runs before it
    (Claude Code 2.1.303), so the run's own is what the baseline, kept per session, has not seen.
    It includes subagents' work, a subagent on another model as an entry of its own, which is how
    the server prices each at its own rate.
    """
    usage = ConversationUsage({"reported": baseline.load()} if baseline else None)
    usage.add_turn(dict(result))
    if baseline:
        baseline.save(usage.reported)
    return usage.as_usage(billing_mode)


class ClaudeHarness:
    name = "claude"

    def __init__(
        self,
        root: Path,
        server: str,
        system_prompt: str,
        *,
        command: Sequence[str] = ("claude",),
        denied_tools: Sequence[str] = (),
        allowed_tools: Sequence[str] = (),
        builtin_tools: Sequence[str] = (),
        environ: Mapping[str, str] | None = None,
        credentials: Mapping[str, str] | None = None,
        masker: SecretMasker | None = None,
        token_script: Callable[[RunSpec], Path] | None = None,
        clock: Clock | None = None,
        grace: float = 5.0,
        start_timeout: float = START_TIMEOUT,
        drain_seconds: float = DRAIN_SECONDS,
        agent_env: AgentEnv | None = None,
    ) -> None:
        self._drain_seconds = drain_seconds
        self._root = root
        self._server = server
        self._system_prompt = system_prompt
        self._command = list(command)
        self._denied = list(denied_tools)
        # Non-empty switches the run from bypassing permissions to an allowlist: see argv.
        self._allowed = list(allowed_tools)
        self._builtin = list(builtin_tools)
        self._environ = environ if environ is not None else os.environ
        # CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY, from the root's secrets.env.
        self._credentials = dict(credentials or {})
        self._agent_env = agent_env
        self._masker = masker or SecretMasker(self._credentials.values())
        self._token_script: Callable[[RunSpec], Path] = token_script or slot_token_script
        self._clock = clock or SystemClock()
        self._grace = grace
        self._start_timeout = start_timeout

    def check_installed(self) -> None:
        if shutil.which(self._command[0]) is None:
            raise FileNotFoundError(f"{self._command[0]} is not installed")

    def missing_credentials(self) -> str:
        if self._credentials.get("CLAUDE_CODE_OAUTH_TOKEN") or self._credentials.get("ANTHROPIC_API_KEY"):
            return ""
        return "CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY"

    def config_dir(self, root: Path) -> Path:
        return root / "harness" / "claude"

    def start(self, spec: RunSpec) -> RunHandle:
        return self._launch(spec, str(uuid.uuid4()), resume=False)

    def resume(self, spec: RunSpec, session_id: str) -> RunHandle:
        run = self._launch(spec, session_id, resume=True)
        run.await_start(self._start_timeout)
        if run.session_missing:
            run.kill()
            raise SessionNotFound(session_id)
        return run

    @staticmethod
    def settings_flags(spec: RunSpec) -> list[str]:
        """The workspace's settings, for a run started in a repo below it.

        Claude Code reads project settings (the hooks among them) from the working directory only, so a run
        in a nested workspace's repo would lose the workspace's Stop and message-delivery hooks (checked on
        2.1.303). --settings is read in addition to the repo's own settings.
        """
        if spec.workspace is None or spec.workspace.resolve() == spec.directory.resolve():
            return []
        settings = spec.workspace / ".claude" / "settings.json"
        return ["--settings", str(settings)] if settings.is_file() else []

    def argv(self, session_id: str, resume: bool, mcp_config: Path, system_prompt: str = "", settings: Sequence[str] = ()) -> list[str]:
        denied = credential_denials(self._root) + self._denied
        # With an allowlist, dontAsk refuses every tool not named in --allowedTools, and --tools removes the
        # other built-ins (Bash, Edit, Write, web) from the model's reach altogether. Bypassing permissions
        # would instead allow everything the deny-list forgot.
        permissions = (
            ["--permission-mode", "dontAsk", "--tools", ",".join(self._builtin), "--allowedTools", ",".join(self._allowed)]
            if self._allowed
            else ["--permission-mode", "bypassPermissions"]
        )
        return self._command + [
            "-p",
            "--input-format", "stream-json",
            "--output-format", "stream-json",
            "--verbose",
            "--include-partial-messages",
            *permissions,
            "--disallowedTools", ",".join(denied),
            "--strict-mcp-config",
            "--mcp-config", str(mcp_config),
            *settings,
            "--append-system-prompt", system_prompt or self._system_prompt,
            "--resume" if resume else "--session-id", session_id,
        ]

    def env(self, variant: Variant, agent: str = "") -> dict[str, str]:
        env = {k: v for k, v in self._environ.items() if not k.startswith("SLOP_HAUS_") or k in RUN_SETTINGS}
        env.update(self._credentials)
        # Before the settings below, which a layer must not change.
        if self._agent_env is not None and agent:
            env.update(self._agent_env(agent))
        # The hooks exit early on this, and auto-memory is off: a run must not write into anyone's memory.
        env["SLOP_HAUS_HOST_SESSION"] = "1"
        env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
        # A run that ends its turn must have nothing left waiting for it, and Bash's run_in_background
        # cannot be denied by a tool rule (a background watcher once stranded the integration lock).
        # Mirrors the server's HeadlessRunEnv.
        env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] = "1"
        # A slop-review pass and full test runs take far longer than Claude Code's 2m default and 10m
        # cap, and with background shells off they must finish inside one foreground call.
        # Mirrors the server's HeadlessRunEnv.
        env["BASH_DEFAULT_TIMEOUT_MS"] = "1800000"
        env["BASH_MAX_TIMEOUT_MS"] = "3600000"
        # One directory per tenant, never ~/.claude, so a person's settings, plugins and connectors never reach a worker.
        env["CLAUDE_CONFIG_DIR"] = str(self.config_dir(self._root))
        if variant.model:
            env["ANTHROPIC_MODEL"] = variant.model
        if variant.effort:
            env["CLAUDE_CODE_EFFORT_LEVEL"] = variant.effort
        if variant.subagent_model:
            env["CLAUDE_CODE_SUBAGENT_MODEL"] = variant.subagent_model
            env["CLAUDE_CODE_SUBAGENT_MODEL_FORCE"] = "1"
        return env

    def plan(self, spec: RunSpec, session_id: str, resume: bool) -> LaunchPlan:
        """Validates the spec and prepares the config directory and MCP config the process needs."""
        if not AGENT_NAME.match(spec.agent):
            raise ValueError(f"agent name {spec.agent!r} is not usable in a path")
        variant = parse_variant(spec.variant)
        check_choice("effort", variant.effort, CLAUDE_EFFORT)
        if variant.provider or variant.thinking:
            raise InvalidVariant("provider and thinking are pi settings")
        config_dir = self.config_dir(self._root)
        config_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        mcp_config = self._root / "state" / "mcp" / f"{spec.agent}.json"
        write_mcp_config(mcp_config, self._server, self._token_script(spec))
        return LaunchPlan(self.argv(session_id, resume, mcp_config, spec.system_prompt, self.settings_flags(spec)), self.env(variant, spec.agent), spec.directory)

    def open_session(self, spec: RunSpec, session_id: str | None = None, resume: bool = False) -> HarnessSession:
        """A live process for the caller to converse with; `spec.prompt` is not sent."""
        if resume and session_id is None:
            raise ValueError("resuming needs the session id")
        sid = session_id or str(uuid.uuid4())
        plan = self.plan(spec, sid, resume)
        process = HarnessProcess(plan.argv, plan.env, plan.cwd, self._drain_seconds)
        return ClaudeSession(process, user_message, sid, self._grace)

    def _launch(self, spec: RunSpec, session_id: str, resume: bool) -> ProcessRun:
        plan = self.plan(spec, session_id, resume)
        process = HarnessProcess(plan.argv, plan.env, plan.cwd, self._drain_seconds)
        return ProcessRun(process, ClaudeProtocol(spec.prompt, session_id, self._clock, SessionBaseline(self._root / "state" / "usage" / f"{session_id}.json")), session_id, self._grace, self._masker)
