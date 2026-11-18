from __future__ import annotations

import http.server
import json
import tempfile
import threading
import unittest
from pathlib import Path

from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.setup.grant import GrantState, SupervisorGrant
from slop_haus_supervisor.setup.host_actions import HostActions
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.layout import Layout
from fakes import FakeRunner


class Server:
    def __init__(self, status: int) -> None:
        self.paths: list[str] = []
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                outer.paths.append(self.path)
                payload = json.dumps({"slots": [], "detail": "nope"}).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                pass

        self._http = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self._http.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self._http.server_port}"

    def close(self) -> None:
        self._http.shutdown()
        self._http.server_close()


class SupervisorGrantTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))

    def store(self) -> None:
        dot = self.layout.supervisor_dir / ".slop-haus"
        dot.mkdir(parents=True)
        (dot / "credentials.json").write_text("{}")

    def state(self, server: str, token: ProcessResult) -> GrantState:
        return SupervisorGrant(self.layout, server, FakeRunner(lambda argv: token)).state()

    def serve(self, status: int) -> Server:
        server = Server(status)
        self.addCleanup(server.close)
        return server

    def test_no_credential_file_is_missing_without_asking_the_server(self) -> None:
        server = self.serve(200)

        self.assertEqual(GrantState.MISSING, self.state(server.url, ProcessResult(0, "t", "")))
        self.assertEqual([], server.paths)

    def test_a_credential_the_server_accepts_is_active(self) -> None:
        self.store()
        server = self.serve(200)

        self.assertEqual(GrantState.ACTIVE, self.state(server.url, ProcessResult(0, "t", "")))
        self.assertEqual(["/api/supervisor/view"], server.paths)

    def test_a_token_the_server_refuses_is_revoked(self) -> None:
        self.store()
        for status in (401, 403):
            with self.subTest(status=status):
                self.assertEqual(GrantState.REVOKED, self.state(self.serve(status).url, ProcessResult(0, "t", "")))

    def test_a_refresh_that_gives_no_token_is_revoked(self) -> None:
        self.store()

        self.assertEqual(GrantState.REVOKED, self.state(self.serve(200).url, ProcessResult(1, "", "slop-haus: this workspace's credentials were rejected -- run login")))

    def test_a_helper_that_failed_to_refresh_for_another_reason_is_not_taken_for_a_revocation(self) -> None:
        self.store()

        with self.assertRaises(ConfigError):
            self.state(self.serve(200).url, ProcessResult(1, "", "slop-haus: could not reach https://s.example to refresh"))

    def test_a_server_error_is_not_taken_for_a_revocation(self) -> None:
        self.store()

        with self.assertRaises(ConfigError):
            self.state(self.serve(500).url, ProcessResult(0, "t", ""))

    def test_an_unreachable_server_is_not_taken_for_a_revocation(self) -> None:
        self.store()
        server = self.serve(200)
        server.close()

        with self.assertRaises(ConfigError):
            self.state(server.url, ProcessResult(0, "t", ""))

    def test_the_host_is_signed_in_only_while_the_grant_is_active(self) -> None:
        for state, expected in ((GrantState.ACTIVE, True), (GrantState.REVOKED, False), (GrantState.MISSING, False)):
            with self.subTest(state=state):
                actions = HostActions(self.layout, None, lambda _: None, lambda _: None, lambda: state, lambda: 0, lambda: None, None, lambda _: None)  # pyright: ignore[reportArgumentType]
                self.assertEqual(expected, actions.signed_in())
