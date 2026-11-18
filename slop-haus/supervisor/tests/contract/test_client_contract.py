"""The supervisor's hand-written REST clients, run against the server's OpenAPI spec.

A stub server built from the spec answers every call with spec-shaped data and records each request or response
that departs from it, so a route, field or type the server changes shows up here, naming the endpoint, instead
of degrading in a parser that skips what it does not recognise.
"""

from __future__ import annotations

import ast
import copy
import logging
import re
import unittest
from collections.abc import Callable
from pathlib import Path
from typing import Any

from contract_server import ContractServer, serving
from openapi_spec import Json, Spec, load_spec, renamed
from slop_haus_supervisor.harness.base import ModelUsage, Usage
from slop_haus_supervisor.slophaus.client import ChecksState, EventKind, LockState, SlotSnapshot
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.http_client import Backoff, HttpSlopHaus
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef, SupervisorApi
from slop_haus_supervisor.slophaus.token import SlotToken
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.workspace.package import PACKAGE_PATH
from slop_haus_supervisor.ports import ProcessResult

import slop_haus_supervisor

SRC = Path(slop_haus_supervisor.__file__).parent
# The routes this contract covers; /mcp and the remote-control host's /api/conductor are outside it.
COVERED = re.compile(r"^/(api/(worker|claims|supervisor|workspace|integration_lock)|hooks/)")
SLOTS = "/api/supervisor/slots"


class _Runner:
    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Any = None) -> ProcessResult:
        return ProcessResult(0, "slot-token\n", "")


class Exercise:
    """Calls every route of the clients once against `spec`'s stub server and returns what the stub saw."""

    def __init__(self, spec: Spec, tweaks: dict[tuple[str, str], Callable[[Any], Any]] | None = None) -> None:
        self.spec = spec
        self.server = ContractServer(spec)
        for (method, template), change in {**self.default_tweaks(), **(tweaks or {})}.items():
            self.server.tweak(method, template, change)
        self.results: dict[str, Any] = {}

    @staticmethod
    def default_tweaks() -> dict[tuple[str, str], Callable[[Any], Any]]:
        def event(body: Json) -> Json:
            body["events"] = [dict(body["events"][0], kind="claimable")]
            return body

        def state(body: Json) -> Json:
            body["lock"]["holds"] = True
            return body

        def claim(body: Json) -> Json:
            body["attempt"]["harness"] = "claude"
            return body

        return {("POST", "/api/worker/poll"): event, ("GET", "/api/worker/state"): state, ("POST", "/api/claims"): claim}

    def run(self) -> ContractServer:
        with serving(self.server):
            self._http(self.server.url)
            self._supervisor(self.server.url)
        return self.server

    def _http(self, url: str) -> None:
        token = SlotToken(Path("/slots/a/.slop-haus/token.sh"), _Runner())
        client = HttpSlopHaus(url, {"a": token}, "claude", logging.getLogger("contract"), backoff=Backoff(0.0, 0.0), pause=lambda _: False)
        r = self.results
        r["claim"] = client.claim("a", 7)
        r["state"] = client.state("a")
        r["events"] = client.poll("a", 1)
        client.heartbeat("a")
        client.take_lock("a")
        client.release_lock("a")
        client.record_session("a", "claude", "session-1")
        client.escalate("a", "a question")
        client.release("a", "a reason")
        client.report_slot_state("a", SlotSnapshot("running", 3.5, ticket_id=7, error_class="timeout", error_streak=2, pause_reason="why", reason="lock_queued", reason_detail="ticket #7", layout=self.spec.sample({"$ref": "#/components/schemas/WorkerLayout"})))
        client.report_usage("a", Usage((ModelUsage("anthropic", "claude-x", "subscription", {"input": 10, "output": 5, "requests": 1}),)))

    def _supervisor(self, url: str) -> None:
        api = SupervisorApi(Transport(url), lambda: "sup-token")
        r = self.results
        slot = SlotRef("slot-1", "w1")
        r["created"] = api.create_slot("w1")
        r["adopted"] = api.adopt_slot("w1")
        r["live"] = api.live_slots()
        r["taken"] = api.taken_names()
        r["retired"] = api.is_retired("x")
        api.retire_slot(slot)
        r["credential"] = api.issue_credential(slot)
        r["steward"] = api.ensure_steward()
        r["steward_credential"] = api.issue_steward_credential()
        r["host"] = api.ensure_remote_control_host("rc-1")
        r["host_credential"] = api.issue_remote_control_credential("rc-1")
        api.revoke_remote_control_credential("rc-1", TokenPair("access", "refresh", 0))
        r["briefing"] = api.steward_briefing()
        r["agreement"] = api.steward_agreement()
        r["events_since"] = api.steward_events(3)
        r["lease"] = api.acquire_steward_lease()
        api.release_steward_lease()
        r["script"] = api.update_script(TokenPair("access", "refresh", 0))
        Transport(url).send("GET", PACKAGE_PATH, token="sup-token")
        Transport(url).send("GET", "/api/worker/state", token="slot-token")


