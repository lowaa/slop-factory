from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.variant import Variant

BACKGROUND_TASKS = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"


class RemoteControlClaude(ClaudeHarness):
    """Claude Code as the Remote Control has always started it: its login and settings live in
    <state>/claude-config, where a person has already logged it in."""

    def config_dir(self, root: Path) -> Path:
        return root / "claude-config"

    def env(self, variant: Variant, agent: str = "") -> dict[str, str]:
        env = super().env(variant, agent)
        # A worker's run must not leave background work holding the integration lock; the owner's
        # own session may run a dev server or a watcher, and is stopped as a whole when it ends.
        if BACKGROUND_TASKS in self._environ:
            env[BACKGROUND_TASKS] = self._environ[BACKGROUND_TASKS]
        else:
            env.pop(BACKGROUND_TASKS, None)
        return env
