"""The harnesses the supervisor knows, with what each can be used for.

Names and capabilities equal `contracts/harnesses.json` (a contract test holds them together); the text a harness needs
on this side of the wire lives here too. Adding a harness: add it to the contract and here.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class LoginSpec:
    """How `slop-haus harness login` logs one harness in on the supervisor's host."""

    command: tuple[str, ...]
    # The variable that points the command at the harness's login folder under the root; "" when it keeps none there.
    dir_env: str = ""
    # The secret the command prints, which is stored the way `secret set` stores it; "" when the login lives in the folder.
    token_key: str = ""
    # Whether the supervisor only reads the login when it starts.
    restart: bool = False
    # What the person does once the command has started.
    instructions: str = ""


@dataclass(frozen=True)
class HarnessEntry:
    name: str
    worker: bool
    # "yes", "no" or "conditional": steward-capable only where a host check passes.
    steward: str
    remote_control: bool
    install_fix: str
    login: LoginSpec
    # Set when the harness runs every tool call without asking.
    unattended_warning: str = ""

    @property
    def login_fix(self) -> str:
        """What to run when its login on the supervisor's host fails."""
        restart = ", restart the supervisor" if self.login.restart else ""
        return f"on the supervisor's host run `slop-haus harness login {self.name}`{restart}, then `slop-haus slot unpause <slot>`"


HARNESS_ENTRIES = (
    HarnessEntry(
        "claude", worker=True, steward="yes", remote_control=True,
        install_fix="install Claude Code (https://docs.claude.com/en/docs/claude-code), then `claude setup-token` for the login token",
        login=LoginSpec(("claude", "setup-token"), token_key="CLAUDE_CODE_OAUTH_TOKEN", restart=True),
    ),
    HarnessEntry(
        "pi", worker=True, steward="no", remote_control=True,
        install_fix="install pi and log in to it",
        login=LoginSpec(("pi",), dir_env="PI_CODING_AGENT_DIR", instructions="pi has started: type /login, finish, then exit pi"),
        unattended_warning="pi runs every tool call without asking -- run this supervisor in a container or VM",
    ),
    HarnessEntry(
        "codex", worker=True, steward="conditional", remote_control=True,
        install_fix="install Codex (https://github.com/openai/codex) and log in to it with ChatGPT",
        # Device code, not the localhost redirect, which does not reach a browser over SSH.
        login=LoginSpec(("codex", "login", "--device-auth"), dir_env="CODEX_HOME"),
    ),
)

NAMES = tuple(h.name for h in HARNESS_ENTRIES)
WORKER_NAMES = tuple(h.name for h in HARNESS_ENTRIES if h.worker)
REMOTE_CONTROL_NAMES = tuple(h.name for h in HARNESS_ENTRIES if h.remote_control)


def find(name: str) -> HarnessEntry | None:
    return next((h for h in HARNESS_ENTRIES if h.name == name), None)