def _empty(label: str, value: object) -> list[str]:
    return [] if value else [f"{label}: the client read nothing from a spec-shaped answer"]


def failures(exercise: Exercise) -> list[str]:
    """Where the clients and the spec part ways: requests and answers off the spec, and answers the parsers read nothing from."""
    found = list(exercise.run().violations)
    r = exercise.results
    state, claim = r["state"], r["claim"]
    found += _empty("POST /api/claims", claim.branch and claim.base_commit and claim.paths)
    found += _empty("POST /api/claims (session)", claim.session_id)
    found += _empty("GET /api/worker/state (session)", state.session_id)
    found += _empty("GET /api/worker/state (checks)", state.checks == ChecksState.PASSED)
    found += _empty("GET /api/supervisor/slots?include_archived (archived_at)", r["retired"])
    found += _empty("GET /api/worker/state", state.lease_held and state.ticket_status and state.paths)
    found += _empty("GET /api/worker/state (lock)", state.lock == LockState.HELD)
    found += _empty("GET /api/worker/state (ids)", state.ticket_id is not None and state.attempt is not None)
    found += _empty("POST /api/worker/poll", [e for e in r["events"] if e.kind == EventKind.CLAIMABLE and e.ticket_id is not None])
    found += _empty("GET /api/supervisor/slots", r["live"] and r["taken"])
    found += _empty("POST /api/supervisor/slots", r["created"].id)
    found += _empty("POST /api/supervisor/slots/adopt", r["adopted"].id)
    found += _empty("POST /api/supervisor/slots/{id}/credential", r["credential"].access_token and r["credential"].refresh_token)
    found += _empty("PUT /api/supervisor/steward", r["steward"].id and r["steward"].name)
    found += _empty("POST /api/supervisor/steward/credential", r["steward_credential"].access_token)
    found += _empty("GET /api/supervisor/steward/briefing", r["briefing"])
    found += _empty("GET /api/supervisor/steward/agreement", r["agreement"])
    found += _empty("GET /api/supervisor/steward/events", r["events_since"][0])
    found += _empty("POST /api/supervisor/steward/lease", r["lease"])
    found += _empty("GET /api/workspace/update_script", r["script"])
    return found


