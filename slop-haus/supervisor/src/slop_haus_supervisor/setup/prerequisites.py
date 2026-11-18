from __future__ import annotations

import shutil
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.tool_check import ToolChecker
from slop_haus_supervisor.ports import ProcessRunner

LOW_DISK_BYTES = 30 * 1024**3


@dataclass(frozen=True)
class Finding:
    name: str
    ok: bool
    detail: str
    # A warning is reported and does not stop setup.
    blocking: bool = True
    # The directory that would make a tool the service cannot see findable: it is on the shell's PATH only.
    path_fix: str = ""


class Prerequisites:
    """What a worker host needs before slots are worth adding, each with the fix named when it is missing."""

    def __init__(
        self,
        runner: ProcessRunner,
        harness: str,
        env: Mapping[str, str],
        home: Path,
        where: Path,
        which: Callable[[str], str | None] = shutil.which,
        service_which: Callable[[str], str | None] | None = None,
        free_bytes: Callable[[Path], int] = lambda path: shutil.disk_usage(path).free,
    ) -> None:
        self._runner = runner
        self._harness = harness
        self._env = env
        self._home = home
        self._where = where
        self._which = which
        # Where the supervisor will look once a service manager starts it; None judges by the shell's PATH alone.
        self._service_which = service_which or which
        self._tools = ToolChecker(self._service_which, which)
        self._free_bytes = free_bytes
        self._default = registry.find(harness) or registry.HARNESS_ENTRIES[0]

    def check(self) -> list[Finding]:
        return [self._uv(), self._harness_cli(), *self._other_harnesses(), self._git(), self._docker(), self._gh(), self._disk()]

    def _exe(self, name: str) -> str:
        """What to run for a probe: the executable the service would find, which the shell's PATH may not have."""
        return self._service_which(name) or name

    def _tool(self, name: str, missing: str, blocking: bool = True) -> Finding:
        """Found where the service will look, or named with why not: a fix for a tool nowhere, a PATH entry for one only the shell has."""
        lookup = self._tools.lookup(name)
        if lookup.found:
            return Finding(name, True, "found")
        if lookup.path_fix:
            return Finding(
                name, False, f"is at {lookup.login_path}, which the service won't see: add {lookup.path_fix} to run.env PATH", blocking, path_fix=lookup.path_fix
            )
        return Finding(name, False, missing, blocking)

    def _uv(self) -> Finding:
        return self._tool("uv", "install it from https://docs.astral.sh/uv/getting-started/installation/")

    def _harness_cli(self) -> Finding:
        entry = self._default
        return self._tool(entry.name, entry.install_fix)

    def _other_harnesses(self) -> list[Finding]:
        """A harness the board can assign a slot is checked too, once the shell has it: not installing one is fine."""
        return [
            self._tool(entry.name, entry.install_fix, blocking=False)
            for entry in registry.HARNESS_ENTRIES
            if entry.worker and entry.name != self._default.name and self._which(entry.name)
        ]

    def _git(self) -> Finding:
        tool = self._tool("git", "install git")
        if not tool.ok:
            return tool
        if self._env.get("SSH_AUTH_SOCK") or self._ssh_key() or self._credential_helper():
            return Finding("git push", True, "an ssh key, an ssh-agent or a credential helper is configured")
        return Finding(
            "git push",
            False,
            "workers push without a person at the keyboard: add an ssh key without a passphrase (or an ssh-agent), "
            "or for https remotes a credential helper with the token stored (`git config --global credential.helper store`)",
        )

    def _gh(self) -> Finding:
        return self._tool("gh", "install the GitHub CLI if workers open pull requests with it", blocking=False)

    def _ssh_key(self) -> bool:
        return any(not key.name.endswith(".pub") for key in self._home.glob(".ssh/id_*"))

    def _credential_helper(self) -> bool:
        done = self._runner.run([self._exe("git"), "config", "--get-all", "credential.helper"], self._home)
        return done.returncode == 0 and bool(done.stdout.strip())

    def _docker(self) -> Finding:
        tool = self._tool("docker", "install docker: workers run tests and builds in containers")
        if not tool.ok:
            return tool
        done = self._runner.run([self._exe("docker"), "info"], self._home, 30)
        if done.returncode == 0:
            return Finding("docker", True, "reachable")
        return Finding("docker", False, "installed but not reachable: start the daemon, and add this user to the docker group")

    def _disk(self) -> Finding:
        free = self._free_bytes(self._where)
        gib = free / 1024**3
        if free >= LOW_DISK_BYTES:
            return Finding("disk", True, f"{gib:.0f} GB free")
        return Finding("disk", False, f"only {gib:.0f} GB free in {self._where}; slots' checkouts and images want about 30 GB", blocking=False)
