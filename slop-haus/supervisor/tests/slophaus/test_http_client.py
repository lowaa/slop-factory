from __future__ import annotations

from collections.abc import Mapping

import contextlib
import json
import logging
import threading
import unittest
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.base import ModelUsage, Usage
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.slophaus.client import ChecksState, ClaimRefused, Event, EventKind, LockState, ReviewRequest, SlotSnapshot
from slop_haus_supervisor.fleet.slot_report import error_class
from slop_haus_supervisor.slophaus.http_client import (
    SUPERVISOR_PROTOCOL,
    ApiError,
    ApiStopped,
    AuthRejected,
    Backoff,
    HttpSlopHaus,
)
from slop_haus_supervisor.slophaus.token import SlotToken, TokenUnavailable
from slop_haus_supervisor.usage.conversation import USAGE_REVISION


@dataclass
class Seen:
    method: str
    path: str
    auth: str
    body: Any
    protocol: str = ""


@dataclass
class Stub:
    """Answers each request with the next scripted (status, body) for its path; the last repeats."""

    script: dict[str, list[tuple[int, Any]]] = field(default_factory=lambda: {})
    seen: list[Seen] = field(default_factory=lambda: [])
    # Authorization values the server refuses with 401, as an expired or revoked token would be.
    refuse: set[str] = field(default_factory=lambda: set[str]())
    # Holds requests carrying `gate_auth` until the barrier's parties have all arrived.
    gate: threading.Barrier | None = None
    gate_auth: str = ""

    def answer(self, path: str) -> tuple[int, Any]:
        queue = self.script.get(path, [(200, {})])
        return queue.pop(0) if len(queue) > 1 else queue[0]


def serve(stub: Stub) -> ThreadingHTTPServer:
    class Handler(BaseHTTPRequestHandler):
        def _handle(self) -> None:
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length) if length else b""
            stub.seen.append(Seen(self.command, self.path, self.headers.get("Authorization", ""), json.loads(raw) if raw else None, self.headers.get("X-Supervisor-Version", "")))
            auth = self.headers.get("Authorization", "")
            if stub.gate is not None and auth == stub.gate_auth:
                stub.gate.wait(10)
            status, body = (401, {"detail": "expired"}) if auth in stub.refuse else stub.answer(self.path)
            payload = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        do_GET = do_POST = do_DELETE = _handle

        def log_message(self, format: str, *args: Any) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def used(model: str, billing: str = "subscription", **counters: int) -> ModelUsage:
    return ModelUsage("anthropic", model, billing, counters)


class ScriptedTokens:
    def __init__(self, tokens: list[str]) -> None:
        self.tokens = tokens
        self.forced = 0
        self.lock = threading.Lock()

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        with self.lock:
            self.forced += 1
            token = self.tokens.pop(0) if len(self.tokens) > 1 else self.tokens[0]
        return ProcessResult(0 if token else 1, token + "\n", "no login")