class ContractTest(unittest.TestCase):
    spec: Spec

    @classmethod
    def setUpClass(cls) -> None:
        cls.spec = Spec(load_spec())

    def test_the_clients_agree_with_the_spec(self) -> None:
        self.assertEqual([], failures(Exercise(self.spec)))

    def test_every_path_the_clients_name_is_in_the_spec(self) -> None:
        named = sorted({path for path in _path_literals() if COVERED.match(path)})

        self.assertGreater(len(named), 15)
        self.assertEqual([], [path for path in named if not self.spec.has_path(path)])

    def test_every_route_the_exercise_sends_is_in_the_spec(self) -> None:
        sent = Exercise(self.spec).run().sent

        self.assertEqual([], [f"{m} {p}" for m, p in sent if self.spec.find(m, p) is None])
        self.assertGreater(len({p for _, p in sent}), 15)

    def test_a_renamed_response_field_fails_naming_the_endpoint(self) -> None:
        drifted = Spec(renamed(self.spec.doc, "ListSlotsResponseBody", "slots", "items"))

        found = failures(Exercise(drifted))

        self.assertTrue(any(f.startswith("GET /api/supervisor/slots") for f in found), found)

    def test_a_renamed_nested_response_field_fails_naming_the_endpoint(self) -> None:
        drifted = Spec(renamed(self.spec.doc, "WorkerLeaseBody", "ticket_id", "ticket"))

        found = failures(Exercise(drifted))

        self.assertTrue(any(f.startswith("GET /api/worker/state") for f in found), found)

    def test_a_renamed_request_field_fails_naming_the_endpoint(self) -> None:
        drifted = Spec(renamed(self.spec.doc, "ClaimBody", "harness", "runtime"))

        found = failures(Exercise(drifted))

        self.assertTrue(any(f.startswith("POST /api/claims: request body") for f in found), found)

    def test_a_renamed_query_parameter_fails_naming_the_endpoint(self) -> None:
        for template, old in (("/api/supervisor/slots", "include_archived"), ("/api/supervisor/steward/events", "after")):
            with self.subTest(old):
                doc = copy.deepcopy(self.spec.doc)
                for parameter in doc["paths"][template]["get"]["parameters"]:
                    if parameter["name"] == old:
                        parameter["name"] = old + "_renamed"

                found = Exercise(Spec(doc)).run().violations

                self.assertTrue(any(f.startswith(f"GET {template}") and old in f for f in found), found)

    def test_a_renamed_session_or_checks_field_fails_naming_the_endpoint(self) -> None:
        for schema, old, endpoint in (
            ("WorkerAttemptBody", "harness_session_id", "GET /api/worker/state (session)"),
            ("WorkerChecksBody", "state", "GET /api/worker/state (checks)"),
            ("AttemptView", "harness_session_id", "POST /api/claims (session)"),
            ("SupervisorSlotBody", "archived_at", "GET /api/supervisor/slots?include_archived"),
        ):
            with self.subTest(old):
                found = failures(Exercise(Spec(renamed(self.spec.doc, schema, old, old + "_renamed"))))

                self.assertTrue(any(f.startswith(endpoint) for f in found), found)

    def test_a_route_gone_from_the_spec_fails_naming_it(self) -> None:
        doc = {**self.spec.doc, "paths": {p: v for p, v in self.spec.doc["paths"].items() if p != "/api/claims/heartbeat"}}

        found = Exercise(Spec(doc)).run().violations

        self.assertIn("POST /api/claims/heartbeat: the spec has no such route", found)


def _path_literals() -> list[str]:
    """Every string in the clients' sources that is an API path; an f-string's holes become `{x}`."""
    found: list[str] = []
    for file in [*(SRC / "slophaus").glob("*.py"), SRC / "workspace" / "rebuild.py", SRC / "workspace" / "package.py"]:
        tree = ast.parse(file.read_text())
        # The pieces of an f-string are read whole, below.
        pieces = {id(v) for node in ast.walk(tree) if isinstance(node, ast.JoinedStr) for v in node.values}
        for node in ast.walk(tree):
            if id(node) in pieces:
                continue
            if isinstance(node, ast.Constant) and isinstance(node.value, str) and node.value.startswith(("/api/", "/hooks/")):
                found.append(node.value)
            elif isinstance(node, ast.JoinedStr):
                text = "".join(str(v.value) if isinstance(v, ast.Constant) else "{x}" for v in node.values)
                if text.startswith(("/api/", "/hooks/")):
                    found.append(text)
    return found
