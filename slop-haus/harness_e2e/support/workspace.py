"""A worker's workspace, made the way a person makes one: ask the board for a
setup script, run it in a git checkout, approve the device sign-in it prints."""

from __future__ import annotations

import os
import re
import signal
import subprocess
import threading
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from .board import Board
from .waiting import wait_for

# token.sh login prints an RFC 8628 user code like ABCD-EFGH.
USER_CODE = re.compile(r"\b[A-Z]{4}-[A-Z]{4}\b")

SEED_FILES = {
    "README.md": "# sample repo\n\nA throwaway checkout for the harness e2e suite.\n",
    "src/uploader.py": 'def upload(client, blob):\n    """Upload a blob."""\n    return client.put(blob)\n',
}


@dataclass
class Workspace:
    name: str
    root: Path
    agent_id: str
    setup_log: str


def git(root: Path, *args: str) -> None:
    subprocess.run(
        ["git", "-c", "user.email=harness@e2e.test", "-c", "user.name=harness-e2e", *args],
        cwd=root,
        check=True,
        capture_output=True,
    )


def fresh_checkout(root: Path) -> None:
    root.mkdir(parents=True)
    git(root, "init", "-q", "-b", "main")
    for rel, content in SEED_FILES.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
    git(root, "add", "-A")
    git(root, "commit", "-q", "-m", "initial")


def add_origin(root: Path, origin: Path, branches: tuple[str, ...] = ()) -> None:
    """A bare repo as the checkout's `origin`, holding main and each of `branches` cut from it: the
    supervisor fetches and switches to the claimed branch before every run, as it does in production."""
    origin.mkdir(parents=True)
    git(origin, "init", "-q", "--bare", "-b", "main")
    git(root, "remote", "add", "origin", str(origin))
    git(root, "push", "-q", "origin", "main", *(f"main:refs/heads/{b}" for b in branches))
    git(root, "fetch", "-q", "origin")
    git(root, "branch", "-q", "--set-upstream-to=origin/main", "main")


def set_up(board: Board, name: str, root: Path, setup_timeout: float = 60, remote_control: bool = False) -> Workspace:
    """A remote control is registered the way the Remote Control page's "Add a
    remote control" does (which needs a recent sign-in); a worker's script is
    the Workers page's."""
    fresh_checkout(root)
    script = root.parent / f"{name}-setup.sh"
    script.write_text(board.register_conductor(name) if remote_control else board.setup_script(name))
    agent_id = lambda: board.conductor(name)["id"] if remote_control else board.agent(name)["id"]  # noqa: E731
    output = run_with_device_approval(board, ["bash", str(script)], root, agent_id, setup_timeout)
    if remote_control:
        install_host(root)
    return Workspace(name=name, root=root, agent_id=agent_id(), setup_log=output)


def install_host(root: Path) -> None:
    """The package a Remote Control's host runs from, which the launcher's first start installs
    (a scenario starts the launcher with --no-update, to keep its edits)."""
    env = {k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "LC_ALL")}
    done = subprocess.run([str(root / ".slop-haus" / "install-app.sh")], cwd=root, env={**env, "HOME": str(root.parent)},
                          capture_output=True, text=True, timeout=120, check=False)
    assert done.returncode == 0, f"install-app.sh exited {done.returncode}:\n{done.stdout}\n{done.stderr}"


def run_with_device_approval(
    board: Board,
    argv: list[str],
    cwd: Path,
    agent_id: Callable[[], str] | None,
    timeout: float = 60,
    env: dict[str, str] | None = None,
    approve: Callable[[str], None] | None = None,
) -> str:
    """Runs a command that walks an agent through the device sign-in, approving the code it prints
    as the signed-in human, for the agent `agent_id` names or however `approve` does; its output once it has exited 0."""
    proc = subprocess.Popen(
        argv, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1,
        # Its own group, so an aborted setup takes token.sh's polling with it.
        start_new_session=True,
    )
    lines: list[str] = []
    reader = threading.Thread(target=lambda: lines.extend(proc.stdout), daemon=True)  # type: ignore[arg-type]
    reader.start()
    try:
        output = lambda: "".join(lines)  # noqa: E731
        code = wait_for(
            "the setup script to print a device code",
            lambda: (m := USER_CODE.search(output())) and m.group(0),
            timeout=timeout,
            diagnose=output,
        )
        if approve is not None:
            approve(code)
        else:
            assert agent_id is not None, "say which agent the sign-in is for, or how to approve it"
            board.approve_device_code(code, agent_id())
        wait_for("the setup script to finish", lambda: proc.poll() is not None, timeout=timeout, diagnose=output)
        reader.join(timeout=5)
        if proc.returncode != 0:
            raise AssertionError(f"setup script exited {proc.returncode}:\n{output()}")
        return output()
    finally:
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGKILL)
        proc.wait()
        if proc.stdout:
            proc.stdout.close()
