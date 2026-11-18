from __future__ import annotations

from enum import Enum

from slop_haus_supervisor.launcher.board_view import BoardView
from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.slophaus.credentials import CREDENTIALS
from slop_haus_supervisor.slophaus.token import SlotToken, TokenUnavailable
from slop_haus_supervisor.slophaus.transport import ApiError, Transport
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.layout import Layout

REVOKED_STATUSES = (401, 403)
# What the vendored token helper prints when the server answers invalid_grant to a refresh.
REJECTED_MARK = "credentials were rejected"


class GrantState(Enum):
    MISSING = "missing"
    ACTIVE = "active"
    REVOKED = "revoked"


def is_revoked(err: Exception) -> bool:
    """Whether a failed call means the server no longer honours the supervisor's grant.

    A token.sh refresh the server rejected says so; one that failed for any other reason (no network, a missing
    helper) does not, and must not send setup into a new sign-in. A still-held access token is refused with 401/403.
    """
    if isinstance(err, TokenUnavailable):
        return REJECTED_MARK in str(err)
    return isinstance(err, ApiError) and err.status in REVOKED_STATUSES


class SupervisorGrant:
    """Whether the supervisor's stored sign-in still works, asked of the server rather than read off the disk."""

    def __init__(self, layout: Layout, server: str, runner: ProcessRunner) -> None:
        self._layout = layout
        self._server = server
        self._runner = runner

    def state(self) -> GrantState:
        here = self._layout.supervisor_dir / ".slop-haus"
        if not (here / CREDENTIALS).is_file():
            return GrantState.MISSING
        view = BoardView(Transport(self._server), SlotToken(here / "token.sh", self._runner).get)
        try:
            view.fetch()
        except (TokenUnavailable, ApiError) as err:
            if is_revoked(err):
                return GrantState.REVOKED
            raise ConfigError(f"could not check the supervisor's sign-in with the server: {err}") from err
        except OSError as err:
            raise ConfigError(f"could not reach {self._server} to check the supervisor's sign-in: {err}") from err
        return GrantState.ACTIVE


REVOKED_ADVICE = "the supervisor's sign-in was revoked or rejected by the server: run `slop-haus setup` (or `slop-haus login`) to sign in again"
