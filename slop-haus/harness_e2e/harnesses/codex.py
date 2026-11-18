from __future__ import annotations

import base64
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

PROVIDER = "scripted"
# A name Codex has no model metadata for, which keeps its tools plain (see support/codex_app_server.py).
MODEL = "scripted-model-1"
# The provider's `env_key`: where the owner puts a key in run.env or secrets.env, as for pi.
KEY_VARIABLE = "SCRIPTED_MODEL_API_KEY"
KEY = "scripted-model-not-a-real-key"
# What the board records for a Codex worker's usage (usage/codex.py's constant), whichever provider the config names;
# a Remote Control records the config's own (`scripted`).
RECORDED_PROVIDER = "openai"


def _jwt(claims: dict[str, Any]) -> str:
    def part(data: dict[str, Any]) -> str:
        return base64.urlsafe_b64encode(json.dumps(data).encode()).rstrip(b"=").decode()

    return f"{part({'alg': 'none', 'typ': 'JWT'})}.{part(claims)}.{part({})}"


class Codex:
    name = "codex"
    # The shell tool's name in a request's tool list.
    bash_tool = "exec_command"
    needs_tty = False
    provider = RECORDED_PROVIDER
    thinking = "low"

    def mcp_tool(self, tool: str) -> str:
        # Codex offers slop-haus's tools as the children of the `mcp__slop_haus` namespace tool, under their own names.
        return tool

    def harness_config(self, model: str) -> dict[str, str]:
        # The board's Codex settings: a model and a reasoning effort (no provider or thinking key).
        return {"model": model, "effort": self.thinking}

    def prepare(self, home: Path, workspace: Path, model_server_url: str) -> None:
        # Where a codex started with no CODEX_HOME has its state: ~/.codex.
        self.write_codex_home(home / ".codex", model_server_url)

    def prepare_remote_control(self, codex_home: Path, workspace: Path, model_server_url: str) -> None:
        self.write_codex_home(codex_home, model_server_url)

    @staticmethod
    def write_codex_home(codex_home: Path, model_server_url: str) -> None:
        """A CODEX_HOME with the scripted model as its provider and the ChatGPT login the supervisor wants.

        `wire_api = "responses"` is the only wire API a custom provider has; the entry also keeps Codex from opening a
        websocket to api.openai.com first, which only the built-in provider does."""
        codex_home.mkdir(mode=0o700, parents=True, exist_ok=True)
        (codex_home / "config.toml").write_text(
            "\n".join(
                [
                    f'model = "{MODEL}"',
                    f'model_provider = "{PROVIDER}"',
                    "check_for_update_on_startup = false",
                    "",
                    # With a ChatGPT login Codex reaches for chatgpt.com (the apps and plugin catalogues) and the
                    # container has no route there: each attempt waits out a timeout, which adds half a minute to a
                    # turn's start. None of it is what the scenarios are about.
                    "[features]",
                    "apps = false",
                    "plugins = false",
                    "remote_plugin = false",
                    "plugin_sharing = false",
                    "",
                    "[analytics]",
                    "enabled = false",
                    "",
                    "[feedback]",
                    "enabled = false",
                    "",
                    f"[model_providers.{PROVIDER}]",
                    f'name = "{PROVIDER}"',
                    f'base_url = "{model_server_url}/v1"',
                    f'env_key = "{KEY_VARIABLE}"',
                    'wire_api = "responses"',
                    "",
                ]
            )
        )
        # A login that is a login to the supervisor (CodexHarness.missing_credentials) and never used: the provider
        # above takes the key from the environment. The tokens only have to parse.
        (codex_home / "auth.json").write_text(
            json.dumps(
                {
                    "auth_mode": "chatgpt",
                    "tokens": {
                        "id_token": _jwt({"email": "scripted@example.invalid", "https://api.openai.com/auth": {"chatgpt_plan_type": "plus"}}),
                        "access_token": "scripted-access-token",
                        "refresh_token": "scripted-refresh-token",
                        "account_id": "scripted-account",
                    },
                    "last_refresh": datetime.now(timezone.utc).isoformat(),
                }
            )
        )

    @staticmethod
    def login_env(model_server_url: str) -> dict[str, str]:
        """The provider's key, for run.env."""
        return {KEY_VARIABLE: KEY}

    def env(self, model_server_url: str) -> dict[str, str]:
        return {**self.login_env(model_server_url), **self.hardening_env()}

    @staticmethod
    def hardening_env() -> dict[str, str]:
        # Codex's update check, analytics and feedback are config.toml's (write_codex_home): it has no variables for them.
        return {}

    def thinking_in(self, request_body: dict[str, Any]) -> str:
        reasoning = request_body.get("reasoning")
        return str(reasoning.get("effort", "")) if isinstance(reasoning, dict) else ""
