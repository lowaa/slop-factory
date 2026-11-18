"""The start-up self-test that gates a Codex steward: the sandbox must hold, or the steward does not run.

Every check runs a command through the steward's own policy (`command/exec` on an app-server started with the same
`-c` flags the steward gets), so what is proved is what the steward will run under. A check that cannot run counts as
a failure: a broken probe must never read as a safe sandbox.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import subprocess
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.steward.codex_policy import CodexStewardPolicy

CANARY = "slop-haus-probe-canary"
CANARY_PREFIX = ".slop-haus-probe-canary-"
ENV_CANARY = "STEWARD_PROBE_SECRET_TOKEN"
CONTROL = "control-ok"
# Variables a command's environment must not carry whatever their names' owner: credentials and Codex's login.
SECRET_ENV = re.compile(r"(TOKEN|KEY|SECRET|PASSWORD|CODEX_HOME|CODEX_API|OPENAI)", re.IGNORECASE)
NETWORK_TARGET = "1.1.1.1/53"
EXEC_TIMEOUT = 30.0


@dataclass(frozen=True)
class ExecResult:
    exit_code: int
    stdout: str
    stderr: str = ""


class SandboxExec(Protocol):
    def run(self, argv: Sequence[str], cwd: Path) -> ExecResult:
        """Runs argv under the steward's sandbox; raises when the sandbox could not be asked."""
        ...


class SandboxUnsafe(Exception):
    """The Codex steward must not start: `problems` say which protection did not hold."""

    def __init__(self, problems: Sequence[str]) -> None:
        super().__init__("; ".join(problems))
        self.problems = list(problems)


class CodexAppServerExec:
    """`command/exec` on a throwaway `codex app-server` carrying the steward's flags and a canary in its environment."""

    def __init__(self, command: Sequence[str], policy: CodexStewardPolicy, env: Mapping[str, str], timeout: float = EXEC_TIMEOUT) -> None:
        self._argv = [*command, *policy.flags(), "app-server"]
        self._env = {**env, ENV_CANARY: CANARY}
        self._timeout = timeout

    def run(self, argv: Sequence[str], cwd: Path) -> ExecResult:
        # One process per command keeps each check independent; the probe is a handful of short commands.
        with subprocess.Popen(self._argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, env=self._env, cwd=cwd) as proc:
            try:
                assert proc.stdin is not None and proc.stdout is not None
                requests = [
                    {"id": 1, "method": "initialize", "params": {"clientInfo": {"name": "slop-haus-probe", "version": "1"}, "capabilities": {}}},
                    {"method": "initialized"},
                    {"id": 2, "method": "command/exec", "params": {"command": list(argv), "cwd": str(cwd), "timeoutMs": int(self._timeout * 1000)}},
                ]
                for request in requests:
                    proc.stdin.write(json.dumps(request) + "\n")
                proc.stdin.flush()
                for line in proc.stdout:
                    record = json.loads(line)
                    if record.get("id") != 2:
                        continue
                    if "error" in record:
                        raise RuntimeError(str(record["error"].get("message")))
                    result = record["result"]
                    return ExecResult(int(result["exitCode"]), str(result.get("stdout", "")), str(result.get("stderr", "")))
                raise RuntimeError("codex app-server exited without answering command/exec")
            finally:
                proc.kill()


class SandboxProbe:
    """Refuses (SandboxUnsafe) unless the sandbox shows: reads confined, environment scrubbed, no writes, no network.

    `forbidden` are directories the steward must not read; a canary file is placed in each (those that exist) and
    must stay unreadable. `writable_roots` are where a write is tried besides the scratch dir.
    """

    def __init__(self, sandbox: SandboxExec, policy: CodexStewardPolicy, forbidden: Sequence[Path]) -> None:
        self._sandbox = sandbox
        self._policy = policy
        self._forbidden = list(forbidden)

    def check(self) -> None:
        problems: list[str] = []
        scratch = self._policy.scratch
        scratch.mkdir(parents=True, exist_ok=True)
        # Unique per check, created exclusively and never through a link: two probes cannot trip over each other's
        # files, and a file the probe did not make is never overwritten or removed.
        name = CANARY_PREFIX + secrets.token_hex(8)
        placed: list[Path] = []
        control = self._plant(scratch, name, CONTROL)
        placed.append(control)
        try:
            problems += self._control(control, scratch)
            for directory in self._forbidden:
                if directory.is_dir():
                    placed.append(self._plant(directory, name, CANARY))
            for canary in placed:
                problems += self._unreadable(canary, scratch)
            problems += self._environment(scratch)
            problems += self._no_write(scratch)
            problems += self._no_network(scratch)
        finally:
            for path in placed:
                path.unlink(missing_ok=True)
        if problems:
            raise SandboxUnsafe(problems)

    def _plant(self, directory: Path, name: str, content: str) -> Path:
        path = directory / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as out:
            out.write(content)
        return path

    def _exec(self, what: str, script: str, cwd: Path) -> tuple[ExecResult | None, list[str]]:
        try:
            return self._sandbox.run(["/usr/bin/bash", "-c", script], cwd), []
        except Exception as err:  # noqa: BLE001 -- any failure to ask the sandbox means it is unproven
            return None, [f"{what}: the sandbox could not be asked ({type(err).__name__}: {err})"]

    def _control(self, control: Path, scratch: Path) -> list[str]:
        result, problems = self._exec("control read", f"cat {_q(control)}", scratch)
        if result is not None and (result.exit_code != 0 or CONTROL not in result.stdout):
            problems.append(f"control read: a file in the scratch dir was not readable, so the other checks prove nothing ({result.stderr.strip()[:200]})")
        return problems

    def _unreadable(self, canary: Path, scratch: Path) -> list[str]:
        result, problems = self._exec(f"read {canary}", f"cat {_q(canary)}", scratch)
        if result is not None and CANARY in result.stdout:
            problems.append(f"{canary.parent} is readable from the steward's shell")
        return problems

    def _environment(self, scratch: Path) -> list[str]:
        result, problems = self._exec("environment", "env", scratch)
        if result is None:
            return problems
        leaked = sorted({line.split("=", 1)[0] for line in result.stdout.splitlines() if "=" in line and (SECRET_ENV.search(line.split("=", 1)[0]) or CANARY in line)})
        return [f"the shell's environment carries {', '.join(leaked)}"] if leaked else []

    def _no_write(self, scratch: Path) -> list[str]:
        targets = [scratch, *self._policy.review_roots]
        problems: list[str] = []
        for target in targets:
            probe = target / ".slop-haus-probe-write"
            result, errors = self._exec(f"write {target}", f"touch {_q(probe)} && echo wrote", scratch)
            problems += errors
            if result is not None and "wrote" in result.stdout:
                probe.unlink(missing_ok=True)
                problems.append(f"the steward's shell can write in {target}")
        return problems

    def _no_network(self, scratch: Path) -> list[str]:
        script = f"timeout 5 bash -c 'exec 3<>/dev/tcp/{NETWORK_TARGET}' && echo connected"
        result, problems = self._exec("network", script, scratch)
        if result is not None and "connected" in result.stdout:
            problems.append("the steward's shell can open network connections")
        return problems


def _q(path: Path) -> str:
    return "'" + str(path).replace("'", "'\\''") + "'"
