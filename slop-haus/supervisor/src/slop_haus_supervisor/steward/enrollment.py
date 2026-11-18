from __future__ import annotations

from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi


class StewardEnrollment:
    """The steward's identity on the server, signed in through the supervisor's own credential."""

    def __init__(self, api: SupervisorApi, server: str) -> None:
        self._api = api
        self.server = server

    def issue(self) -> TokenPair:
        self._api.ensure_steward()
        return self._api.issue_steward_credential()

    def update_script(self, tokens: TokenPair) -> str:
        return self._api.update_script(tokens)

    def refresh(self, tokens: TokenPair) -> TokenPair:
        return self._api.refresh_credential(tokens)

    def revoke(self, tokens: TokenPair) -> None:
        self._api.revoke_steward_credential(tokens)
