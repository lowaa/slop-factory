"""A slop-haus supervisor on a machine of its own: set up with the board's setup script the way a
person does it, one slot signed in through the device flow, started in the foreground with its
log in a file. The api it talks to is whatever `server` says, so a scenario can put a fault proxy
between them."""

from __future__ import annotations

import os
import re
import shutil
import signal
import subprocess
from dataclasses import dataclass
from pathlib import Path

from support.board import Board
from support.workspace import add_origin, fresh_checkout, run_with_device_approval

PACKAGE = "slop_haus_supervisor"
# Where `uv python install` put the interpreter at image build: the containers have no internet.
UV_ENV = {"UV_PYTHON_INSTALL_DIR": "/opt/uv-python", "UV_CACHE_DIR": "/opt/uv-cache", "UV_OFFLINE": "1", "UV_NO_CONFIG": "1"}


def supervisor_env(home: Path, extra: dict[str, str] | None = None) -> dict[str, str]:
    """A minimal environment, like a harness's: what a shell and uv need, and nothing of the test's own."""
    kept = {k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "LC_ALL", "TERM")}
    return {**kept, "HOME": str(home), **UV_ENV, **(extra or {})}


@dataclass
class SupervisorRoot:
    root: Path
    home: Path
    slot: str | None = None

    @property
    def app_src(self) -> Path:
        return self.root / "app" / "src"

    @property
    def slot_dir(self) -> Path:
        assert self.slot, "no slot yet"
        return self.root / "slots" / self.slot

    @property
    def secrets(self) -> Path:
        return self.root / "secrets.env"

    @property
    def config(self) -> Path:
        return self.root / "supervisor.toml"

    def command(self, *args: str) -> list[str]:
        # How the setup script itself runs the package from a directory (uv, no project to build),
        # which is what an air-gapped container can do.
        return ["uv", "run", "--no-project", "--python", ">=3.12", "python", "-m", PACKAGE, *args]

    def env(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        return supervisor_env(self.home, {"PYTHONPATH": str(self.app_src), **(extra or {})})


def set_up_root(board: Board, root: Path, home: Path, tenant: str = "e2e") -> SupervisorRoot:
    """The board's supervisor setup script, run as a person runs it."""
    home.mkdir(parents=True, exist_ok=True)
    script = root.parent / "supervisor-setup.sh"
    script.write_text(board.request("GET", "/api/supervisor/setup_script"))
    done = subprocess.run(
        ["bash", str(script), "--root", str(root), "--tenant", tenant],
        env=supervisor_env(home), capture_output=True, text=True, timeout=120, check=False,
    )
    assert done.returncode == 0, f"supervisor setup exited {done.returncode}:\n{done.stdout}\n{done.stderr}"
    return SupervisorRoot(root, home)


def add_slot(board: Board, sup: SupervisorRoot, name: str, branches: tuple[str, ...] = (), nested_repo: str | None = None) -> None:
    """A fresh agent, registered on the board and signed in by `slot add`, which runs its setup script.
    The slot's checkout has an origin holding `branches`: the attempt branches the board will name in a claim.
    With `nested_repo`, the slot's directory is a plain workspace root and the checkout is that folder in it."""
    script = sup.root.parent / f"{name}-setup.sh"
    script.write_text(board.setup_script(name))
    # The scripted model needs a commit to work on, so the checkout is made before `slot add` sets the slot up.
    checkout = sup.root / "slots" / name / nested_repo if nested_repo else sup.root / "slots" / name
    fresh_checkout(checkout)
    add_origin(checkout, sup.root.parent / f"{name}-origin.git", branches)
    run_with_device_approval(
        board,
        sup.command("slot", "add", name, "--root", str(sup.root), "--setup-script", str(script)),
        sup.root, lambda: board.agent(name)["id"], env=sup.env(),
    )
    sup.slot = name


def log_in(board: Board, sup: SupervisorRoot, name: str, slot_cap: int = 1) -> str:
    """`login`, approved on the add-a-machine consent, which creates the supervisor `name`: what lets it read
    the board's desired state and enrol what it lists. Its id."""
    run_with_device_approval(
        # Unbuffered: it prints the code and then waits, so a buffered pipe would hold the code until it gave up.
        board, sup.command("login", "--root", str(sup.root), "--slots", str(slot_cap)), sup.root, None,
        env=sup.env({"PYTHONUNBUFFERED": "1"}),
        approve=lambda code: board.approve_new_supervisor(code, name, slot_cap),
    )
    return str(board.supervisor(name)["id"])


def point_at(sup: SupervisorRoot, server: str) -> None:
    """The supervisor's server (REST and MCP both) is `server` from now on; the slot's own sign-in keeps the direct address."""
    text = sup.config.read_text()
    changed, count = re.subn(r'^server = .*$', f'server = "{server}"', text, count=1, flags=re.MULTILINE)
    assert count == 1, f"no server line in supervisor.toml:\n{text}"
    sup.config.write_text(changed)


def use_pi(sup: SupervisorRoot, harness_config: dict[str, str]) -> None:
    """Makes pi the root's harness, with the board's pi settings as the default variant. Nothing
    installs the extension at the root yet, and the extension finds its scripts (server-url,
    token.sh, the hooks) beside itself, so this copies the one slot's whole `.slop-haus`, where its
    setup script put them: it stands in for that install, for a root with a single slot."""
    source = sup.slot_dir / ".slop-haus"
    assert (source / "pi" / "slop-haus.ts").is_file(), f"the slot's setup did not install the pi extension in {source}"
    shutil.copytree(source, sup.root / ".slop-haus", dirs_exist_ok=True)
    text = sup.config.read_text()
    variant = ",".join(f"{k}={v}" for k, v in harness_config.items())
    text, harnesses = re.subn(r'^default_harness = .*$', 'default_harness = "pi"', text, count=1, flags=re.MULTILINE)
    text, variants = re.subn(r'^default_variant = .*$', f'default_variant = "{variant}"', text, count=1, flags=re.MULTILINE)
    assert (harnesses, variants) == (1, 1), f"no harness lines in supervisor.toml:\n{text}"
    sup.config.write_text(text)


def use_codex(sup: SupervisorRoot, harness_config: dict[str, str], server: str) -> None:
    """Makes Codex the root's harness, with the board's Codex settings as the default variant.

    The supervisor's Codex run gives the thread slop-haus's url alone (CodexHarness.thread_params), and the MCP
    credential is not wired in yet, so the slot's token.sh is named as the
    server's `http_headers_helper` in the CODEX_HOME's own config.toml, which is where the owner would put it.
    When the supervisor wires it, this goes and the scenarios must still pass."""
    home = sup.root / "harness" / "codex"
    config = home / "config.toml"
    helper = f"{sup.slot_dir / '.slop-haus' / 'token.sh'} --headers-json -server={server}"
    config.write_text(config.read_text() + f'\n[mcp_servers.slop-haus]\nhttp_headers_helper = "{helper}"\n')
    text = sup.config.read_text()
    variant = ",".join(f"{k}={v}" for k, v in harness_config.items())
    text, harnesses = re.subn(r'^default_harness = .*$', 'default_harness = "codex"', text, count=1, flags=re.MULTILINE)
    text, variants = re.subn(r'^default_variant = .*$', f'default_variant = "{variant}"', text, count=1, flags=re.MULTILINE)
    assert (harnesses, variants) == (1, 1), f"no harness lines in supervisor.toml:\n{text}"
    sup.config.write_text(text)


def edit_app(sup: SupervisorRoot, relative: str, old: str, new: str) -> None:
    """Breaks the root's copy of the supervisor: what a regression would be. An edit that finds
    nothing fails, so a rewritten source does not turn a negative test into a test of nothing."""
    path = sup.app_src / PACKAGE / relative
    text = path.read_text()
    assert old in text, f"{relative} no longer contains {old!r}: update this negative test"
    path.write_text(text.replace(old, new, 1))


class RunningSupervisor:
    def __init__(self, sup: SupervisorRoot, log_path: Path, env: dict[str, str]) -> None:
        self.log_path = log_path
        self._log = open(log_path, "wb")
        self._proc = subprocess.Popen(
            sup.command("run", "--root", str(sup.root)),
            cwd=sup.root, env=sup.env(env), stdout=self._log, stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL, start_new_session=True,
        )

    def output(self) -> str:
        return self.log_path.read_text(errors="replace")

    def exit_code(self) -> int | None:
        return self._proc.poll()

    def close(self) -> None:
        if self._proc.poll() is None:
            # The group: the supervisor's claude processes and their helpers go with it.
            os.killpg(self._proc.pid, signal.SIGKILL)
            self._proc.wait()
        self._log.close()
