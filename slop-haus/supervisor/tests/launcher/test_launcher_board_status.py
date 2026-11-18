from __future__ import annotations

import contextlib
import http.server
import io
import json
import tempfile
import threading
import unittest
from collections.abc import Mapping
from pathlib import Path

from slop_haus_supervisor import cli
from slop_haus_supervisor.launcher.board_view import BoardSlot, BoardStatus, lines
from test_launcher_cli import make_archive

VIEW = {
    "claimable": 0,
    "offline": False,
    "last_polled_at": "2026-10-25T10:00:00+00:00",
    "slots": [
        {
            "agent_id": "00000000-0000-0000-0000-000000000001",
            "agent_name": "box-1",
            "phase": "idle",
            "phase_entered_at": "2026-10-25T09:50:00+00:00",
            "reason": "lock_queued",
            "reason_detail": "behind #9",
            "ticket_id": 12,
            "offline": False,
        }
    ],
}


class StubServer:
    """A server that answers the supervisor view and records what it was asked."""

    def __init__(self, body: Mapping[str, object], status: int = 200) -> None:
        self.requests: list[tuple[str, str]] = []
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                outer.requests.append((self.path, self.headers.get("Authorization", "")))
                payload = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                pass

        self._http = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self._http.serve_forever, daemon=True).start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self._http.server_port}"

    def close(self) -> None:
        self._http.shutdown()
        self._http.server_close()


class BoardStatusTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.cwd = self.tmp / "cwd"
        self.cwd.mkdir()
        self.run_cli("init", "--server", "https://s.example/", "--tenant-id", "tid", "--tenant", "acme", "--package", str(make_archive(self.tmp / "p.tar.gz")))
        self.root = self.home / "slop-haus" / "acme"

    def run_cli(self, *argv: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cli.main([*argv, *([] if argv[0] == "init" else ["-t", "acme"])], {}, self.home, self.cwd)
        return code, out.getvalue(), err.getvalue()

    def sign_in(self, server: str) -> None:
        config = self.root / "supervisor.toml"
        config.write_text(config.read_text().replace("https://s.example", server))
        dot = self.root / "supervisor" / ".slop-haus"
        dot.mkdir(parents=True, exist_ok=True)
        (dot / "credentials.json").write_text("{}")
        helper = dot / "token.sh"
        helper.write_text("#!/bin/sh\nprintf supervisor-token\n")
        helper.chmod(0o755)

    def test_status_shows_the_boards_view_asked_with_the_supervisors_credential(self) -> None:
        server = StubServer(VIEW)
        self.addCleanup(server.close)
        self.sign_in(server.url)
        (self.root / "supervisor.log").write_text("2099-01-01 10:00:00,000 ERROR slop-haus-supervisor: slot=box-1 boom\n")

        code, out, _ = self.run_cli("status")

        self.assertEqual(0, code)
        # What only this host knows still follows the board's view.
        self.assertIn("slop-haus start", out)
        self.assertIn("last error", out)
        self.assertIn("slot=box-1 boom", out)
        self.assertEqual([("/api/supervisor/view", "Bearer supervisor-token")], server.requests)
        self.assertIn("last heartbeat", out)
        self.assertIn("box-1: idle for", out)
        self.assertIn("reason=lock_queued (behind #9)", out)
        self.assertIn("ticket=#12", out)

    def test_a_server_that_refuses_falls_back_to_the_log_and_says_why(self) -> None:
        server = StubServer({"detail": "no"}, status=403)
        self.addCleanup(server.close)
        self.sign_in(server.url)

        code, out, err = self.run_cli("status")

        self.assertEqual(0, code)
        self.assertIn("revoked or rejected", err)
        self.assertIn("slop-haus login", err)
        self.assertIn("slop-haus start", out)
        self.assertNotIn("last heartbeat", out)

    def test_a_helper_that_failed_for_another_reason_is_not_called_a_revocation(self) -> None:
        self.sign_in("https://s.example")
        helper = self.root / "supervisor" / ".slop-haus" / "token.sh"
        helper.write_text("#!/bin/sh\necho 'slop-haus: could not reach the server' >&2\nexit 1\n")

        _, _, err = self.run_cli("status")

        self.assertNotIn("revoked", err)
        self.assertIn("could not ask the server", err)

    def test_a_refresh_the_server_refused_says_the_grant_is_revoked(self) -> None:
        self.sign_in("https://s.example")
        helper = self.root / "supervisor" / ".slop-haus" / "token.sh"
        helper.write_text("#!/bin/sh\necho \"slop-haus: this workspace's credentials were rejected -- run login\" >&2\nexit 1\n")

        code, _, err = self.run_cli("status")

        self.assertEqual(0, code)
        self.assertIn("revoked or rejected", err)

    def test_a_host_not_signed_in_uses_the_log(self) -> None:
        code, out, err = self.run_cli("status")

        self.assertEqual(0, code)
        self.assertIn("not signed in", err)
        self.assertIn("slop-haus start", out)

    def test_lines_say_when_the_board_has_never_seen_a_supervisor(self) -> None:
        self.assertEqual(["  board: the board has never seen a supervisor poll"], lines(BoardStatus(None, False, ()), 0.0))

    def test_lines_omit_the_reason_of_a_running_slot_and_flag_an_offline_one(self) -> None:
        slot = BoardSlot("box-1", "running", "no_claimable", "", 3, None, True)
        self.assertEqual("  box-1: running ticket=#3 [offline]", lines(BoardStatus(100.0, True, (slot,)), 160.0)[1])
        self.assertIn("(OFFLINE)", lines(BoardStatus(100.0, True, (slot,)), 160.0)[0])
