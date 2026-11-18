from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol

from slop_haus_supervisor.setup.prerequisites import Finding
from slop_haus_supervisor.workspace.envfile import EnvFile
from slop_haus_supervisor.workspace.layout import Layout
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.text import SecretMasker

DEFAULT_SLOTS = 3
HARNESS_TOKEN_KEYS = ("CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY")
OPTIONAL_SECRETS = ("WOODPECKER_TOKEN",)


class Prompts(Protocol):
    def ask(self, question: str, default: str) -> str: ...
    def ask_hidden(self, question: str) -> str: ...


class Actions(Protocol):
    """What setup does to the host, each safe to repeat: the real ones are the commands of the same names."""

    def prerequisites(self) -> list[Finding]: ...
    def add_run_env_path(self, folder: str) -> None:
        """Puts `folder` in front of the base run.env's PATH, so the service finds what the shell does."""
        ...
    def signed_in(self) -> bool: ...
    def login(self, slots: int) -> None: ...
    def reissue_slot_credentials(self) -> int: ...
    def existing_slots(self) -> int: ...
    def add_slots(self, count: int) -> None: ...
    def board_workers_url(self) -> str | None:
        """Where agents are added when the board provisions them, or None when this host adds its own slots."""
        ...
    def harness_logged_in(self, name: str) -> bool: ...
    def harness_login(self, name: str) -> list[str]: ...


@dataclass(frozen=True)
class SetupOptions:
    slots: int | None = None


