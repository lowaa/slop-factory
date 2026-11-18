from __future__ import annotations

import json
from pathlib import Path
from typing import Any


class ClaudeCode:
    name = "claude"
    bash_tool = "Bash"
    needs_tty = False
    provider = ""
    # Claude Code's config here is the model alone; there is no thinking setting to see.
    thinking = ""

    def mcp_tool(self, tool: str) -> str:
        return f"mcp__slop-haus__{tool}"

    def harness_config(self, model: str) -> dict[str, str]:
        return {"model": model}

    def prepare(self, home: Path, workspace: Path, model_server_url: str) -> None:
        # Claude Code keeps its state in ~/.claude.json, where it records the
        # answers below.
        self.answer_first_run_prompts(home, workspace)

    @staticmethod
    def answer_first_run_prompts(state_dir: Path, workspace: Path) -> None:
        # What a person does once by answering Claude Code's first-run prompts:
        # onboarding, and trusting this checkout. Untrusted, Claude Code runs no
        # hooks, and a `claude -p` run does not count as trusting it (the setup
        # script says as much).
        # Kept alongside any directory answered before: a nested workspace has two.
        path = state_dir / ".claude.json"
        projects = json.loads(path.read_text())["projects"] if path.exists() else {}
        projects[str(workspace)] = {"hasTrustDialogAccepted": True}
        path.write_text(json.dumps({"hasCompletedOnboarding": True, "projects": projects}))

    def prepare_remote_control(self, config_dir: Path, workspace: Path, model_server_url: str) -> None:
        # The Remote Control's Claude Code runs with CLAUDE_CONFIG_DIR set, and
        # then keeps its state in that directory's own .claude.json: the
        # interactive login the owner does there answers these
        # prompts as well.
        config_dir.mkdir(parents=True, exist_ok=True)
        self.answer_first_run_prompts(config_dir, workspace)

    def login_env(self, model_server_url: str) -> dict[str, str]:
        """Where the scripted model is, and the API key that logs Claude Code in
        to it (the owner sets ANTHROPIC_API_KEY instead of logging in)."""
        return {
            "ANTHROPIC_BASE_URL": model_server_url,
            # Claude Code wants a key to run; the scripted model takes any.
            "ANTHROPIC_API_KEY": "sk-ant-scripted-model-not-a-real-key",
        }

    def env(self, model_server_url: str) -> dict[str, str]:
        return {**self.login_env(model_server_url), **self.hardening_env()}

    @staticmethod
    def hardening_env() -> dict[str, str]:
        """Switches off the harness's outward traffic."""
        return {
            "DISABLE_AUTOUPDATER": "1",
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
            "DISABLE_TELEMETRY": "1",
            "DISABLE_ERROR_REPORTING": "1",
            # After a turn Claude Code may ask the model, on a fork of the conversation,
            # what the user might type next. That is a request the script has no turn
            # for, and it comes or not from one run to the next.
            "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false",
        }

    def thinking_in(self, request_body: dict[str, Any]) -> str:
        return ""
