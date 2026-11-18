from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import cast
from urllib.parse import quote

from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.slop_haus_token import CLIENT_ID
from slop_haus_supervisor.workspace.update_script import REPOS_HEADER, UpdateScript, parse_repos_header
from slop_haus_supervisor.slophaus.transport import ApiError, Response, Transport


@dataclass(frozen=True)
class SlotRef:
    id: str
    name: str


class SupervisorApi:
    """The server's supervisor routes, called with the supervisor's own credential.

    `token` is asked for on every call, so a refresh by token.sh is always picked up.
    """

    def __init__(self, transport: Transport, token: Callable[[], str]) -> None:
        self._transport = transport
        self._token = token

    def _call(self, what: str, method: str, path: str, body: object | None = None) -> Response:
        answer = self._transport.send(method, path, token=self._token(), json_body=body)
        if not answer.ok:
            raise ApiError(f"{what}: {answer.detail()}", answer.status)
        return answer

    def create_slot(self, name: str) -> SlotRef:
        answer = self._call(f"the server would not create slot {name!r}", "POST", "/api/supervisor/slots", {"name": name})
        slot_id = answer.json().get("id")
        if not isinstance(slot_id, str) or not slot_id:
            raise ApiError("the server sent an unrecognisable slot")
        return SlotRef(slot_id, name)

    def adopt_slot(self, name: str) -> SlotRef:
        """Link an existing agent of this name that has no supervisor (one reused as a slot) as this supervisor's slot."""
        answer = self._call(f"the server would not adopt agent {name!r} as a slot", "POST", "/api/supervisor/slots/adopt", {"name": name})
        slot_id = answer.json().get("id")
        if not isinstance(slot_id, str) or not slot_id:
            raise ApiError("the server sent an unrecognisable slot")
        return SlotRef(slot_id, name)

    def _listed(self, path: str) -> list[dict[str, object]]:
        listed = self._call("could not list the supervisor's slots", "GET", path).json().get("slots")
        return [cast(dict[str, object], entry) for entry in cast(list[object], listed) if isinstance(entry, dict)] if isinstance(listed, list) else []

    def live_slots(self) -> list[SlotRef]:
        slots: list[SlotRef] = []
        for fields in self._listed("/api/supervisor/slots"):
            slot_id, slot_name = fields.get("id"), fields.get("name")
            if isinstance(slot_id, str) and isinstance(slot_name, str):
                slots.append(SlotRef(slot_id, slot_name))
        return slots

    def taken_names(self) -> set[str]:
        """Every slot name the server holds, retired slots' included: a retired slot's name stays taken."""
        return {name for f in self._listed("/api/supervisor/slots?include_archived=true") if isinstance(name := f.get("name"), str)}

    def find_slot(self, name: str) -> SlotRef | None:
        return next((slot for slot in self.live_slots() if slot.name == name), None)

    def is_retired(self, name: str) -> bool:
        """Whether the name is held by a retired slot; its name stays taken, so the live listing cannot tell it from an unknown one."""
        return any(f.get("name") == name and f.get("archived_at") for f in self._listed("/api/supervisor/slots?include_archived=true"))

    def retire_slot(self, slot: SlotRef) -> None:
        self._call(f"the server would not retire slot {slot.name!r}", "DELETE", f"/api/supervisor/slots/{slot.id}")

    def issue_credential(self, slot: SlotRef, now: Callable[[], float] = time.time) -> TokenPair:
        body = self._call(
            f"the server would not issue slot {slot.name!r} a credential", "POST", f"/api/supervisor/slots/{slot.id}/credential"
        ).json()
        return self._token_pair(body, now)

    @staticmethod
    def _token_pair(body: dict[str, object], now: Callable[[], float]) -> TokenPair:
        access, refresh, ttl = body.get("access_token"), body.get("refresh_token"), body.get("expires_in")
        if not (isinstance(access, str) and access and isinstance(refresh, str) and refresh):
            raise ApiError("the server sent an unrecognisable slot credential")
        return TokenPair(access, refresh, int(now()) + (ttl if isinstance(ttl, int) and ttl > 0 else 900))

    def ensure_steward(self) -> SlotRef:
        """The tenant's steward agent, made by the server if there is none."""
        body = self._call("the server would not give the tenant's steward", "PUT", "/api/supervisor/steward").json()
        steward_id, name = body.get("id"), body.get("name")
        if not (isinstance(steward_id, str) and steward_id and isinstance(name, str) and name):
            raise ApiError("the server sent an unrecognisable steward")
        return SlotRef(steward_id, name)

    def issue_steward_credential(self, now: Callable[[], float] = time.time) -> TokenPair:
        return self._token_pair(
            self._call("the server would not issue the steward a credential", "POST", "/api/supervisor/steward/credential").json(), now
        )

    def ensure_remote_control_host(self, name: str) -> SlotRef:
        """The conductor behind a launch=supervisor `remote_control` entry of this supervisor's desired state, made by the server if there is none."""
        body = self._call("the server would not give the Remote Control host's agent", "PUT", _host_path(name)).json()
        host_id, host_name = body.get("id"), body.get("name")
        if not (isinstance(host_id, str) and host_id and isinstance(host_name, str) and host_name):
            raise ApiError("the server sent an unrecognisable Remote Control host")
        return SlotRef(host_id, host_name)

    def issue_remote_control_credential(self, name: str, now: Callable[[], float] = time.time) -> TokenPair:
        return self._token_pair(
            self._call("the server would not issue the Remote Control host a credential", "POST", _host_path(name) + "/credential").json(), now
        )

    def revoke_remote_control_credential(self, name: str, tokens: TokenPair) -> None:
        """End the grant these tokens belong to. 403 when it is already gone or is not this host's."""
        self._call(
            "the server would not revoke the Remote Control host's credential", "POST", _host_path(name) + "/credential/revoke",
            {"refresh_token": tokens.refresh_token},
        )

    def refresh_credential(self, tokens: TokenPair, now: Callable[[], float] = time.time) -> TokenPair:
        """Trade a credential's refresh token for a new pair, as token.sh does; the caller must store the result, the old pair is spent."""
        answer = self._transport.send(
            "POST", "/oauth/token", form={"grant_type": "refresh_token", "refresh_token": tokens.refresh_token, "client_id": CLIENT_ID}
        )
        if not answer.ok:
            raise ApiError(f"the server would not refresh the credential: {answer.detail()}", answer.status)
        return self._token_pair(answer.json(), now)

    def revoke_steward_credential(self, tokens: TokenPair) -> None:
        """End the steward grant these tokens belong to. 403 when it is already gone or is not the steward's."""
        self._call(
            "the server would not revoke the steward's credential", "POST", "/api/supervisor/steward/credential/revoke",
            {"refresh_token": tokens.refresh_token},
        )

    def steward_briefing(self) -> str:
        return self._text("the server would not give the steward's briefing", "GET", "/api/supervisor/steward/briefing")

    def steward_mode(self) -> str:
        """The tenant's steward_mode: off, shadow or live."""
        mode = self._call("the server would not give the steward's mode", "GET", "/api/supervisor/steward/mode").json().get("mode")
        # The runner treats anything but "off" as on, so a value outside the contract must fail here, closed.
        if mode not in ("off", "shadow", "live"):
            raise ApiError("the server sent an unrecognisable steward mode")
        return mode

    def steward_agreement(self) -> str:
        return self._text("the server would not give the agreement report", "GET", "/api/supervisor/steward/agreement")

    def _text(self, what: str, method: str, path: str) -> str:
        text = self._call(what, method, path).json().get("text")
        if not isinstance(text, str):
            raise ApiError(f"{what}: the answer had no text")
        return text

    def steward_events(self, after: int | None) -> tuple[list[tuple[str, int]], int]:
        """Board events after the cursor as (kind, ticket id), and the next cursor. `after` None starts from now: no events."""
        path = "/api/supervisor/steward/events" + ("" if after is None else f"?after={after}")
        body = self._call("could not read the steward's board events", "GET", path).json()
        cursor, listed = body.get("cursor"), body.get("events")
        if not isinstance(cursor, int) or not isinstance(listed, list):
            raise ApiError("the server sent unrecognisable steward events")
        events: list[tuple[str, int]] = []
        for entry in cast(list[object], listed):
            fields = cast(dict[str, object], entry) if isinstance(entry, dict) else {}
            kind, ticket_id = fields.get("kind"), fields.get("ticket_id")
            if isinstance(kind, str) and isinstance(ticket_id, int):
                events.append((kind, ticket_id))
        return events, cursor

    def acquire_steward_lease(self) -> bool:
        return self._call("could not take the steward lease", "POST", "/api/supervisor/steward/lease").json().get("acquired") is True

    def release_steward_lease(self) -> None:
        self._call("could not release the steward lease", "DELETE", "/api/supervisor/steward/lease")

    def desired_state(self) -> dict[str, object]:
        """The board's document for this supervisor; empty while the tenant does not provision from the board."""
        return self._call("could not read the desired state", "GET", "/api/supervisor/desired_state").json()

    def provision_from_board(self) -> bool:
        """Whether the tenant provisions agents from the board; a server that does not say means off."""
        document = self.desired_state()
        # Response.json() turns an unreadable body into {}, which must not read as "off".
        if not isinstance(document.get("agents"), list):
            raise ApiError("the server sent an unrecognisable desired state")
        return document.get("provision_from_board") is True

    def workspace_reconcile(self) -> bool:
        """Whether the tenant's owner has the supervisor reconcile slots' checkouts; a server that does not say means off."""
        return self.desired_state().get("workspace_reconcile") is True

    def workspace_manifest(self) -> dict[str, object]:
        """The workspace manifest as a slot may read it: its revision and each repo's path, remote and base."""
        return self._call("could not read the workspace manifest", "GET", "/api/workspace/manifest/slot").json()

    def report_observed(self, reports: list[dict[str, object]]) -> None:
        self._call("could not report the observed state", "POST", "/api/supervisor/desired_state/observed", {"agents": reports})

    def update_script(self, tokens: TokenPair) -> UpdateScript:
        """What the slot's own `update.sh` runs: its hooks, token helper, briefing, and the manifest's repo sync."""
        answer = self._transport.send("GET", "/api/workspace/update_script", token=tokens.access_token)
        if not answer.ok:
            raise ApiError(f"the server would not hand out the slot's workspace files: {answer.detail()}", answer.status)
        return UpdateScript(answer.body.decode(), parse_repos_header(answer.headers.get(REPOS_HEADER.lower())))


def _host_path(name: str) -> str:
    return f"/api/supervisor/remote-control/{quote(name, safe='')}"