class HttpClientTest(unittest.TestCase):
    def setUp(self) -> None:
        self.stub = Stub()
        self.server = serve(self.stub)
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.pauses: list[float] = []
        self.stop_after: int | None = None
        self.restarted = 0

    def pause(self, seconds: float) -> bool:
        self.pauses.append(seconds)
        return self.stop_after is not None and len(self.pauses) >= self.stop_after

    def client(self, runner: ScriptedTokens | None = None, token: SlotToken | None = None, **kw: Any) -> HttpSlopHaus:
        port = self.server.server_address[1]
        token = token or SlotToken(Path("/slots/a/.slop-haus/token.sh"), runner or ScriptedTokens(["t1"]))
        return HttpSlopHaus(
            f"http://127.0.0.1:{port}", {"a": token}, "claude", logging.getLogger("test"),
            backoff=Backoff(1.0, 4.0), pause=self.pause, on_restart=self.on_restart, **kw,
        )  # fmt: skip

    def on_restart(self) -> None:
        self.restarted += 1

    def test_claim_reports_what_the_run_is_made_with_when_known(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]

        self.client(harness_version="2.1.0 (Claude Code)").claim("a", 7, bundle_hash="abc123")

        self.assertEqual(
            {"harness": "claude", "ticket_id": 7, "harness_version": "2.1.0 (Claude Code)", "bundle_hash": "abc123"},
            self.stub.seen[0].body,
        )

    def test_claim_sends_nothing_extra_when_the_run_has_nothing_to_report(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]

        self.client().claim("a", 7)

        self.assertEqual({"harness": "claude", "ticket_id": 7}, self.stub.seen[0].body)

    def test_claim_returns_the_branch_and_resumable_session(self) -> None:
        self.stub.script["/api/claims"] = [(200, {
            "ticket": {"id": 7},
            "attempt": {"branch": "ticket-7-a1", "base_commit": "abc", "harness": "claude", "harness_session_id": "s9"},
        })]

        claim = self.client().claim("a", 7)

        self.assertEqual(("ticket-7-a1", "abc", "s9", 7), (claim.branch, claim.base_commit, claim.session_id, claim.ticket_id))
        self.assertEqual({"harness": "claude", "ticket_id": 7}, self.stub.seen[0].body)
        self.assertEqual("Bearer t1", self.stub.seen[0].auth)

    def test_a_ticket_with_paths_has_paths_and_one_without_does_not(self) -> None:
        attempt = {"branch": "ticket-7-a1", "base_commit": "abc"}
        self.stub.script["/api/claims"] = [
            (200, {"ticket": {"id": 7, "paths": [{"path": "a/"}]}, "attempt": attempt}),
            (200, {"ticket": {"id": 8, "paths": []}, "attempt": attempt}),
            (200, {"ticket": {"id": 9}, "attempt": attempt}),
        ]
        client = self.client()

        self.assertEqual([True, False, False], [client.claim("a").has_paths for _ in range(3)])

    def test_a_claims_paths_are_the_path_of_each_scope_object(self) -> None:
        attempt = {"branch": "ticket-7-a1", "base_commit": "abc"}
        paths = [{"path": "app/src/", "source": "manual"}, {"path": "app/docs/", "source": "manual"}]
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7, "paths": paths}, "attempt": attempt})]

        self.assertEqual(("app/src/", "app/docs/"), self.client().claim("a").paths)

    def test_another_harnesses_session_is_not_resumed(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b", "harness": "pi", "harness_session_id": "s9"}})]

        self.assertIsNone(self.client().claim("a").session_id)

    def test_an_empty_queue_and_a_conflict_are_refusals(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": None, "blocked": None}), (409, {"detail": "already holding"})]
        client = self.client()

        for _ in range(2):
            with self.assertRaises(ClaimRefused):
                client.claim("a")

    def test_an_outage_is_retried_with_backoff_until_it_ends(self) -> None:
        self.stub.script["/api/claims/heartbeat"] = [(502, {}), (503, {}), (429, {}), (503, {}), (200, {})]

        self.client().heartbeat("a")

        self.assertEqual([1.0, 2.0, 4.0, 4.0], self.pauses)
        self.assertEqual(5, len(self.stub.seen))

    def test_slot_state_is_posted_without_waiting_out_an_outage(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(503, {}), (503, {}), (503, {})]

        with self.assertRaises(ApiError):
            self.client().report_slot_state("a", SlotSnapshot("running", 12.5, 7, "api_error", 3))

        self.assertEqual(2, len(self.stub.seen))
        self.assertEqual(
            {"phase": "running", "phase_age_seconds": 12.5, "ticket_id": 7, "error_class": "api_error", "error_streak": 3},
            self.stub.seen[0].body,
        )

    def test_slot_state_leaves_out_what_it_does_not_have(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {})]

        self.client().report_slot_state("a", SlotSnapshot("idle", 1.0))

        self.assertEqual({"phase": "idle", "phase_age_seconds": 1.0, "error_streak": 0}, self.stub.seen[0].body)

    def test_the_workspace_bundle_hash_is_posted_only_when_there_is_one(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {}), (204, {})]

        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0, bundle_hash="c1ff58edfd3c9bb6"))
        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0))

        self.assertEqual("c1ff58edfd3c9bb6", self.stub.seen[0].body["bundle_hash"])
        self.assertNotIn("bundle_hash", self.stub.seen[1].body)

    def test_idle_is_posted_only_when_the_slot_says_so(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {}), (204, {})]

        self.client().report_slot_state("a", SlotSnapshot("running", 3.0, idle=True))
        self.client().report_slot_state("a", SlotSnapshot("running", 3.0))

        self.assertIs(True, self.stub.seen[0].body["idle"])
        self.assertNotIn("idle", self.stub.seen[1].body)

    def test_a_pause_reason_is_posted_only_when_there_is_one(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {}), (204, {})]

        self.client().report_slot_state("a", SlotSnapshot("paused", 3.0, pause_reason="push refused"))
        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0))

        self.assertEqual("push refused", self.stub.seen[0].body["pause_reason"])
        self.assertNotIn("pause_reason", self.stub.seen[1].body)

    def test_a_reason_and_its_detail_are_posted_only_when_there_is_one(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {}), (204, {})]

        self.client().report_slot_state("a", SlotSnapshot("wait_lock", 3.0, reason="lock_queued", reason_detail="ticket #7"))
        self.client().report_slot_state("a", SlotSnapshot("running", 3.0))
        self.stub.script["/api/worker/slot-state"].append((204, {}))
        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0, reason="no_claimable"))

        self.assertEqual(("no_claimable", False), (self.stub.seen[2].body["reason"], "reason_detail" in self.stub.seen[2].body))
        self.assertEqual(("lock_queued", "ticket #7"), (self.stub.seen[0].body["reason"], self.stub.seen[0].body["reason_detail"]))
        self.assertNotIn("reason", self.stub.seen[1].body)
        self.assertNotIn("reason_detail", self.stub.seen[1].body)

    def test_a_layout_is_posted_only_when_the_snapshot_has_one(self) -> None:
        self.stub.script["/api/worker/slot-state"] = [(204, {}), (204, {})]
        layout = {"root": {"path": ".", "git": False, "origin": ""}, "repos": [{"path": "slop-factory", "git": True, "origin": "h/x"}]}

        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0, layout=layout))
        self.client().report_slot_state("a", SlotSnapshot("idle", 3.0))

        self.assertEqual(layout, self.stub.seen[0].body["layout"])
        self.assertNotIn("layout", self.stub.seen[1].body)

    def test_an_invalid_token_fetches_a_fresh_one(self) -> None:
        runner = ScriptedTokens(["old", "new"])
        self.stub.script["/api/worker/state"] = [(401, {"detail": "invalid token"}), (200, {"lease": None})]

        state = self.client(runner).state("a")

        self.assertFalse(state.lease_held)
        self.assertEqual(["Bearer old", "Bearer new"], [s.auth for s in self.stub.seen])

    def test_the_state_carries_the_tickets_paths(self) -> None:
        self.stub.script["/api/worker/state"] = [(200, {
            "lease": {"id": 1, "ticket_id": 7, "attempt": 1}, "ticket": {"id": 7, "status": "in_progress", "paths": ["app/src/", "app/docs/"]},
        })]

        state = self.client().state("a")

        self.assertEqual(("app/src/", "app/docs/"), state.paths)

    def test_a_connection_refused_is_an_outage_too(self) -> None:
        client = self.client()
        self.server.shutdown()
        self.server.server_close()
        self.stop_after = 2

        with self.assertRaises(ApiStopped):
            client.state("a")

        self.assertEqual([1.0, 2.0], self.pauses)

    def test_a_token_helper_that_fails_is_waited_out(self) -> None:
        runner = ScriptedTokens(["", "t2"])
        self.stub.script["/api/worker/state"] = [(200, {"lease": None})]

        self.client(runner).state("a")

        self.assertEqual(1, len(self.pauses))

    def test_shutdown_ends_a_retry_loop(self) -> None:
        self.stub.script["/api/claims/heartbeat"] = [(503, {})]
        self.stop_after = 1

        with self.assertRaises(ApiStopped):
            self.client().heartbeat("a")

    def test_other_client_errors_are_not_retried(self) -> None:
        self.stub.script["/api/integration_lock"] = [(409, {"detail": "lock held"})]

        with self.assertRaises(ApiError) as caught:
            self.client().take_lock("a")

        self.assertEqual(409, caught.exception.status)
        self.assertEqual([], self.pauses)

    def test_a_403_or_404_during_a_rollout_is_retried_until_the_new_pod_answers(self) -> None:
        self.stub.script["/api/worker/state"] = [(404, {"detail": "not found"}), (403, {"detail": "no"}), (200, {"lease": None})]

        self.client().state("a")

        self.assertEqual([1.0, 2.0], self.pauses)

    def test_a_lasting_403_or_404_is_final_after_the_default_rollout_allowance(self) -> None:
        for status in (403, 404):
            with self.subTest(status=status):
                self.stub.script["/api/integration_lock"] = [(status, {"detail": "gone"})]
                self.stub.seen.clear()
                self.pauses.clear()

                with self.assertRaises(ApiError) as caught:
                    self.client().take_lock("a")

                self.assertEqual(status, caught.exception.status)
                self.assertEqual([1.0, 2.0, 4.0, 4.0, 4.0], self.pauses)
                self.assertEqual(6, len(self.stub.seen))

    def test_a_422_is_final_at_once(self) -> None:
        self.stub.script["/api/integration_lock"] = [(422, {"detail": "bad"})]

        with self.assertRaises(ApiError):
            self.client().take_lock("a")

        self.assertEqual([], self.pauses)

    def test_state_maps_every_checks_state(self) -> None:
        client = self.client()
        for wire, expected in [("success", ChecksState.PASSED), ("failure", ChecksState.FAILED), ("pending", ChecksState.PENDING), ("none", ChecksState.NONE)]:
            self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 3}, "checks": {"state": wire}})]
            self.assertEqual(expected, client.state("a").checks)

    def test_the_tenants_default_variant_rides_on_the_claim(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]

        self.assertEqual("model=x", self.client(default_variant="model=x").claim("a").variant)

    def test_state_maps_lock_and_checks(self) -> None:
        cases = [
            ({"holds": True, "position": 0, "offered": False}, LockState.HELD),
            ({"holds": False, "position": 1, "offered": True}, LockState.OFFERED),
            ({"holds": False, "position": 2, "offered": False}, LockState.QUEUED),
            ({"holds": False, "position": 0, "offered": False}, LockState.NONE),
        ]
        client = self.client()
        for lock, expected in cases:
            self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 3}, "ticket": {"status": "in_progress"}, "lock": lock, "checks": {"state": "unknown"}})]
            state = client.state("a")
            self.assertEqual((expected, ChecksState.PENDING, "in_progress", True), (state.lock, state.checks, state.ticket_status, state.lease_held))

    def test_state_carries_the_held_lease_to_resume(self) -> None:
        self.stub.script["/api/worker/state"] = [(200, {
            "lease": {"id": 3, "ticket_id": 871, "attempt": 2},
            "attempt": {"number": 2, "harness": "claude", "harness_session_id": "s9"},
        })]

        state = self.client().state("a")

        self.assertEqual((871, 2, "s9"), (state.ticket_id, state.attempt, state.session_id))

    def test_state_ignores_a_session_of_another_harness(self) -> None:
        self.stub.script["/api/worker/state"] = [(200, {
            "lease": {"id": 3, "ticket_id": 871, "attempt": 2},
            "attempt": {"harness": "pi", "harness_session_id": "s9"},
        })]

        self.assertIsNone(self.client().state("a").session_id)

    def test_state_carries_an_open_review_request(self) -> None:
        request = {"head_sha": "a" * 40, "intent": "Adds the thing.", "requested_at": "2026-11-18T10:00:00Z"}
        self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 3}, "review_request": request})]

        self.assertEqual(ReviewRequest("a" * 40, "Adds the thing.", "2026-11-18T10:00:00Z"), self.client().state("a").review_request)

    def test_a_review_request_without_intent_or_time_still_names_its_head(self) -> None:
        self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 3}, "review_request": {"head_sha": "a" * 40, "intent": 5}})]

        self.assertEqual(ReviewRequest("a" * 40, "", ""), self.client().state("a").review_request)

    def test_state_has_no_review_request_when_none_is_open_or_it_names_no_head(self) -> None:
        client = self.client()
        for answer in (
            {"lease": {"id": 3}},
            {"lease": {"id": 3}, "review_request": None},
            {"lease": {"id": 3}, "review_request": {"intent": "x"}},
            {"lease": None, "review_request": {"head_sha": "a" * 40}},
        ):
            self.stub.script["/api/worker/state"] = [(200, answer)]
            self.assertIsNone(client.state("a").review_request, answer)

    def test_release_lock_deletes_the_lock(self) -> None:
        self.stub.script["/api/integration_lock"] = [(200, {})]

        self.client().release_lock("a")

        self.assertEqual(("DELETE", "/api/integration_lock"), (self.stub.seen[0].method, self.stub.seen[0].path))

    def test_release_lock_treats_a_lock_already_gone_as_released_but_not_other_errors(self) -> None:
        client = self.client()
        for status in (404, 409):
            self.stub.script["/api/integration_lock"] = [(status, {"detail": "not held"})]
            client.release_lock("a")
        self.stub.script["/api/integration_lock"] = [(400, {"detail": "bad"})]
        with self.assertRaises(ApiError):
            client.release_lock("a")

    def test_poll_maps_events_and_passes_the_lease_it_watches(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]
        self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 11}})]
        self.stub.script["/api/worker/poll"] = [(200, {"events": [
            {"kind": "messages", "ticket_id": 7}, {"kind": "offered"}, {"kind": "restart"}, {"kind": "revoked", "lease_id": 11},
        ]})]
        client = self.client(version="v1")
        client.claim("a")

        events = client.poll("a", 5)

        self.assertEqual(
            [EventKind.MESSAGE, EventKind.LOCK_OFFER, EventKind.RESTART, EventKind.LEASE_REVOKED], [e.kind for e in events]
        )
        poll = next(s for s in self.stub.seen if s.path == "/api/worker/poll")
        self.assertEqual({"wait_seconds": 5, "protocol": SUPERVISOR_PROTOCOL, "version": "v1", "lease_id": 11}, poll.body)
        self.assertEqual(1, self.restarted)

    def test_every_request_names_the_protocol_in_a_header(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]
        client = self.client()

        client.claim("a")
        client.poll("a", 1)

        self.assertEqual({str(SUPERVISOR_PROTOCOL)}, {s.protocol for s in self.stub.seen})

    def test_an_unsupported_protocol_is_logged_readably_once_and_waited_out(self) -> None:
        message = "supervisor protocol 1 is no longer supported; run `slop-haus update`"
        self.stub.script["/api/worker/poll"] = [(200, {"events": [{"kind": "protocol_unsupported", "message": message}]})]
        client = self.client()

        with self.assertLogs("test", level="ERROR") as logs:
            first = client.poll("a", 5)
            second = client.poll("a", 5)

        self.assertEqual([], first + second)
        self.assertEqual([message], [r.getMessage() for r in logs.records], "once, however often it is told")
        self.assertEqual([5, 5], self.pauses, "it does not spin on a poll answered at once")

    def test_a_revoked_lease_is_not_watched_again(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]
        self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 11}})]
        self.stub.script["/api/worker/poll"] = [(200, {"events": [{"kind": "revoked", "lease_id": 11}]}), (200, {"events": []})]
        client = self.client()
        client.claim("a")
        client.poll("a", 0)

        client.poll("a", 0)

        polls = [s.body for s in self.stub.seen if s.path == "/api/worker/poll"]
        self.assertNotIn("lease_id", polls[1])

    def test_an_end_of_a_lease_we_no_longer_watch_is_ignored(self) -> None:
        self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]
        self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 12}})]
        self.stub.script["/api/worker/poll"] = [(200, {"events": [{"kind": "revoked", "lease_id": 11}]})]
        client = self.client()
        client.claim("a")

        self.assertEqual([], client.poll("a", 0))

    def test_a_disabled_server_is_waited_on_here(self) -> None:
        self.stub.script["/api/worker/poll"] = [(200, {"events": [{"kind": "disabled"}]})]

        self.assertEqual([Event(EventKind.DISABLED)], self.client().poll("a", 30))
        self.assertEqual([30.0], self.pauses)

    def test_escalate_without_a_lease_still_completes(self) -> None:
        self.stub.script["/api/claims/escalate"] = [(409, {"detail": "no lease"})]

        with self.assertLogs("test", logging.WARNING) as logs:
            self.client().escalate("a", "gave up")

        self.assertEqual({"note": "gave up"}, self.stub.seen[0].body)
        self.assertIn("gave up", logs.output[0])

    def test_release_sends_the_reason_and_completes_without_a_lease(self) -> None:
        self.stub.script["/api/claims/release"] = [(409, {"detail": "no lease"})]

        with self.assertLogs("test", logging.WARNING) as logs:
            self.client().release("a", "run.env refused")

        self.assertEqual("/api/claims/release", self.stub.seen[0].path)
        self.assertEqual({"reason": "run.env refused"}, self.stub.seen[0].body)
        self.assertIn("run.env refused", logs.output[0])

    def test_release_forgets_the_lease_so_the_next_poll_names_none(self) -> None:
        answers: list[tuple[int, dict[str, str]]] = [(200, {}), (409, {"detail": "no lease"})]
        for answer in answers:
            with self.subTest(status=answer[0]):
                self.stub.seen.clear()
                self.stub.script["/api/claims"] = [(200, {"ticket": {"id": 7}, "attempt": {"branch": "b"}})]
                self.stub.script["/api/worker/state"] = [(200, {"lease": {"id": 11}})]
                self.stub.script["/api/claims/release"] = [answer]
                self.stub.script["/api/worker/poll"] = [(200, {"events": []}), (200, {"events": []})]
                client = self.client()
                client.claim("a")
                client.poll("a", 1)

                with self.assertLogs("test", logging.WARNING) if answer[0] == 409 else contextlib.nullcontext():
                    client.release("a", "why")
                client.poll("a", 1)

                polls = [s.body for s in self.stub.seen if s.path == "/api/worker/poll"]
                self.assertEqual(11, polls[0]["lease_id"])
                self.assertNotIn("lease_id", polls[1])
                self.assertEqual(1, sum(1 for s in self.stub.seen if s.path == "/api/worker/state"), "no lease left to look up")

    def test_release_raises_on_another_refusal(self) -> None:
        self.stub.script["/api/claims/release"] = [(400, {"detail": "bad"})]

        with self.assertRaises(ApiError):
            self.client().release("a", "why")

    def test_usage_is_reported_per_model_as_session_totals_and_never_blocks(self) -> None:
        client = self.client()
        client.record_session("a", "claude", "sess")

        client.report_usage("a", Usage((used("claude-opus-5-5", input=10, output=5, cache_read=2, cache_write=1, requests=3), used("claude-sonnet-5-5", input=4))))
        client.report_usage("a", Usage((used("claude-opus-5-5", input=1, output=1),)))

        reports = [s.body for s in self.stub.seen if s.path == "/hooks/usage"]
        self.assertEqual("sess", reports[1]["transcript_key"])
        self.assertEqual(("supervisor", USAGE_REVISION), (reports[1]["source"], reports[1]["reporter_revision"]))
        opus, sonnet = reports[1]["records"]
        self.assertEqual(("claude-opus-5-5", 11, 6, 2, 1, 3), (opus["model"], opus["input_tokens"], opus["output_tokens"], opus["cache_read_tokens"], opus["cache_write_tokens"], opus["requests"]))
        self.assertEqual(("claude-sonnet-5-5", 4), (sonnet["model"], sonnet["input_tokens"]))
        for record in (opus, sonnet):
            self.assertEqual(("anthropic", "subscription", 1, "claude"), (record["provider"], record["billing_mode"], record["schema_version"], record["harness"]))
        session = next(s for s in self.stub.seen if s.path == "/api/claims/session")
        self.assertEqual({"harness": "claude", "session_id": "sess"}, session.body)

    def test_no_record_says_the_model_is_unknown(self) -> None:
        client = self.client()
        client.record_session("a", "claude", "sess")
        client.report_usage("a", Usage((used("claude-opus-5-5", input=1),)))
        records = [r for s in self.stub.seen if s.path == "/hooks/usage" for r in s.body["records"]]
        self.assertNotIn("unknown", {r["model"] for r in records})

    def test_a_metered_run_reports_its_cost_and_a_subscription_one_does_not(self) -> None:
        client = self.client()
        client.record_session("a", "claude", "sess")
        client.report_usage("a", Usage((used("m-metered", billing="metered", input=1, cost_micros=7), used("m-sub", input=1, cost_micros=7))))
        metered, sub = next(s for s in self.stub.seen if s.path == "/hooks/usage").body["records"]
        self.assertEqual(7, metered["reported_cost_microdollars"])
        self.assertNotIn("reported_cost_microdollars", sub)

    def test_a_failing_usage_endpoint_is_given_up_on(self) -> None:
        self.stub.script["/hooks/usage"] = [(503, {})]
        client = self.client()
        client.record_session("a", "claude", "sess")

        client.report_usage("a", Usage((used("claude-opus-5-5", input=1, output=1),)))

        self.assertEqual(1, len(self.pauses))

    def test_token_helper_failure_is_typed(self) -> None:
        with self.assertRaises(TokenUnavailable):
            SlotToken(Path("/x/token.sh"), ScriptedTokens([""])).get()

    def test_a_token_past_its_window_is_fetched_again_before_the_request(self) -> None:
        now = [0.0]
        runner = ScriptedTokens(["t1", "t2"])
        token = SlotToken(Path("/x/token.sh"), runner, max_age=30.0, monotonic=lambda: now[0])
        client = self.client(token=token)
        client.heartbeat("a")
        self.stub.refuse.add("Bearer t1")
        now[0] = 31.0

        with self.assertNoLogs("test", logging.DEBUG):
            client.heartbeat("a")

        self.assertEqual(["Bearer t1", "Bearer t2"], [s.auth for s in self.stub.seen])
        self.assertEqual(2, runner.forced)

    def test_a_token_inside_its_window_is_not_fetched_again(self) -> None:
        now = [0.0]
        runner = ScriptedTokens(["t1", "t2"])
        client = self.client(token=SlotToken(Path("/x/token.sh"), runner, max_age=30.0, monotonic=lambda: now[0]))

        client.heartbeat("a")
        now[0] = 29.0
        client.heartbeat("a")

        self.assertEqual(1, runner.forced)

    def test_one_401_refreshes_and_retries_at_once_without_a_warning(self) -> None:
        self.stub.refuse.add("Bearer t1")
        runner = ScriptedTokens(["t1", "t2"])
        client = self.client(runner)

        with self.assertNoLogs("test", logging.WARNING):
            client.heartbeat("a")

        self.assertEqual(["Bearer t1", "Bearer t2"], [s.auth for s in self.stub.seen])
        self.assertEqual(2, runner.forced)
        self.assertEqual([], self.pauses)

    def test_a_second_401_is_an_auth_error_and_does_not_loop(self) -> None:
        self.stub.refuse.update({"Bearer t1", "Bearer t2"})
        client = self.client(ScriptedTokens(["t1", "t2"]))

        with self.assertLogs("test", logging.ERROR) as logs, self.assertRaises(AuthRejected) as caught:
            client.poll("a", 0)

        self.assertEqual("auth_rejected", error_class(caught.exception))
        self.assertEqual(2, len(self.stub.seen))
        self.assertEqual([], self.pauses)
        self.assertEqual(1, len(logs.records))
        self.assertIn("a:", logs.output[0])

    def test_concurrent_401s_run_the_helper_once(self) -> None:
        runner = ScriptedTokens(["t1", "t2", "t3"])
        client = self.client(runner)
        client.heartbeat("a")
        self.stub.refuse.add("Bearer t1")
        self.stub.gate, self.stub.gate_auth = threading.Barrier(2), "Bearer t1"

        threads = [threading.Thread(target=client.heartbeat, args=("a",)) for _ in range(2)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(15)

        self.assertEqual(2, runner.forced)
        self.assertEqual(["Bearer t2", "Bearer t2"], [s.auth for s in self.stub.seen[-2:]])
