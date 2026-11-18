from __future__ import annotations

from collections.abc import Callable

from slop_haus_supervisor.harness.login import HarnessLogins
from slop_haus_supervisor.setup.prerequisites import Finding, Prerequisites
from slop_haus_supervisor.setup.grant import GrantState
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.layout import Layout


class HostActions:
    """The real steps of `setup`: the checks, and the same login and slot add the commands of those names run."""

    def __init__(
        self,
        layout: Layout,
        prerequisites: Prerequisites,
        login: Callable[[int], None],
        add_slots: Callable[[int], None],
        grant: Callable[[], GrantState],
        reissue_slots: Callable[[], int],
        board_url: Callable[[], str | None],
        harness_logins: HarnessLogins,
        add_run_env_path: Callable[[str], None],
    ) -> None:
        self._layout = layout
        self._prerequisites = prerequisites
        self._login = login
        self._add_slots = add_slots
        self._grant = grant
        self._reissue_slots = reissue_slots
        self._board_url = board_url
        self._harness_logins = harness_logins
        self._add_run_env_path = add_run_env_path

    def prerequisites(self) -> list[Finding]:
        return self._prerequisites.check()

    def add_run_env_path(self, folder: str) -> None:
        self._add_run_env_path(folder)

    def signed_in(self) -> bool:
        # A credential file the server no longer honours is not a sign-in: setup must sign in again.
        return self._grant() is GrantState.ACTIVE

    def login(self, slots: int) -> None:
        self._login(slots)

    def reissue_slot_credentials(self) -> int:
        return self._reissue_slots()

    def existing_slots(self) -> int:
        return len(cfg.load(self._layout.config_file).slots)

    def add_slots(self, count: int) -> None:
        self._add_slots(count)

    def board_workers_url(self) -> str | None:
        return self._board_url()

    def harness_logged_in(self, name: str) -> bool:
        return self._harness_logins.logged_in(name)

    def harness_login(self, name: str) -> list[str]:
        """The login's follow-up lines; a login that did not take is the first of them, as a warning."""
        outcome = self._harness_logins.login(name)
        return list(outcome.lines) if outcome.ok else [f"WARNING: {outcome.lines[0]}"]
