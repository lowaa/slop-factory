from __future__ import annotations

import re
from pathlib import Path

from slop_haus_supervisor.remote_control.host.prompt import HOST_PREFIX


class PiErrors:
    """What the owner is told when pi fails over its provider, model or key.

    pi runs with the host's own agent directory, so a provider the owner set up in ~/.pi/agent is
    unknown to it, and pi's own words do not say so. The wording is pi 0.99.1's (its CLI, and the
    RPC errors for a prompt it refuses); a message pi words differently just gets no hint. Not
    verified against a running pi: the "No API key for <provider>/<model>" variant, which is a
    throw in its source rather than something seen on stderr or RPC.
    """

    NEEDS_PROVIDER = re.compile(
        r'Unknown provider "|No models? available|No model selected|\bno API key\b', re.IGNORECASE)
    # pi's notice that --session-id is creating the session, which is every conversation's first
    # start; it reads like the cause of what follows.
    FIRST_START = re.compile(
        r"^Warning: No project session found with id '[^'\n]*'; creating a new session with that id\.$")

    def __init__(self, agent_dir: Path) -> None:
        self.agent_dir = agent_dir

    def tidy(self, stderr: str) -> str:
        """pi's stderr without the one benign line, every other line kept."""
        return "\n".join(line for line in stderr.split("\n") if not self.FIRST_START.match(line.strip()))

    def explain(self, text: str) -> str:
        """text, with the hint appended when it is about the provider, model or key. Names paths and files, never their contents."""
        if not self.NEEDS_PROVIDER.search(text):
            return text
        return (
            f"{text}\n{HOST_PREFIX}hint: this session's pi uses {self.agent_dir}, with its own models.json, "
            "auth.json and API key variables (run.env), not the ones in your own ~/.pi/agent. "
            "Log in or configure the provider there."
        )
