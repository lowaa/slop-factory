from __future__ import annotations

import os
import re
import shutil
import subprocess
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.fleet import login_failure
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.base import ConfiguredHarness
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import status
from slop_haus_supervisor.workspace.envfile import EnvFile
from slop_haus_supervisor.workspace.layout import Layout

# What `claude setup-token` prints; matched rather than read from a file because the command stores nothing itself.
_TOKEN = re.compile(r"sk-ant-[A-Za-z0-9_-]{20,}")


class LoginError(cfg.ConfigError):
    pass


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    # Only set when the caller asked for the output to be captured.
    stdout: str = ""


class LoginCommands(Protocol):
    def run(self, argv: list[str], env: Mapping[str, str], cwd: Path, capture: bool) -> CommandResult:
        """Runs `argv` on the person's terminal; `capture` takes its stdout instead, stdin and stderr staying theirs."""
        ...


class TerminalCommands:
    def run(self, argv: list[str], env: Mapping[str, str], cwd: Path, capture: bool) -> CommandResult:
        done = subprocess.run(argv, env=dict(env), cwd=cwd, stdout=subprocess.PIPE if capture else None, text=True, check=False)
        return CommandResult(done.returncode, done.stdout or "")


@dataclass(frozen=True)
class HarnessStatus:
    name: str
    # Whether a slot of this root runs on the harness.
    used: bool
    # What is missing, as the harness words it; "" when it can log in.
    missing: str

    def line(self) -> str:
        if self.missing:
            state = f"MISSING: {self.missing}"
        else:
            state = "logged in"
        return f"  {self.name}: {state}" + ("" if self.used else " (not used by any slot)")


@dataclass(frozen=True)
class LoginOutcome:
    ok: bool
    lines: tuple[str, ...]


class HarnessLogins:
    """`harness login` and `harness status` for one root.

    Everything a harness needs is data on its registry entry. A login is judged only through
    `missing_credentials()`: the files the harness writes are never opened here.
    """

    def __init__(
        self,
        layout: Layout,
        build: Callable[[str], ConfiguredHarness],
        commands: LoginCommands,
        ask_hidden: Callable[[str], str],
        say: Callable[[str], None] = print,
        environ: Mapping[str, str] | None = None,
        euid: int | None = None,
        root_owner: int | None = None,
    ) -> None:
        self._layout = layout
        self._build = build
        self._commands = commands
        self._ask_hidden = ask_hidden
        self._say = say
        self._environ = os.environ if environ is None else environ
        self._euid = os.geteuid() if euid is None else euid
        self._root_owner = root_owner

    def _used(self) -> str:
        return cfg.load(self._layout.config_file).harness

    def statuses(self) -> list[HarnessStatus]:
        used = self._used()
        return [HarnessStatus(name, name == used, self._build(name).missing_credentials()) for name in registry.NAMES]

    def status_lines(self) -> list[str]:
        return ["  harness logins:", *(s.line() for s in self.statuses())]

    def logged_in(self, name: str) -> bool:
        return not self._build(name).missing_credentials()

    def _refuse_wrong_user(self) -> None:
        if self._euid == 0:
            raise LoginError("refusing to log a harness in as root: run this as the user that runs the supervisor")
        owner = self._layout.root.stat().st_uid if self._root_owner is None else self._root_owner
        if owner != self._euid:
            raise LoginError(f"{self._layout.root} belongs to another user (uid {owner}): run this as the user that runs the supervisor")

    def login(self, name: str) -> LoginOutcome:
        entry = registry.find(name)
        if entry is None:
            raise LoginError(f"unknown harness {name!r}: use one of {', '.join(registry.NAMES)}")
        self._refuse_wrong_user()
        spec = entry.login
        searched = self._environ.get("PATH")
        if shutil.which(spec.command[0], path=searched) is None:
            # The usual cause is a binary only on an interactive shell's PATH, which a service never reads.
            raise LoginError(
                f"{spec.command[0]} was not found on PATH (searched: {searched or 'PATH unset'}). {entry.install_fix} "
                f"A supervisor service's PATH comes from {self._layout.root / 'run.env'}, not ~/.bashrc."
            )
        folder = self._build(name).config_dir(self._layout.root)
        folder.mkdir(mode=0o700, parents=True, exist_ok=True)
        folder.chmod(0o700)
        env = dict(self._environ)
        if spec.dir_env:
            env[spec.dir_env] = str(folder)
        if spec.instructions:
            self._say(spec.instructions)
        result = self._commands.run(list(spec.command), env, folder, capture=bool(spec.token_key))
        if spec.token_key:
            self._keep_token(spec, result)
        elif result.returncode != 0:
            # An interrupted login can leave a half-written file that still reads as logged in.
            return LoginOutcome(False, (f"`{' '.join(spec.command)}` exited with {result.returncode}; {name} is not logged in",))
        return self._outcome(entry)

    def _keep_token(self, spec: registry.LoginSpec, result: CommandResult) -> None:
        match = _TOKEN.search(result.stdout)
        # What the command said is shown, minus the token, which only goes to secrets.env.
        shown = result.stdout.replace(match.group(0), "[token hidden]") if match else result.stdout
        if shown.strip():
            self._say(shown.rstrip())
        if result.returncode != 0:
            self._say(f"`{' '.join(spec.command)}` exited with {result.returncode}; nothing stored")
            return
        token = match.group(0) if match else self._ask_hidden(f"{spec.token_key} (hidden; the token the command printed, Enter to skip)")
        if len(token) < SecretMasker.MIN_LENGTH:
            self._say("no token captured; nothing stored")
            return
        EnvFile(self._layout.secrets_file).set(spec.token_key, token)
        self._say(f"stored {spec.token_key} in {self._layout.secrets_file}")

    def _outcome(self, entry: registry.HarnessEntry) -> LoginOutcome:
        missing = self._build(entry.name).missing_credentials()
        if missing:
            return LoginOutcome(False, (f"{entry.name} is still not logged in: {missing}",))
        lines = [f"{entry.name} is logged in"]
        running, _ = status.holder(self._layout.root / ".run.lock")
        if running and entry.login.restart:
            lines.append("the supervisor reads this login when it starts: `slop-haus stop`, then `slop-haus start`")
        if running and entry.name == self._used():
            paused = [p.slot for p in paused_state.read(self._layout.paused_file) if login_failure.SUBJECT in p.reason]
            if paused:
                after = " once it has restarted" if entry.login.restart else ""
                lines.append(f"slots paused on the login: {', '.join(paused)}; `slop-haus slot unpause --all`{after}")
        return LoginOutcome(True, tuple(lines))
