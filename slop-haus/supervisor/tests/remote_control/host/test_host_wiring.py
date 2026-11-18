from __future__ import annotations

import ast
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path
from typing import Any
from unittest import mock

from host_fixtures import CONV, FAILSAFE, FAKE_HARNESS, SERVER, git, make_harness
from slop_haus_supervisor.remote_control.host import main as main_module
from slop_haus_supervisor.remote_control.host.host import Host, is_uuid
from slop_haus_supervisor.remote_control.host.poller import Poller, host_version
from slop_haus_supervisor.remote_control.host.settings import Settings
from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.remote_control.wire.reporter import Reporter
from wire_fixtures import FakeTokens, Responder, StubServer

SRC = Path(__file__).resolve().parents[3] / "src" / "slop_haus_supervisor"


class ServerCase(unittest.TestCase):
    def serve(self, respond: Responder) -> StubServer:
        server = StubServer(respond)
        self.addCleanup(server.close)
        return server


class PollerTest(ServerCase):
    def test_hands_over_what_is_not_acknowledged_with_the_hosts_ack(self) -> None:
        replies = iter([
            {"reason": "work", "acked": 0, "items": [{"seq": 1, "type": "start", "conversation": CONV},
                                                      {"seq": 2, "type": "message", "conversation": CONV, "text": "hi"}]},
            # the host has acknowledged 1 only: 2 is sent again
            {"reason": "work", "acked": 1, "items": [{"seq": 2, "type": "message", "conversation": CONV, "text": "hi"},
                                                      {"seq": 3, "type": "stop", "conversation": CONV}]},
        ])
        server = self.serve(lambda _p, _b, _h: next(replies))
        host = mock.Mock()
        host.capabilities.side_effect = list
        poller = Poller(Client(server.url, FakeTokens()), "host-1", host, wait_seconds=0, version="1.2.3")

        host.current_ack.return_value = 0
        first = poller.poll_once()
        host.current_ack.return_value = 1
        second = poller.poll_once()

        self.assertEqual([1, 2], [i.seq for i in first])
        self.assertEqual([2, 3], [i.seq for i in second])
        self.assertEqual(
            [{"host_id": "host-1", "ack": 0, "wait_seconds": 0, "capabilities": ["interrupt"], "version": "1.2.3"},
             {"host_id": "host-1", "ack": 1, "wait_seconds": 0, "capabilities": ["interrupt"], "version": "1.2.3"}],
            [r["body"] for r in server.requests],
        )
        self.assertEqual("hi", first[1].raw["text"])

    def test_the_version_is_the_build_id_install_app_recorded_beside_the_app(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / ".slop-haus"
            module = root / "app" / "src" / "pkg" / "poller.py"
            module.parent.mkdir(parents=True)
            module.touch()
            self.assertEqual("", host_version(module), "no app.version yet")
            (root / "app.version").write_text("ab12cd34ef56\n", encoding="utf-8")
            self.assertEqual("ab12cd34ef56", host_version(module))
            (root / "app.version").write_text("", encoding="utf-8")
            self.assertEqual("", host_version(module))

    def test_the_poll_reports_the_version_host_version_finds(self) -> None:
        server = self.serve(lambda _p, _b, _h: {"items": []})
        host = mock.Mock()
        host.capabilities.side_effect = list
        host.current_ack.return_value = 0
        with mock.patch("slop_haus_supervisor.remote_control.host.poller.host_version", return_value="ab12cd34ef56"):
            Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=0).poll_once()
        self.assertEqual(["ab12cd34ef56"], [r["body"]["version"] for r in server.requests])

    def test_the_version_is_empty_for_code_that_is_not_the_installed_app(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            module = Path(tmp) / "checkout" / "src" / "poller.py"
            module.parent.mkdir(parents=True)
            module.touch()
            (Path(tmp) / "app.version").write_text("ab12cd34ef56\n", encoding="utf-8")
            self.assertEqual("", host_version(module))

    def test_a_reply_that_is_not_a_list_of_items_is_no_items(self) -> None:
        server = self.serve(lambda _p, _b, _h: {"items": [1, "x", {"seq": "no"}, {"seq": 5, "type": "stop", "conversation": CONV}]})
        host = mock.Mock()
        host.capabilities.side_effect = list
        host.current_ack.return_value = 0
        poller = Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=0)
        self.assertEqual([5], [i.seq for i in poller.poll_once()], "only well-formed items")

    def test_an_items_field_that_is_not_a_list_is_no_items(self) -> None:
        for reply in ({"items": {"seq": 5}}, {"items": "x"}, {"items": None}, {}):
            server = self.serve(lambda _p, _b, _h, reply=reply: reply)
            host = mock.Mock()
            host.capabilities.side_effect = list
            host.current_ack.return_value = 0
            poller = Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=0)
            self.assertEqual([], poller.poll_once(), reply)

    def test_a_refused_credential_stops_the_host(self) -> None:
        server = self.serve(lambda _p, _b, _h: (403, {"detail": "only a conductor's host may do that"}))
        host = mock.Mock()
        host.capabilities.side_effect = list
        host.stopping = threading.Event()
        host.current_ack.return_value = 0
        poller = Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=0)
        poller.run()
        self.assertTrue(host.stopping.is_set())

    def test_the_lease_is_refreshed_while_a_batch_is_being_handled(self) -> None:
        """A batch can take longer than the server's host timeout; a silent host loses its claim."""
        host = mock.Mock()
        host.capabilities.side_effect = list
        host.stopping = threading.Event()
        host.batch_done = threading.Event()  # the host never finishes the batch on its own
        host.current_ack.return_value = 0
        polls: list[int] = []

        def respond(_path: str, body: dict[str, Any], _headers: dict[str, str]) -> Any:
            polls.append(body["wait_seconds"])
            if len(polls) == 1:
                return {"items": [{"seq": 1, "type": "stop", "conversation": CONV}]}
            if len(polls) >= 3:
                host.stopping.set()
                host.batch_done.set()
            return {"items": [{"seq": 1, "type": "stop", "conversation": CONV}]}

        server = self.serve(respond)
        poller = Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=7, lease_refresh=0.01)
        runner = threading.Thread(target=poller.run, daemon=True)
        runner.start()
        runner.join(FAILSAFE)
        self.assertFalse(runner.is_alive(), "the poller never went on polling while the batch was handled")
        self.assertEqual([7, 0, 0], polls[:3], "the refresh polls do not wait")
        host.offer.assert_called_once()

    def test_a_failed_lease_refresh_is_not_fatal(self) -> None:
        server = self.serve(lambda _p, _b, _h: (500, {"detail": "boom"}))
        host = mock.Mock()
        host.capabilities.side_effect = list
        host.stopping = threading.Event()
        host.current_ack.return_value = 0
        poller = Poller(Client(server.url, FakeTokens()), "h", host, wait_seconds=0)
        poller.refresh_lease()
        self.assertFalse(host.stopping.is_set())


