from __future__ import annotations

import os
import subprocess
from collections.abc import Callable, Mapping
from pathlib import Path

Spawn = Callable[[list[str], Path, Mapping[str, str]], int]


def _spawn(argv: list[str], cwd: Path, env: Mapping[str, str]) -> int:
    return subprocess.run(argv, cwd=cwd, env=dict(env), check=False).returncode


class GitAccess:
    """Runs a slot's setup commands so that git never stops at a credentials prompt.

    Which protocol the clones use is decided once, in layout._clone_protocol, and handed to the update
    script's sync-repos call; nothing here rewrites a command.
    """

    def __init__(self, env: Mapping[str, str], spawn: Spawn = _spawn) -> None:
        self._env = env
        self._spawn = spawn

    def __call__(self, argv: list[str], cwd: Path) -> int:
        return self._spawn(argv, cwd, {**os.environ, **self._env, "GIT_TERMINAL_PROMPT": "0"})
