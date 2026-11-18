from __future__ import annotations

import json
import threading
import unittest
from collections.abc import Callable
from typing import Any, TypeVar

from slop_haus_supervisor.remote_control.repo.working_copy import MAX_REPO_PATH_BYTES, MAX_REPO_PATHS
from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.remote_control.wire.reporter import MAX_DELTA_BYTES, MAX_FAILURE_BYTES, Reporter
from wire_fixtures import CONV, FAILSAFE, FakeTokens, StubServer


class EventStore:
    """Enough of the server's rules for the reporter's tests: events are
    stored once and in order, a gap is a 409 saying what the server has."""

    def __init__(self) -> None:
        self.stored: list[dict[str, Any]] = []
        self.acked = 0
        self.fail_next: list[Any] = []
        self.lock = threading.Lock()

    def __call__(self, path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
        if path == "/hooks/usage":
            return {"scope": "ticket"}
        with self.lock:
            if self.fail_next:
                return self.fail_next.pop(0)
            for event in body["events"]:
                if event["host_seq"] <= self.acked:
                    continue
                if event["host_seq"] != self.acked + 1:
                    return 409, {"detail": "a report skips events: the server has up to host_seq %d, send from the next" % self.acked}
                self.stored.append(event)
                self.acked = event["host_seq"]
            return {"acked": self.acked}

_T = TypeVar("_T")


class ReporterTest(unittest.TestCase):
    def serve(self, respond: Callable[[str, dict[str, Any], dict[str, str]], Any]) -> StubServer:
        server = StubServer(respond)
        self.addCleanup(server.close)
        return server

    def reporter(self, server: StubServer, **kwargs: Any) -> Any:
        stop = threading.Event()
        self.addCleanup(stop.set)
        r = Reporter(Client(server.url, FakeTokens()), "host-1", stop, min_interval=0, retry_delay=0.001,
                                 max_backoff=0.01, **kwargs)
        r.start()
        return r

    def events_of(self, store: EventStore) -> list[Any]:
        with store.lock:
            return [(e["host_seq"], e["kind"], e.get("text") or e.get("reason")) for e in store.stored]

    def test_text_is_merged_within_a_block_and_kept_apart_between_blocks(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        reporter.text(CONV, "Checking ", "m1")
        reporter.text(CONV, "pilot-3:", "m1")
        reporter.text(CONV, "Next.", "m2")
        reporter.text(CONV, " More", "m2")
        reporter.text(CONV, "Old", "")
        token = reporter.text(CONV, " text", "")
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        with store.lock:
            sent = [(e["text"], e.get("block")) for e in store.stored]
        self.assertEqual([("Checking pilot-3:", "m1"), ("Next. More", "m2"), ("Old text", None)], sent)

    def test_a_block_id_is_kept_within_what_the_server_takes(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.text(CONV, "x", "m" * 500)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        with store.lock:
            self.assertEqual(128, len(store.stored[0]["block"]))

    def test_an_activity_is_sent_between_the_texts_around_it_and_carries_only_its_line(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        reporter.text(CONV, "before", "m1")
        reporter.activity(CONV, "t1", "Bash", "Check pilot-3", "running")
        token = reporter.text(CONV, "after", "m1")
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        with store.lock:
            stored = list(store.stored)
        self.assertEqual(["assistant", "activity", "assistant"], [e["kind"] for e in stored])
        self.assertEqual({"call_id": "t1", "tool": "Bash", "line": "Check pilot-3", "status": "running"}, stored[1]["activity"])

    def test_events_are_delivered_in_order_numbered_from_what_the_server_has(self) -> None:
        store = EventStore()
        store.acked = 7  # a host that restarted: the server is ahead of it
        server = self.serve(store)
        reporter = self.reporter(server)
        reporter.text(CONV, "hello")
        token = reporter.ready(CONV, 3)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([(8, "assistant", "hello"), (9, "ready", None)], self.events_of(store))
        self.assertEqual([], server.requests[0]["body"]["events"], "the first report only asks where the server is")
        self.assertEqual("host-1", server.requests[0]["body"]["host_id"])
        ready = store.stored[-1]
        self.assertEqual(3, ready["work_seq"])

    def test_draining_an_idle_reporter_after_stop_returns_at_once(self) -> None:
        """Ctrl-C sets stop from a signal handler; the host then drains. An idle
        reporter must wake for it rather than hold the exit for the join's 40s."""
        asleep = threading.Event()

        class WatchedCondition(threading.Condition):
            def wait_for(self, predicate: Callable[[], _T], timeout: float | None = None) -> _T:
                def watched() -> _T:
                    ready = predicate()
                    if not ready:
                        asleep.set()
                    return ready
                return super().wait_for(watched, timeout)

        stop = threading.Event()
        reporter = Reporter(Client(self.serve(EventStore()).url, FakeTokens()), "host-1", stop,
                                        min_interval=0)
        reporter.cond = WatchedCondition()
        reporter.start()
        self.assertTrue(asleep.wait(FAILSAFE))
        # Under the lock, so the reporter is inside wait(), past its last look
        # at stop: only a notify can wake it now.
        with reporter.cond:
            stop.set()
        drained = threading.Event()
        threading.Thread(target=lambda: (reporter.drain(), drained.set()), daemon=True).start()
        self.assertTrue(drained.wait(FAILSAFE), "drain waited out its join")
        self.assertFalse(reporter.thread.is_alive())

    def test_a_drain_that_times_out_does_not_flush_beside_the_reporter_thread(self) -> None:
        """The reporter thread is still inside a post when the drain's join gives
        up: a second flush would send the same in-flight batch concurrently."""
        entered = threading.Event()
        release = threading.Event()
        lock = threading.Lock()
        active = 0
        peak = 0

        class BlockingClient:
            def post(self, path: str, body: dict[str, Any], timeout: float) -> dict[str, Any]:
                nonlocal active, peak
                if not body["events"]:
                    return {"acked": 0}
                with lock:
                    active += 1
                    peak = max(peak, active)
                try:
                    if not entered.is_set():
                        entered.set()
                        release.wait(FAILSAFE)
                    return {"acked": len(body["events"])}
                finally:
                    with lock:
                        active -= 1

        stop = threading.Event()
        reporter = Reporter(BlockingClient(), "host-1", stop, min_interval=0, drain_timeout=0.05)
        self.addCleanup(release.set)
        self.addCleanup(stop.set)
        reporter.start()
        reporter.text(CONV, "hello")
        self.assertTrue(entered.wait(FAILSAFE))
        stop.set()
        reporter.drain()
        release.set()
        reporter.thread.join(FAILSAFE)
        self.assertFalse(reporter.thread.is_alive())
        self.assertEqual(1, peak, "two flushes were in a post at once")

    def test_a_repo_state_goes_out_with_its_state_and_its_turn(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        repo = {"branch": "main", "head": "a" * 40, "changed_paths": ["x.go"]}
        token = reporter.repo_state(CONV, repo, 7, 4)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([{"host_seq": 1, "kind": "repo_state", "turn": 4, "repo": repo, "work_seq": 7}], store.stored)

    def test_context_events_carry_the_token_count_and_the_cleared_divider_nothing(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        reporter.context(CONV, 1234)
        token = reporter.context_cleared(CONV)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([{"host_seq": 1, "kind": "context", "context_tokens": 1234}, {"host_seq": 2, "kind": "context_cleared"}], store.stored)

    def test_the_compacted_divider_carries_nothing(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.context_compacted(CONV)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([{"host_seq": 1, "kind": "context_compacted"}], store.stored)

    def test_an_unsupported_work_kind_goes_out_as_a_reason_inside_the_servers_limit(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.unsupported(CONV, "x" * 100)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([{"host_seq": 1, "kind": "unsupported", "reason": "x" * 40}], store.stored)

    def test_an_unsupported_report_always_names_a_kind_as_the_server_refuses_an_empty_one(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.unsupported(CONV, "")
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual("unknown", store.stored[0]["reason"])

    def test_a_batch_stays_inside_the_servers_body_limit(self) -> None:
        """200 repo states of 40KB each, or 12MB of text, would be refused whole
        by the server for ever; the host sends them in batches that fit."""
        store = EventStore()
        server = self.serve(store)
        reporter = self.reporter(server)
        paths = ["p" * MAX_REPO_PATH_BYTES] * MAX_REPO_PATHS
        for _ in range(60):
            reporter.repo_state(CONV, {"branch": "b", "head": "a" * 40, "changed_paths": paths}, 1, 1)
        token = reporter.text(CONV, "x" * (3 * 1024 * 1024))
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        sent = [r for r in server.requests if r["body"].get("events")]
        self.assertGreater(len(sent), 3)
        for request in sent:
            self.assertLess(len(json.dumps(request["body"])), 1024 * 1024)
        self.assertEqual(60, len([e for e in store.stored if e["kind"] == "repo_state"]), "and nothing is lost")
        self.assertEqual(3 * 1024 * 1024, sum(len(e["text"]) for e in store.stored if e["kind"] == "assistant"))

    def test_text_while_a_report_is_in_flight_goes_out_as_one_event(self) -> None:
        store = EventStore()
        release = threading.Event()
        first_posted = threading.Event()

        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if body.get("events"):
                first_posted.set()
                release.wait(FAILSAFE)
            return store(path, body, headers)

        server = self.serve(respond)
        reporter = self.reporter(server)
        reporter.text(CONV, "a")
        self.assertTrue(first_posted.wait(FAILSAFE))
        for part in ("b", "c", "d"):
            reporter.text(CONV, part)
        token = reporter.turn_end(CONV, 1)
        release.set()
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([(1, "assistant", "a"), (2, "assistant", "bcd"), (3, "turn_end", None)], self.events_of(store))

    def test_text_over_the_servers_limit_is_split(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.text(CONV, "x" * (MAX_DELTA_BYTES + 10))
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([MAX_DELTA_BYTES, 10], [len(e["text"]) for e in store.stored])

    def test_a_report_that_failed_is_sent_again_exactly_as_it_was(self) -> None:
        """The server skips what it has by number, so text merged into the
        resend would be dropped as already stored."""
        store = EventStore()
        failed_once = threading.Event()

        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if body.get("events") and not failed_once.is_set():
                failed_once.set()
                return 500, {"detail": "boom"}
            return store(path, body, headers)

        server = self.serve(respond)
        reporter = self.reporter(server)
        reporter.text(CONV, "one")
        self.assertTrue(failed_once.wait(FAILSAFE))
        reporter.text(CONV, "two")
        token = reporter.turn_end(CONV, 1)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        posts = [r["body"]["events"] for r in server.requests if r["body"].get("events")]
        self.assertEqual(posts[0], posts[1][: len(posts[0])], "the failed batch went again unchanged")
        self.assertEqual([(1, "assistant", "one"), (2, "assistant", "two"), (3, "turn_end", None)], self.events_of(store))

    def test_a_gap_is_renumbered_from_what_the_server_says_it_has(self) -> None:
        store = EventStore()
        server = self.serve(store)
        reporter = self.reporter(server)
        first = reporter.text(CONV, "one")
        self.assertTrue(reporter.wait_delivered(CONV, first, FAILSAFE))
        store.acked = 0  # the server lost them (a restored backup, say): it is behind the host
        with store.lock:
            store.stored.clear()
        token = reporter.text(CONV, "two")
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([(1, "assistant", "two")], self.events_of(store))

    def test_events_the_server_refuses_for_good_are_dropped_and_the_rest_go_on(self) -> None:
        store = EventStore()
        store.fail_next = []
        server = self.serve(store)
        reporter = self.reporter(server)
        first = reporter.text(CONV, "one")
        self.assertTrue(reporter.wait_delivered(CONV, first, FAILSAFE))
        store.fail_next = [(422, {"detail": "bad"})]
        dropped = reporter.text(CONV, "bad one")
        self.assertTrue(reporter.wait_delivered(CONV, dropped, FAILSAFE))
        last = reporter.text(CONV, "fine")
        self.assertTrue(reporter.wait_delivered(CONV, last, FAILSAFE))
        self.assertEqual([(1, "assistant", "one"), (2, "assistant", "fine")], self.events_of(store))

    def test_a_failed_reason_is_cleaned_and_bounded(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.failed(CONV, "clone of https://u:pw@h/x failed " + "y" * 5000, 2)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        (event,) = store.stored
        self.assertNotIn("pw", event["reason"])
        self.assertLessEqual(len(event["reason"].encode()), MAX_FAILURE_BYTES)

    def test_usage_is_posted_with_the_latest_totals(self) -> None:
        store = EventStore()
        server = self.serve(store)
        reporter = self.reporter(server)
        body = {"conversation_id": CONV, "records": [{"input_tokens": 5}]}
        reporter.report_usage(CONV, body)
        server.wait_for(lambda reqs: any(r["path"] == "/hooks/usage" for r in reqs))
        (post,) = server.to("/hooks/usage")
        self.assertEqual(body, post["body"])

    def test_usage_with_more_records_than_the_server_takes_is_posted_in_parts(self) -> None:
        store = EventStore()
        server = self.serve(store)
        reporter = self.reporter(server)
        records = [{"model": f"m{i:03d}"} for i in range(70)]
        reporter.report_usage(CONV, {"transcript_key": CONV, "conversation_id": CONV, "records": records})
        server.wait_for(lambda reqs: len([r for r in reqs if r["path"] == "/hooks/usage"]) >= 3)
        posts = server.to("/hooks/usage")
        self.assertEqual([32, 32, 6], [len(p["body"]["records"]) for p in posts])
        # The server keeps the last report per transcript_key, so what it ends
        # up holding is the last post of each key.
        stored = {p["body"]["transcript_key"]: p["body"]["records"] for p in posts}
        self.assertEqual(3, len(stored))
        self.assertEqual(CONV, posts[0]["body"]["transcript_key"])
        self.assertTrue(all(k.startswith(CONV + ":") for k in list(stored)[1:]))
        self.assertEqual(records, [r for recs in stored.values() for r in recs])
        self.assertEqual({CONV}, {p["body"]["conversation_id"] for p in posts})

    def test_a_usage_report_the_server_refuses_is_not_repeated(self) -> None:
        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if path == "/hooks/usage":
                return 422, {"detail": "nope"}
            return {"acked": 0}

        server = self.serve(respond)
        reporter = self.reporter(server)
        reporter.report_usage(CONV, {"conversation_id": CONV, "records": []})
        server.wait_for(lambda reqs: any(r["path"] == "/hooks/usage" for r in reqs))
        # Reporting carries on after it, which it could not if it were retried
        # in a loop: later text is still sent.
        reporter.text(CONV, "x")
        server.wait_for(lambda reqs: any(r["body"].get("events") for r in reqs))
        reporter.text(CONV, "y")
        server.wait_for(lambda reqs: len([r for r in reqs if r["body"].get("events")]) >= 2)
        self.assertEqual(1, len(server.to("/hooks/usage")))

    def test_a_usage_report_the_server_could_not_take_yet_is_sent_again(self) -> None:
        store = EventStore()
        refused_once = threading.Event()

        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if path == "/hooks/usage" and not refused_once.is_set():
                refused_once.set()
                return 503, {"detail": "busy"}
            return store(path, body, headers)

        server = self.serve(respond)
        reporter = self.reporter(server)
        reporter.report_usage(CONV, {"conversation_id": CONV, "records": []})
        server.wait_for(lambda reqs: len([r for r in reqs if r["path"] == "/hooks/usage"]) >= 2)

    def test_a_gap_keeps_only_what_the_server_lacks_when_it_has_part_of_the_batch(self) -> None:
        store = EventStore()
        gapped = threading.Event()

        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if body.get("events") and not gapped.is_set():
                gapped.set()
                store.acked = 2  # the server stored the first two of the batch before the report was lost
                with store.lock:
                    store.stored.extend({"host_seq": n, "kind": "turn_end", "turn": n} for n in (1, 2))
                return 409, {"detail": "a report skips events: the server has up to host_seq 2, send from the next"}
            return store(path, body, headers)

        server = self.serve(respond)
        reporter = Reporter(Client(server.url, FakeTokens()), "host-1", threading.Event(), min_interval=0)
        for turn in (1, 2, 3):
            reporter.turn_end(CONV, turn)
        reporter.flush_once()
        self.assertEqual([(1, "turn_end", None), (2, "turn_end", None), (3, "turn_end", None)], self.events_of(store))

    def test_text_is_merged_only_while_the_event_stays_inside_the_servers_limit(self) -> None:
        reporter = Reporter(Client("http://unused", FakeTokens()), "host-1", threading.Event())
        reporter.next_seq[CONV] = 1
        half = MAX_DELTA_BYTES // 2 + 1
        reporter.text(CONV, "a" * half)
        reporter.text(CONV, "b" * half)
        reporter.text(CONV, "c")
        batch = reporter.take_batch(CONV)
        self.assertEqual([half, half + 1], [len(o.text) for _, o in batch])

    def test_a_turn_end_carries_its_turn_number(self) -> None:
        store = EventStore()
        reporter = self.reporter(self.serve(store))
        token = reporter.turn_end(CONV, 7)
        self.assertTrue(reporter.wait_delivered(CONV, token, FAILSAFE))
        self.assertEqual([{"host_seq": 1, "kind": "turn_end", "turn": 7}], store.stored)

    def test_only_the_newest_usage_report_is_kept(self) -> None:
        reporter = Reporter(Client("http://unused", FakeTokens()), "host-1", threading.Event())
        reporter.report_usage(CONV, {"records": [{"input_tokens": 1}]})
        newest = {"records": [{"input_tokens": 2}]}
        reporter.report_usage(CONV, newest)
        self.assertEqual({CONV: newest}, reporter.usage)
