"""What a Codex steward may do: a read-only permission profile, a scrubbed shell environment, the tool allowlist.

Unlike Claude's Read/Grep/Glob, Codex reads files with its shell, so the shell is on. A read-only sandbox stops writes,
not reads, and the steward reads untrusted text, so the profile grants reads to named paths only: everything else on
the host (the steward's credentials, CODEX_HOME's login, secrets.env, other slots, $HOME) is simply not mounted.
`codex_probe.py` proves that at start-up. The slop-haus MCP credential (`http_headers_helper`) runs in the Codex
process, outside the shell's sandbox, so the confined side never holds it and no auth proxy is needed.
"""

from __future__ import annotations

import json
import os
import shutil
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any, cast

from slop_haus_supervisor.steward.guard import STEWARD_ALLOWED_TOOLS, STEWARD_BUILTIN_TOOLS

PROFILE = "steward"
SHELL_PATH = "/usr/bin:/bin"
MCP_PREFIX = "mcp__slop-haus__"
# System paths a toolchain (git, rg, sh) needs; ":minimal" is Codex's own minimal set.
SYSTEM_READ = (":minimal", "/usr", "/etc/gitconfig", "/etc/ld.so.cache", "/etc/alternatives")
# Files in a review checkout that hold credentials (install_repo_env writes the self-review key to `.env`).
REPO_SECRET_FILES = (".env",)


def allowed_mcp_tools() -> list[str]:
    """STEWARD_ALLOWED_TOOLS in Codex's spelling: the bare names of slop-haus's tools."""
    return [t.removeprefix(MCP_PREFIX) for t in STEWARD_ALLOWED_TOOLS if t.startswith(MCP_PREFIX) and t not in STEWARD_BUILTIN_TOOLS]


def toml_value(value: Any) -> str:
    """A TOML value for a `-c key=value` flag; strings use JSON escaping, which TOML basic strings accept."""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, Mapping):
        items = cast(Mapping[object, object], value)
        return "{" + ", ".join(f"{json.dumps(str(k))} = {toml_value(v)}" for k, v in items.items()) + "}"
    if isinstance(value, (list, tuple)):
        return "[" + ", ".join(toml_value(v) for v in cast(Sequence[object], value)) + "]"
    return json.dumps(value)


@dataclass(frozen=True)
class CodexStewardPolicy:
    """`review_roots` are the checkouts the steward reviews; `scratch` is its empty working directory."""

    codex_executable: Path
    scratch: Path
    review_roots: Sequence[Path] = ()

    @classmethod
    def for_command(cls, command: Sequence[str], scratch: Path, review_roots: Sequence[Path] = ()) -> CodexStewardPolicy:
        found = shutil.which(command[0])
        if found is None:
            raise FileNotFoundError(f"{command[0]} is not installed")
        return cls(Path(os.path.realpath(found)), scratch, tuple(review_roots))

    def readable(self) -> dict[str, str]:
        paths: dict[str, str] = {p: "read" for p in SYSTEM_READ if p.startswith(":") or Path(p).exists()}
        # The sandbox helper re-executes codex inside the namespace, so its own directory must be visible.
        paths[str(self.codex_executable.parent)] = "read"
        paths[str(self.scratch)] = "read"
        for root in self.review_roots:
            paths[str(root)] = "read"
            for name in REPO_SECRET_FILES:
                paths[str(root / name)] = "none"
        return paths

    def config(self) -> dict[str, Any]:
        return {
            "default_permissions": PROFILE,
            f"permissions.{PROFILE}.filesystem": self.readable(),
            f"permissions.{PROFILE}.network.enabled": False,
            # Nothing of the host's environment reaches a command: no tokens, keys or CODEX_* auth.
            "shell_environment_policy.inherit": "none",
            # With nothing inherited the shell has no PATH, so `git` and `cat` by name are "command not found".
            "shell_environment_policy.set": {"PATH": SHELL_PATH},
            "features.shell_tool": True,
            "features.multi_agent": False,
            "features.goals": False,
            "features.apply_patch_freeform": False,
            "features.view_image": False,
            "features.js_repl": False,
            "web_search": "disabled",
            "mcp_servers.slop-haus.enabled_tools": allowed_mcp_tools(),
        }

    def flags(self) -> list[str]:
        """The sandbox half of the config as process-level `-c` flags, which `command/exec` (the probe) resolves from.

        MCP entries stay on the thread: given alone, `enabled_tools` makes a server with no transport, which codex rejects.
        """
        flags: list[str] = []
        for key, value in self.config().items():
            if key.startswith("mcp_servers."):
                continue
            flags += ["-c", f"{key}={toml_value(value)}"]
        return flags
