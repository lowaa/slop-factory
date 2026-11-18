"""The slop-haus API as a signed-in person uses it: the test plays the human."""

from __future__ import annotations

import http.cookiejar
import json
import urllib.error
import urllib.request
from typing import Any


class ApiError(AssertionError):
    def __init__(self, method: str, path: str, status: int, body: str) -> None:
        super().__init__(f"{method} {path} -> {status}: {body}")
        self.status = status


class Board:
    def __init__(self, server: str) -> None:
        self.server = server.rstrip("/")
        self._jar = http.cookiejar.CookieJar()
        self._opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self._jar))
        self._csrf = ""
        self._password = ""

    def request(self, method: str, path: str, body: Any = None, ok: tuple[int, ...] = (200, 201, 204)) -> Any:
        data = None if body is None else json.dumps(body).encode()
        headers = {"Content-Type": "application/json", "Origin": self.server}
        if self._csrf:
            headers["X-CSRF-Token"] = self._csrf
        req = urllib.request.Request(self.server + path, data=data, method=method, headers=headers)
        try:
            with self._opener.open(req, timeout=15) as resp:
                raw = resp.read().decode()
        except urllib.error.HTTPError as err:
            raise ApiError(method, path, err.code, err.read().decode()) from None
        if not raw:
            return None
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return raw

    def sign_in(self, email: str, password: str) -> None:
        profile = self.request("POST", "/api/auth/login", {"email": email, "password": password})
        self._csrf = profile["csrf_token"]
        self._password = password

    def create_ticket(self, title: str, body: str, paths: list[str]) -> int:
        created = self.request(
            "POST",
            "/api/tickets",
            {"title": title, "body": body, "status": "open", "paths": [{"path": p} for p in paths]},
        )
        return int(created["id"])

    def ticket(self, ticket_id: int) -> dict[str, Any]:
        return self.request("GET", f"/api/tickets/{ticket_id}")

    def handoffs(self, ticket_id: int) -> list[str]:
        """The handoffs agents left on the ticket, oldest first."""
        return [c["body"] for c in self.request("GET", f"/api/tickets/{ticket_id}/contexts")["contexts"]]

    def agents(self) -> list[dict[str, Any]]:
        return self.request("GET", "/api/agents")["agents"]

    def agent(self, name: str) -> dict[str, Any]:
        for agent in self.agents():
            if agent["name"] == name:
                return agent
        raise AssertionError(f"no agent named {name!r} on the board")

    def setup_script(self, name: str) -> str:
        return self.request("POST", "/api/agents/setup_script", {"name": name})

    def approve_device_code(self, user_code: str, agent_id: str) -> None:
        # No lookup first, as the page does: the decision looks the code up
        # itself, and each lookup counts against a limit (20 in 10 minutes per
        # user) that a whole suite's worth of workspaces shares.
        self.request("POST", "/api/oauth/device/decision", {"user_code": user_code, "approve": True, "agent_id": agent_id})

    def approve_new_supervisor(self, user_code: str, name: str, slot_cap: int) -> None:
        """The add-a-machine consent: approving a supervisor's sign-in creates it."""
        self.request("POST", "/api/oauth/device/decision", {
            "user_code": user_code, "approve": True, "agent_name": name, "agent_kind": "supervisor", "slot_cap": slot_cap,
        })

    def supervisor(self, name: str) -> dict[str, Any]:
        for supervisor in self.request("GET", "/api/supervisors")["supervisors"]:
            if supervisor["name"] == name:
                return supervisor
        raise AssertionError(f"no supervisor named {name!r} on the board")

    def provision_from_board(self) -> bool:
        return bool(self.request("GET", "/api/settings/provision_from_board")["bool_val"])

    def set_provision_from_board(self, on: bool) -> None:
        self.request("PATCH", "/api/settings/provision_from_board", {"type": "bool", "bool_val": on})

    def desired_state(self, supervisor_id: str) -> dict[str, Any]:
        return self.request("GET", f"/api/supervisors/{supervisor_id}/desired_state")

    def put_desired_state(self, supervisor_id: str, revision: int, agents: list[dict[str, Any]]) -> dict[str, Any]:
        return self.request("PUT", f"/api/supervisors/{supervisor_id}/desired_state", {"revision": revision, "agents": agents})

    def conductors(self) -> list[dict[str, Any]]:
        """The signed-in person's own remote controls: what the Remote Control tab lists."""
        return self.request("GET", "/api/conductors")["conductors"]

    def save_harness_defaults(self, settings: dict[str, dict[str, str]]) -> None:
        self.request("PUT", "/api/harness_config", {"settings": settings})

    def llm_usage(self) -> dict[str, Any]:
        return self.request("GET", "/api/reports/llm-usage")

    # -- Remote Control (a conductor, in the API) --

    def register_conductor(self, name: str) -> str:
        """The setup script of a new remote control, as "Add a remote control" hands it out.
        Registering needs a recent sign-in, so the password is confirmed first."""
        self.request("POST", "/api/auth/reauth/password", {"password": self._password})
        return self.request("POST", "/api/conductors", {"name": name})

    def conductor(self, name: str) -> dict[str, Any]:
        for conductor in self.request("GET", "/api/conductors")["conductors"]:
            if conductor["name"] == name:
                return conductor
        raise AssertionError(f"no remote control named {name!r} on the board")

    def save_agent_harness_config(self, agent_id: str, settings: dict[str, dict[str, str]]) -> None:
        self.request("PUT", f"/api/agents/{agent_id}/harness_config", {"settings": settings})

    def conductor_host(self, conductor_id: str) -> dict[str, Any]:
        return self.request("GET", f"/api/conductors/{conductor_id}/host")

    def start_conversation(self, conductor_id: str, message: str) -> dict[str, Any]:
        return self.request("POST", f"/api/conductors/{conductor_id}/conversations", {"message": message})

    def send_message(self, conductor_id: str, conversation_id: str, text: str) -> dict[str, Any]:
        return self.request("POST", f"/api/conductors/{conductor_id}/conversations/{conversation_id}/messages", {"text": text})

    def conversation(self, conductor_id: str, conversation_id: str) -> dict[str, Any]:
        return self.request("GET", f"/api/conductors/{conductor_id}/conversations/{conversation_id}")

    def conversation_events(self, conductor_id: str, conversation_id: str) -> list[dict[str, Any]]:
        return self.request("GET", f"/api/conductors/{conductor_id}/conversations/{conversation_id}/events")["events"]
