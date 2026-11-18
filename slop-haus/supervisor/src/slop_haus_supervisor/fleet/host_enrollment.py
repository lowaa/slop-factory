from __future__ import annotations

from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi


class RemoteControlEnrollment:
    """A Remote Control host's identity on the server, signed in through the supervisor's own credential."""

    def __init__(self, api: SupervisorApi, server: str, name: str) -> None:
        self._api = api
        self.server = server
        self._name = name

    def issue(self) -> TokenPair:
        self._api.ensure_remote_control_host(self._name)
        return self._api.issue_remote_control_credential(self._name)

    def update_script(self, tokens: TokenPair) -> str:
        return self._api.update_script(tokens)

    def refresh(self, tokens: TokenPair) -> TokenPair:
        return self._api.refresh_credential(tokens)

    def revoke(self, tokens: TokenPair) -> None:
        self._api.revoke_remote_control_credential(self._name, tokens)
