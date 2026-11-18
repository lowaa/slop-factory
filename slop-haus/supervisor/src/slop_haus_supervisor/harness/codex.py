"""OpenAI Codex, run headless: `codex app-server` speaking JSON-RPC, one prompt to its `turn/completed`."""

from __future__ import annotations

import os
import shutil
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.base import AgentEnv, LaunchPlan, RunHandle, RunSpec, SessionNotFound
from slop_haus_supervisor.harness.codex_login import CodexLogin
from slop_haus_supervisor.harness.codex_protocol import CodexProtocol, is_uuid
from slop_haus_supervisor.harness.denied import DENIED_TOOLS
from slop_haus_supervisor.harness.mcp_config import TOOL_CALL_TIMEOUT_MS
from slop_haus_supervisor.harness.process import HarnessProcess, ProcessRun
from slop_haus_supervisor.harness.variant import InvalidVariant, parse_variant
from slop_haus_supervisor.ports import Clock, SystemClock
from slop_haus_supervisor.service.run_env import RUN_SETTINGS
from slop_haus_supervisor.usage.billing import codex_login_billing_mode, read_codex_login

# The CLI the protocol code was written and its schema checked against (tests/harness/test_codex_schema.py).
CODEX_VERSION = "0.165.0"
START_TIMEOUT = 60.0
MCP_PREFIX = "mcp__slop-haus__"
# ChatGPT login only: a key in the environment would override it, and meter what the subscription covers.
API_KEY_ENV = ("OPENAI_API_KEY", "CODEX_API_KEY")


def denied_mcp_tools() -> list[str]:
    """DENIED_TOOLS in Codex's spelling: slop-haus's own tools, by their bare names."""
    return [t.removeprefix(MCP_PREFIX) for t in DENIED_TOOLS if t.startswith(MCP_PREFIX)]


class CodexRun(ProcessRun):
    """A run whose session id is the thread id Codex assigns, known once `thread/start` has answered."""

    def __init__(self, process: HarnessProcess, protocol: CodexProtocol, grace: float) -> None:
        self._codex = protocol
        super().__init__(process, protocol, "", grace)

    @property
    def session_id(self) -> str:
        return self._codex.thread_id


class CodexHarness:
    name = "codex"

    def __init__(
        self,
        root: Path,
        server: str,
        *,
        command: Sequence[str] = ("codex",),
        environ: Mapping[str, str] | None = None,
        credentials: Mapping[str, str] | None = None,
        agent_env: AgentEnv | None = None,
        clock: Clock | None = None,
        grace: float = 5.0,
        start_timeout: float = START_TIMEOUT,
        login: CodexLogin | None = None,
    ) -> None:
        self._root = root
        self._server = server
        self._command = list(command)
        self._environ = environ if environ is not None else os.environ
        self._credentials = dict(credentials or {})
        self._agent_env = agent_env
        self._clock = clock or SystemClock()
        self._grace = grace
        self._start_timeout = start_timeout
        self._login = login

    def check_installed(self) -> None:
        if shutil.which(self._command[0]) is None:
            raise FileNotFoundError(f"{self._command[0]} is not installed")

    def config_dir(self, root: Path) -> Path:
        return root / "harness" / "codex"

    def missing_credentials(self) -> str:
        """ChatGPT login only: the `auth.json` that `codex login` writes, not an API key."""
        home = self.config_dir(self._root)
        login = read_codex_login(home)
        if login.auth_mode == "chatgpt" and login.has_tokens:
            return ""
        return f"a ChatGPT login: on the supervisor's host, as the user that runs it, run `slop-haus harness login codex` (it sets `CODEX_HOME={home}`)"

    def start(self, spec: RunSpec) -> RunHandle:
        run = self._launch(spec, "")
        run.await_start(self._start_timeout)
        return run

    def resume(self, spec: RunSpec, session_id: str) -> RunHandle:
        # `codex exec resume <name>` starts a new thread for anything but an id; thread/resume takes only ids, and a
        # recorded value that is not one cannot be a conversation of ours.
        if not is_uuid(session_id):
            raise SessionNotFound(session_id)
        run = self._launch(spec, session_id)
        run.await_start(self._start_timeout)
        if run.session_missing:
            run.kill()
            raise SessionNotFound(session_id)
        return run

    def thread_params(self, spec: RunSpec) -> dict[str, Any]:
        variant = parse_variant(spec.variant)
        if variant.provider or variant.thinking or variant.subagent_model:
            raise InvalidVariant("provider, thinking and subagent_model are not codex settings")
        config: dict[str, Any] = {
            # Codex's own sub-agents would leave the supervisor's accounting.
            "features.multi_agent": False,
            "features.goals": False,
            "web_search": "disabled",
            # The repo's contract is a CLAUDE.md; Codex reads AGENTS.md first and this only as a fallback.
            "project_doc_fallback_filenames": ["CLAUDE.md"],
            # Claiming belongs to the supervisor. The credential for the server is the MCP auth part's.
            "mcp_servers.slop-haus.url": self._server.rstrip("/") + "/mcp",
            "mcp_servers.slop-haus.disabled_tools": denied_mcp_tools(),
            # Codex's own default is shorter than the 120 s the other harnesses allow a slop-haus call.
            "mcp_servers.slop-haus.tool_timeout_sec": TOOL_CALL_TIMEOUT_MS // 1000,
        }
        if variant.effort:
            config["model_reasoning_effort"] = variant.effort
        params: dict[str, Any] = {
            "cwd": str(spec.directory),
            # The equivalent of Claude's bypassPermissions: the agent has the user's permissions.
            "approvalPolicy": "never",
            "sandbox": "danger-full-access",
            "config": config,
        }
        if spec.system_prompt:
            params["developerInstructions"] = spec.system_prompt
        if variant.model:
            params["model"] = variant.model
        return params

    def home_for(self, agent: str) -> Path:
        """With a shared login each agent runs on its own copy, so Codex never presents the master's refresh token itself."""
        if self._login is None or not agent:
            return self.config_dir(self._root)
        return self.config_dir(self._root).with_name("codex-slots") / agent

    def env(self, agent: str = "") -> dict[str, str]:
        env = {k: v for k, v in self._environ.items() if not k.startswith("SLOP_HAUS_") or k in RUN_SETTINGS}
        env.update(self._credentials)
        if self._agent_env is not None and agent:
            env.update(self._agent_env(agent))
        for key in API_KEY_ENV:
            env.pop(key, None)
        env["SLOP_HAUS_HOST_SESSION"] = "1"
        env["CODEX_HOME"] = str(self.home_for(agent))
        return env

    def plan(self, spec: RunSpec) -> LaunchPlan:
        """Validates the spec and prepares the config directory the process needs."""
        self.thread_params(spec)
        self.config_dir(self._root).mkdir(mode=0o700, parents=True, exist_ok=True)
        if self._login is not None and spec.agent:
            # Before every launch, under the login's lock: a copy is never older than the refresh threshold.
            self._login.refresh_if_due([self.home_for(spec.agent)])
        return LaunchPlan(self._command + ["app-server"], self.env(spec.agent), spec.directory)

    def _launch(self, spec: RunSpec, resume_id: str) -> CodexRun:
        plan = self.plan(spec)
        protocol = CodexProtocol(spec.prompt, self._clock, self.thread_params(spec), resume_id, parse_variant(spec.variant).effort,
                                 codex_login_billing_mode(self.config_dir(self._root)))
        return CodexRun(HarnessProcess(plan.argv, plan.env, plan.cwd), protocol, self._grace)