class EventStore:
    """Enough of the server's rules for the end-to-end test: events are stored once and in order."""

    def __init__(self) -> None:
        self.stored: list[dict[str, Any]] = []
        self.acked = 0
        self.lock = threading.Lock()

    def __call__(self, path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
        with self.lock:
            for event in body["events"]:
                if event["host_seq"] == self.acked + 1:
                    self.stored.append(event)
                    self.acked = event["host_seq"]
            return {"acked": self.acked}


class EndToEndTest(ServerCase):
    """The whole host -- poller, reporter, client, harness, checkout -- against a stub of slop-haus
    and the fake harness: a conversation from start to a reply, with its usage."""

    def test_a_conversation(self) -> None:
        tmp = tempfile.mkdtemp(prefix="remote-control-host-e2e-")
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        origin, root, logs = (os.path.join(tmp, n) for n in ("origin", "workspace", "logs"))
        for d in (origin, logs):
            os.makedirs(d)
        git(origin, "init", "-q", "-b", "main")
        with open(os.path.join(origin, "a.txt"), "w") as f:
            f.write("one")
        git(origin, "add", "a.txt")
        env = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        with mock.patch.dict(os.environ, env):
            git(origin, "commit", "-q", "-m", "one")
        subprocess.run(["git", "clone", "-q", origin, root], check=True)
        os.makedirs(os.path.join(root, ".slop-haus"))
        with open(os.path.join(root, ".git", "info", "exclude"), "a") as f:
            f.write(".slop-haus/\n")
        script = os.path.join(tmp, "fake_harness.py")
        with open(script, "w") as f:
            f.write(FAKE_HARNESS)

        queue_items: list[dict[str, Any]] = [
            {"seq": 1, "type": "start", "conversation": CONV},
            {"seq": 2, "type": "message", "conversation": CONV, "message_seq": 1, "text": "what is in a.txt?"},
        ]
        store = EventStore()
        state = {"acked": 0}
        replied = threading.Event()

        def respond(path: str, body: dict[str, Any], headers: dict[str, str]) -> Any:
            if path == "/api/conductor/poll":
                state["acked"] = max(state["acked"], body["ack"])
                items = [i for i in queue_items if i["seq"] > state["acked"]]
                return {"reason": "work" if items else "timeout", "acked": state["acked"], "items": items}
            if path == "/hooks/usage":
                replied.set()
                return {"scope": "ticket", "ticket_id": 7}
            return store(path, body, headers)

        server = self.serve(respond)
        with mock.patch.dict(os.environ, {**env, "FAKE_HARNESS_DIR": logs}):
            stop = threading.Event()
            client = Client(server.url, FakeTokens())
            settings = Settings(poll_wait=0, min_report_interval=0)
            reporter = Reporter(client, "host-e2e", stop, min_interval=0, retry_delay=0.001, max_backoff=0.01)
            host = Host(root, reporter, make_harness(root, script), settings, stopping=stop)
            poller = Poller(client, "host-e2e", host, wait_seconds=0)
            reporter.start()
            poller.start()
            runner = threading.Thread(target=host.run)
            runner.start()
            self.assertTrue(replied.wait(FAILSAFE), "usage never arrived")
            stop.set()
            runner.join(FAILSAFE)

        # The reporter merges streamed text only when it is still queued, so a flush between two
        # deltas sends the reply as two events: fold consecutive ones back into one reply.
        events: list[dict[str, Any]] = []
        for e in store.stored:
            if events and e["kind"] == "assistant" and events[-1]["kind"] == "assistant":
                events[-1] = {**events[-1], "text": events[-1]["text"] + e["text"]}
            else:
                events.append(e)
        self.assertEqual(["ready", "repo_state", "assistant", "turn_end", "repo_state", "context"], [e["kind"] for e in events])
        self.assertEqual([e["host_seq"] for e in store.stored], list(range(1, len(store.stored) + 1)))
        self.assertEqual("echo: what is in a.txt?", events[2]["text"])
        self.assertEqual(1, store.stored[0]["work_seq"])
        usage = [r for r in server.requests if r["path"] == "/hooks/usage"][-1]
        self.assertEqual(CONV, usage["body"]["conversation_id"])
        self.assertEqual(100, usage["body"]["records"][0]["input_tokens"])


class MainTest(unittest.TestCase):
    def setUp(self) -> None:
        self.workspace = tempfile.mkdtemp(prefix="remote-control-main-")
        self.addCleanup(shutil.rmtree, self.workspace, ignore_errors=True)
        os.makedirs(os.path.join(self.workspace, ".slop-haus"))
        env = mock.patch.dict(os.environ, {"SLOP_HAUS_URL": SERVER})
        env.start()
        self.addCleanup(env.stop)
        self.mocks: dict[str, mock.MagicMock] = {}
        for name in ("Host", "Poller", "Reporter", "Client", "SlotToken", "HostLease"):
            patcher = mock.patch.object(main_module, name)
            self.mocks[name] = patcher.start()
            self.addCleanup(patcher.stop)
        signals = mock.patch.object(main_module.signal, "signal")
        signals.start()
        self.addCleanup(signals.stop)
        self.which = mock.patch("slop_haus_supervisor.harness.claude.shutil.which", return_value="/bin/claude")
        self.which.start()
        self.addCleanup(self.which.stop)

    def run_main(self, *args: str) -> int:
        return main_module.main(["--workspace", self.workspace, *args])

    def test_runs_in_the_workspace_it_is_given(self) -> None:
        self.assertEqual(0, self.run_main())
        self.assertEqual(str(Path(self.workspace).resolve()), self.mocks["Host"].call_args.args[0])

    def test_the_credential_is_the_workspaces_own_token_script(self) -> None:
        self.run_main()
        token_script = os.path.join(Path(self.workspace).resolve(), ".slop-haus", "token.sh")
        token = self.mocks["SlotToken"].call_args
        self.assertEqual(token_script, str(token.args[0]))
        self.assertEqual({"SLOP_HAUS_URL": SERVER}, token.kwargs["env"])

    def test_the_idle_timeout_is_passed_on(self) -> None:
        self.run_main("--idle-timeout", "12")
        self.assertEqual(12.0, self.mocks["Host"].call_args.args[3].idle_timeout)

    def test_a_stopped_host_releases_after_the_reporter_has_drained(self) -> None:
        calls = mock.Mock()
        calls.attach_mock(self.mocks["Host"].return_value.run, "run")
        calls.attach_mock(self.mocks["Reporter"].return_value.drain, "drain")
        calls.attach_mock(self.mocks["Client"].return_value.release, "release")
        self.assertEqual(0, self.run_main())
        host_id = self.mocks["Reporter"].call_args.args[1]
        self.assertEqual(host_id, self.mocks["Poller"].call_args.args[1], "the host releases the id it polled with")
        self.assertEqual([mock.call.run(), mock.call.drain(), mock.call.release(host_id)], calls.mock_calls)

    def test_the_lease_is_taken_before_serving_started_with_the_poller_and_released_last(self) -> None:
        calls = mock.Mock()
        lease = self.mocks["HostLease"].return_value
        calls.attach_mock(lease.acquire, "acquire")
        calls.attach_mock(lease.start, "lease_start")
        calls.attach_mock(self.mocks["Poller"].return_value.start, "poll_start")
        calls.attach_mock(self.mocks["Host"].return_value.run, "run")
        calls.attach_mock(self.mocks["Client"].return_value.release, "release")
        calls.attach_mock(lease.release, "lease_release")
        self.assertEqual(0, self.run_main())
        names = [c[0] for c in calls.mock_calls]
        self.assertEqual(["acquire", "lease_start", "poll_start", "run", "release", "lease_release"], names)

    def test_a_refused_lease_ends_the_host_before_it_serves(self) -> None:
        self.mocks["HostLease"].return_value.acquire.side_effect = main_module.LeaseRefused("another host")
        with self.assertLogs(main_module.log, "ERROR"):
            self.assertEqual(1, self.run_main())
        self.mocks["Host"].return_value.run.assert_not_called()
        self.mocks["Poller"].return_value.start.assert_not_called()

    def test_no_server_address_is_refused(self) -> None:
        with mock.patch.dict(os.environ, {"SLOP_HAUS_URL": ""}):
            self.assertEqual(1, self.run_main())

    def test_the_server_address_can_come_from_the_workspace(self) -> None:
        with open(os.path.join(self.workspace, ".slop-haus", "server-url"), "w") as f:
            f.write("https://from-file.example\n")
        with mock.patch.dict(os.environ, {"SLOP_HAUS_URL": ""}):
            self.run_main()
        self.assertEqual("https://from-file.example", self.mocks["Client"].call_args.args[0])

    def test_a_machine_without_claude_is_refused(self) -> None:
        self.which.stop()
        with mock.patch("slop_haus_supervisor.harness.claude.shutil.which", return_value=None):
            self.assertEqual(127, self.run_main())
        self.which.start()


class IsolationTest(unittest.TestCase):
    def test_nothing_outside_the_remote_control_imports_it(self) -> None:
        """The host holds the owner-bound conductor credential: no other part of the package, the
        supervisor loop above all, may reach it."""
        importers: list[str] = []
        for path in SRC.rglob("*.py"):
            if "remote_control" in path.relative_to(SRC).parts:
                continue
            for node in ast.walk(ast.parse(path.read_text())):
                names = [a.name for a in node.names] if isinstance(node, ast.Import) else \
                    [node.module or ""] + [a.name for a in node.names] if isinstance(node, ast.ImportFrom) else []
                if any("remote_control" in n for n in names):
                    importers.append(str(path))
        self.assertEqual([], importers)

    def test_the_script_is_registered(self) -> None:
        pyproject = (SRC.parents[1] / "pyproject.toml").read_text()
        self.assertIn('slop-haus-remote-control = "slop_haus_supervisor.remote_control.host.main:main"', pyproject)

    def test_only_uuids_are_conversation_ids(self) -> None:
        self.assertTrue(is_uuid(CONV))
        for bad in ("", "../x", "a" * 36, CONV + "/x"):
            self.assertFalse(is_uuid(bad), bad)
