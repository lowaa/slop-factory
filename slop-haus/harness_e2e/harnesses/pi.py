from __future__ import annotations

import json
from pathlib import Path
from typing import Any

PROVIDER = "scripted"
# What models.json names as the provider's key, so the key itself can be given
# the way the owner gives one: in the environment (run.env).
KEY_VARIABLE = "SCRIPTED_MODEL_API_KEY"
KEY = "scripted-model-not-a-real-key"
# The model ids the scenarios use: pi refuses one its provider does not list.
MODELS = ["scripted-model-1", "scripted-model-2"]


class Pi:
    name = "pi"
    bash_tool = "bash"
    needs_tty = True
    provider = PROVIDER
    thinking = "low"

    def mcp_tool(self, tool: str) -> str:
        # The extension registers each server tool under its own name.
        return tool

    def harness_config(self, model: str) -> dict[str, str]:
        # All three of the board's pi settings, so the assertions prove each
        # of harness-config.py's flags reaches pi through the launcher.
        return {"provider": PROVIDER, "model": model, "thinking": self.thinking}

    def prepare(self, home: Path, workspace: Path, model_server_url: str) -> None:
        # Where a pi started with no PI_CODING_AGENT_DIR has its agent directory:
        # the test user's ~/.pi/agent, as a person's is.
        self.write_agent_dir(home / ".pi" / "agent", model_server_url, apiKey=KEY)

    def prepare_remote_control(self, agent_dir: Path, workspace: Path, model_server_url: str) -> None:
        """The Remote Control's own agent directory (`PI_CODING_AGENT_DIR`),
        which starts empty: the provider is configured there and its key is
        left to run.env (`login_env`), both as the owner does it."""
        self.write_agent_dir(agent_dir, model_server_url, apiKey=f"${KEY_VARIABLE}")

    @staticmethod
    def write_agent_dir(agent_dir: Path, model_server_url: str, apiKey: str) -> None:
        agent_dir.mkdir(parents=True, exist_ok=True)
        # A custom provider over OpenAI chat completions. Any key will do; it
        # is what makes the model available to pi.
        (agent_dir / "models.json").write_text(
            json.dumps(
                {
                    "providers": {
                        PROVIDER: {
                            "baseUrl": f"{model_server_url}/v1",
                            "api": "openai-completions",
                            "apiKey": apiKey,
                            "models": [
                                {
                                    "id": model,
                                    "name": model,
                                    # pi sends no reasoning effort for a model that does not reason, which
                                    # would leave the thinking setting with nothing to show.
                                    "reasoning": True,
                                    "input": ["text"],
                                    "contextWindow": 128000,
                                    "maxTokens": 8192,
                                    "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
                                }
                                for model in MODELS
                            ],
                        }
                    }
                }
            )
        )
        (agent_dir / "settings.json").write_text(json.dumps({"enableInstallTelemetry": False, "collapseChangelog": True}))

    @staticmethod
    def login_env(model_server_url: str) -> dict[str, str]:
        """The provider's key, for run.env."""
        return {KEY_VARIABLE: KEY}

    def env(self, model_server_url: str) -> dict[str, str]:
        return self.hardening_env()

    @staticmethod
    def hardening_env() -> dict[str, str]:
        # No update check, no model catalog refresh, no install report.
        return {"PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"}

    def thinking_in(self, request_body: dict[str, Any]) -> str:
        return str(request_body.get("reasoning_effort", ""))