class GuidedSetup:
    """One pass over everything a host needs, reporting each step done or skipped.

    Every step first looks at what is already there, so a re-run after an interruption (or a
    finished setup) only does what is missing, and never asks again for what is stored.
    """

    def __init__(
        self, layout: Layout, actions: Actions, prompts: Prompts, say: Callable[[str], None] = print, harness: str = "claude"
    ) -> None:
        self._harness = harness
        self._layout = layout
        self._actions = actions
        self._prompts = prompts
        self._say = say

    def run(self, options: SetupOptions) -> int:
        if not self._prerequisites():
            return 1
        slots = options.slots if options.slots is not None else self._ask_slots()
        self._login(slots)
        self._harness_token()
        self._extra_secrets()
        board = self._actions.board_workers_url()
        if board is None:
            self._slots(slots)
        else:
            self._step("slots", "skipped: this tenant provisions agents from the board")
        self._finish(board)
        return 0

    def _step(self, name: str, outcome: str) -> None:
        status, _, detail = outcome.partition(": ")
        self._say(f"[{status}] {name}" + (f": {detail}" if detail else ""))

    def _prerequisites(self) -> bool:
        findings = self._offer_path_fixes(self._actions.prerequisites())
        for finding in findings:
            mark = "ok" if finding.ok else "MISSING" if finding.blocking else "warning"
            self._say(f"[{mark}] {finding.name}: {finding.detail}")
        missing = [f for f in findings if not f.ok and f.blocking]
        if missing:
            self._say(f"\nsetup stopped: fix {', '.join(f.name for f in missing)} (above), then run `slop-haus setup` again")
        return not missing

    def _offer_path_fixes(self, findings: list[Finding]) -> list[Finding]:
        """A tool only the shell can find is a start-up failure waiting for the service; offer the PATH entry that fixes it."""
        fixed = False
        for folder in dict.fromkeys(f.path_fix for f in findings if not f.ok and f.path_fix):
            names = ", ".join(f.name for f in findings if f.path_fix == folder)
            answer = self._prompts.ask(f"{names} at {folder}, which the service won't see: add {folder} to run.env PATH?", "y")
            if answer.lower().startswith("y"):
                self._actions.add_run_env_path(folder)
                self._step("run.env PATH", f"done: {folder} added")
                fixed = True
        return self._actions.prerequisites() if fixed else findings

    def _ask_slots(self) -> int:
        existing = self._actions.existing_slots()
        default = max(DEFAULT_SLOTS, existing)
        answer = self._prompts.ask("How many worker slots on this host?", str(default))
        if not answer.isdigit() or int(answer) < 1:
            self._say(f"not a number of slots: {answer!r}; using {default}")
            return default
        return int(answer)

    def _login(self, slots: int) -> None:
        if self._actions.signed_in():
            self._step("sign-in", "skipped: the supervisor is already signed in")
            return
        self._actions.login(slots)
        self._step("sign-in", "done")
        # A sign-in that replaced a revoked one leaves the slots' own credentials revoked too; a fresh host has none.
        if self._actions.existing_slots():
            failed = self._actions.reissue_slot_credentials()
            self._step("slot credentials", f"WARNING: {failed} not reissued (above); a slot the server disabled must be enabled on the board first, then `slop-haus slot reissue --all`" if failed else "done: reissued")

    def _harness_token(self) -> None:
        entry = registry.find(self._harness)
        if entry is not None and not entry.login.token_key:
            self._own_login(entry.name)
            return
        secrets = EnvFile(self._layout.secrets_file)
        have = secrets.values()
        stored = next((key for key in HARNESS_TOKEN_KEYS if have.get(key)), "")
        if stored:
            self._replace_harness_token(secrets, stored)
            return
        self._say("The workers log in with a long-lived token: run `claude setup-token` in another terminal and paste it here.")
        token = self._prompts.ask_hidden("harness login token (CLAUDE_CODE_OAUTH_TOKEN, hidden; Enter to skip)")
        if not token:
            self._step("harness login token", "skipped: nothing entered; `slop-haus secret set CLAUDE_CODE_OAUTH_TOKEN` later")
            return
        if len(token) < SecretMasker.MIN_LENGTH:
            self._step("harness login token", f"skipped: under {SecretMasker.MIN_LENGTH} characters, so not a token")
            return
        secrets.set("CLAUDE_CODE_OAUTH_TOKEN", token)
        self._step("harness login token", "done: stored in secrets.env")

    def _own_login(self, name: str) -> None:
        if self._actions.harness_logged_in(name):
            self._step("harness login", f"skipped: {name} is already logged in")
            return
        self._say(f"Logging {name} in (`slop-haus harness login {name}`):")
        lines = self._actions.harness_login(name)
        self._step("harness login", f"done: {lines[0]}" if not lines[0].startswith("WARNING") else lines[0])
        for line in lines[1:]:
            self._say(f"  {line}")

    def _replace_harness_token(self, secrets: EnvFile, key: str) -> None:
        # Not probed with `claude -p`: that spends usage and a network round trip on every re-run.
        token = self._prompts.ask_hidden(f"harness login token ({key} is stored; paste a new one to replace it, hidden; Enter keeps it)")
        if not token:
            self._step("harness login token", f"skipped: {key} already in secrets.env; to replace it run `slop-haus secret set {key}` and restart the supervisor")
            return
        if len(token) < SecretMasker.MIN_LENGTH:
            self._step("harness login token", f"skipped: under {SecretMasker.MIN_LENGTH} characters, so not a token; {key} unchanged")
            return
        secrets.set(key, token)
        self._step("harness login token", f"done: {key} replaced in secrets.env; restart the supervisor to use it")

    def _extra_secrets(self) -> None:
        secrets = EnvFile(self._layout.secrets_file)
        for key in OPTIONAL_SECRETS:
            if secrets.values().get(key):
                self._step(key, "skipped: already set")
                continue
            value = self._prompts.ask_hidden(f"optional {key} (hidden; Enter to skip)")
            if len(value) >= SecretMasker.MIN_LENGTH:
                secrets.set(key, value)
                self._step(key, "done")
            else:
                self._step(key, "skipped")

    def _slots(self, wanted: int) -> None:
        have = self._actions.existing_slots()
        if have >= wanted:
            self._step("slots", f"skipped: {have} already set up")
            return
        self._actions.add_slots(wanted - have)
        self._step("slots", f"done: {wanted - have} added, {wanted} in all")

    def _finish(self, board_url: str | None) -> None:
        if board_url is not None:
            self._say(
                "\nThis host is signed in, so the board can manage it. Next, add the agents there:\n\n"
                f"  {board_url}\n\n"
                "A host whose supervisor has not run `slop-haus login` is not managed by the board."
            )
        self._say(
            "\nSetup is complete. Run the supervisor with:\n\n"
            "  slop-haus service install   # under systemd or launchd: back after a reboot, restarted after an update\n"
            "  slop-haus start             # or detached, in a screen (or tmux) session; then `status`, `logs`, `attach`, `stop`"
        )
