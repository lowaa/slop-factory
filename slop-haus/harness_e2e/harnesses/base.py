"""What a harness adds to the suite: an adapter, and an image (Dockerfile).

Scenarios speak to adapters, never to a harness directly, so a scenario that
runs on Claude Code runs on the next harness once it has an adapter.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Protocol


class HarnessAdapter(Protocol):
    # The harness's name, and the key of its settings in slop-haus's harness config.
    name: str
    # Whether the harness needs a terminal to run its full interactive loop.
    # pi replaces its session between tickets only in its TUI, so the suite
    # starts it on a pty and has to stop it itself: it never exits on its own.
    needs_tty: bool
    # The provider name the board records for a session, if the harness has one.
    provider: str
    # The thinking level the harness config sets, and how it shows in a request
    # to the model: what proves the setting reached the harness.
    thinking: str

    def mcp_tool(self, tool: str) -> str:
        """The name the harness gives a slop-haus MCP tool in its tool list."""
        ...

    @property
    def bash_tool(self) -> str: ...

    def thinking_in(self, request_body: dict[str, Any]) -> str: ...

    def harness_config(self, model: str) -> dict[str, str]:
        """The board's harness config settings for this harness that select
        the scripted model: what the launcher must turn into the harness's own
        model flags or variables."""
        ...

    def prepare(self, home: Path, workspace: Path, model_server_url: str) -> None:
        """What makes a headless first run work: logged in, workspace trusted,
        and where it has the scripted model server's provider, if that is a
        file rather than environment."""
        ...

    def env(self, model_server_url: str) -> dict[str, str]:
        """Points the harness at the scripted model, logs it in with a dummy
        credential, and switches off its outward traffic."""
        ...

    def prepare_remote_control(self, state_dir: Path, workspace: Path, model_server_url: str) -> None:
        """The harness's own configuration directory under the Remote Control's
        `.slop-haus/conductor`, set up the way the owner does once there."""
        ...

    def login_env(self, model_server_url: str) -> dict[str, str]:
        """What the owner puts in run.env to log the harness in."""
        ...

    def hardening_env(self) -> dict[str, str]:
        """Switches off the harness's outward traffic."""
        ...
