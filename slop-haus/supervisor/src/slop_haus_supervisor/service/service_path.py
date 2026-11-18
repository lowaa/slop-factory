from __future__ import annotations

import os
import shutil
import sys
from collections.abc import Callable
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.envfile import EnvFileError
from slop_haus_supervisor.workspace.layered_env import EnvKind, Layer, LayerFile, LayerRef
from slop_haus_supervisor.workspace.layout import Layout

LAUNCHD_PATH = "/usr/bin:/bin:/usr/sbin:/sbin"
# What systemd gives a user manager when `show-environment` cannot be asked.
SYSTEMD_FALLBACK_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"


class ServicePath:
    """The PATH the supervisor has when a service manager starts it, not the one in the shell running setup.

    A login shell has ~/.local/bin, nvm and so on; a systemd user manager or launchd does not, so a tool found
    by setup can still be missing at start-up. The supervisor puts run.env's PATH in front of the launcher's
    (`run_env.apply`), and so does this.
    """

    def __init__(
        self,
        run_env: Path,
        runner: ProcessRunner,
        home: Path,
        platform: str = sys.platform,
        which: Callable[[str, str], str | None] = lambda name, path: shutil.which(name, path=path),
    ) -> None:
        self._run_env = run_env
        self._runner = runner
        self._home = home
        self._platform = platform
        self._which = which

    def launcher_path(self) -> str | None:
        """The manager's own PATH, or None on a platform whose launcher is unknown (the shell's is then the best guess)."""
        if self._platform == "darwin":
            return LAUNCHD_PATH
        if self._platform.startswith("linux"):
            return self._systemd_path()
        return None

    def _systemd_path(self) -> str:
        try:
            done = self._runner.run(["systemctl", "--user", "show-environment"], self._home, 10)
        except (OSError, TimeoutError):
            return SYSTEMD_FALLBACK_PATH
        if done.returncode == 0:
            for line in done.stdout.splitlines():
                if line.startswith("PATH="):
                    return line.removeprefix("PATH=")
        return SYSTEMD_FALLBACK_PATH

    def run_env_path(self) -> str:
        return envfile.read(self._run_env).get("PATH", "")

    def effective(self, fallback: str) -> str:
        """run.env's PATH in front of the launcher's; `fallback` stands in for a launcher we know nothing about."""
        base = self.launcher_path()
        parts = [self.run_env_path(), fallback if base is None else base]
        return os.pathsep.join(p for p in parts if p)

    def find(self, name: str, fallback: str) -> str | None:
        return self._which(name, self.effective(fallback))



def prepend_to_run_env(layout: Layout, folder: str) -> bool:
    """Puts `folder` in front of the base run.env's PATH; False when it is already there. Absolute folders only."""
    if not os.path.isabs(folder) or os.pathsep in folder:
        raise EnvFileError(f"{folder!r} is not an absolute directory; run.env PATH takes absolute paths only")
    target = LayerFile(layout, LayerRef(Layer.BASE), EnvKind.RUN)
    added = False

    def change(text: str) -> str:
        nonlocal added
        current = envfile.parse(text).get("PATH", "")
        if folder in current.split(os.pathsep):
            return text
        added = True
        return envfile.with_value(text, "PATH", os.pathsep.join(p for p in (folder, current) if p))

    # Read and written under the file's lock, so two setups adding different folders keep both.
    target.edit_path(change)
    return added
