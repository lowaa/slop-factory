from __future__ import annotations

import contextlib
import io
import json
import os
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Optional, Sequence, Tuple

import slop_haus_token as tok
from slop_haus_token import CredentialsError, CredentialStore, Credentials, RefreshPolicy, Refresher, Response

NOW = 1_000_000.0
FAST = RefreshPolicy(budget=7, attempt_max=4, attempts=4, backoff=0.5)


class FakeTransport:
    """Answers from a script; records what was sent. Decides nothing itself."""

    def __init__(self, *replies: Response) -> None:
        self.replies = list(replies)
        self.calls: list[Tuple[str, Sequence[Tuple[str, str]], float]] = []

    def __call__(self, url: str, fields: Sequence[Tuple[str, str]], timeout: float) -> Response:
        self.calls.append((url, list(fields), timeout))
        return self.replies.pop(0) if len(self.replies) > 1 else self.replies[0]


OK = Response(200, '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}')


class StoreCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        os.umask(0o077)
        self.store = CredentialStore(self.tmp, lock_timeout=0.3)
        self.store.ensure_directory()

    def seed(self, access: str = "AT-old", refresh: str = "RT-old", expires_at: int = int(NOW) + 30) -> None:
        self.store.write(Credentials(access, refresh, expires_at))

    def refresher(self, transport: FakeTransport, sleeps: Optional[list[float]] = None) -> Refresher:
        log = sleeps if sleeps is not None else []
        return Refresher(self.store, FAST, "http://srv", transport, sleep=log.append, clock=lambda: NOW, monotonic=lambda: NOW)


class StoreTest(StoreCase):
    def test_write_is_0600_atomic_and_leaves_no_temp_files(self) -> None:
        self.seed()
        self.assertEqual(stat.S_IMODE(os.stat(self.store.path).st_mode), 0o600)
        self.assertEqual(sorted(os.listdir(self.store.directory)), ["credentials.json"])
        self.assertEqual(self.store.read(), Credentials("AT-old", "RT-old", int(NOW) + 30))

    def test_a_failed_replace_leaves_the_old_file_whole_and_no_temp(self) -> None:
        self.seed()
        with mock.patch("slop_haus_token.os.replace", side_effect=OSError("boom")), self.assertRaises(OSError):
            self.store.write(Credentials("AT-new", "RT-new", 1))
        self.assertEqual(self.store.read(), Credentials("AT-old", "RT-old", int(NOW) + 30))
        self.assertEqual(os.listdir(self.store.directory), ["credentials.json"])

    def test_symlinked_credentials_is_refused(self) -> None:
        victim = os.path.join(self.tmp, "victim")
        with open(victim, "w") as handle:
            handle.write('{"access_token":"x","refresh_token":"y","expires_at":99999999999}')
        os.symlink(victim, self.store.path)
        with self.assertRaisesRegex(CredentialsError, "is a symlink"):
            self.store.read()

    def test_symlinked_lock_is_refused_and_not_truncated(self) -> None:
        victim = os.path.join(self.tmp, "victim")
        with open(victim, "w") as handle:
            handle.write("not a lock file")
        os.symlink(victim, self.store.lock_path)
        with self.assertRaisesRegex(CredentialsError, "is a symlink"), self.store.locked():
            pass
        with open(victim) as handle:
            self.assertEqual(handle.read(), "not a lock file")

    def test_symlinked_directory_is_refused(self) -> None:
        other = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, other, True)
        link_root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, link_root, True)
        os.symlink(other, os.path.join(link_root, ".slop-haus"))
        with self.assertRaisesRegex(CredentialsError, "is a symlink"):
            CredentialStore(link_root).ensure_directory()

    def test_lock_file_mode_is_forced_to_0600(self) -> None:
        with open(self.store.lock_path, "w"):
            pass
        os.chmod(self.store.lock_path, 0o666)
        with self.store.locked():
            pass
        self.assertEqual(stat.S_IMODE(os.stat(self.store.lock_path).st_mode), 0o600)

    def test_lock_is_not_inherited_by_children(self) -> None:
        # The child compares each open fd with the lock file by device and inode, which needs no /proc (macOS).
        # close_fds=False, or subprocess would close the fd itself and the test would pass without O_CLOEXEC.
        child = (
            "import os, sys\n"
            "lock = os.stat(sys.argv[1])\n"
            "for fd in range(min(os.sysconf('SC_OPEN_MAX'), 4096)):\n"
            "    try:\n"
            "        st = os.fstat(fd)\n"
            "    except OSError:\n"
            "        continue\n"
            "    if (st.st_dev, st.st_ino) == (lock.st_dev, lock.st_ino):\n"
            "        print('inherited', fd)\n"
        )
        with self.store.locked():
            out = subprocess.run(
                [sys.executable, "-c", child, self.store.lock_path], capture_output=True, text=True, check=True, close_fds=False
            ).stdout
        self.assertEqual(out, "")

    def test_leftover_temp_files_are_removed_but_not_the_credentials(self) -> None:
        self.seed()
        for name in ("credentials.json.AbC123", ".credentials.json.tmp"):
            with open(os.path.join(self.store.directory, name), "w") as handle:
                handle.write("RT-spent")
        self.store.remove_leftovers()
        self.assertEqual(sorted(os.listdir(self.store.directory)), ["credentials.json"])


class BashFlock:
    """util-linux flock(1) holding credentials.lock, as token.sh does."""

    def __init__(self, path: str) -> None:
        self.proc = subprocess.Popen(
            ["flock", "-x", path, "-c", "echo held; exec sleep 60"], stdout=subprocess.PIPE, text=True, start_new_session=True
        )
        assert self.proc.stdout is not None
        assert self.proc.stdout.readline().strip() == "held"

    def release(self) -> None:
        # The whole group: flock's child inherits the locked descriptor and would keep it.
        with contextlib.suppress(ProcessLookupError):
            os.killpg(self.proc.pid, signal.SIGKILL)
        self.proc.wait()
        assert self.proc.stdout is not None
        self.proc.stdout.close()


@unittest.skipUnless(shutil.which("flock"), "util-linux flock(1) not installed")
class CrossImplementationLockTest(StoreCase):
    def test_python_waits_for_bash_flock_then_proceeds(self) -> None:
        holder = BashFlock(self.store.lock_path)
        self.addCleanup(holder.release)
        self.store.lock_timeout = 10
        entered = threading.Event()

        def take() -> None:
            with self.store.locked():
                entered.set()

        thread = threading.Thread(target=take)
        thread.start()
        self.assertFalse(entered.wait(0.5), "entered while bash held the lock")
        holder.release()
        self.assertTrue(entered.wait(10), "never entered after bash released")
        thread.join()

    def test_python_times_out_when_bash_never_releases(self) -> None:
        holder = BashFlock(self.store.lock_path)
        self.addCleanup(holder.release)
        with self.assertRaisesRegex(CredentialsError, "timed out"), self.store.locked():
            pass

    def test_bash_waits_for_python_lock(self) -> None:
        with self.store.locked():
            tried = subprocess.run(["flock", "-n", self.store.lock_path, "-c", "true"], check=False)
            self.assertNotEqual(tried.returncode, 0, "bash flock got the lock Python held")
        free = subprocess.run(["flock", "-n", self.store.lock_path, "-c", "true"], check=False)
        self.assertEqual(free.returncode, 0)


class RefresherTest(StoreCase):
    def test_success_stores_pair_and_returns_access(self) -> None:
        self.seed()
        transport = FakeTransport(OK)
        self.assertEqual(self.refresher(transport).refresh("RT-old"), "AT-new")
        self.assertEqual(self.store.read(), Credentials("AT-new", "RT-new", int(NOW) + 900))
        url, fields, _ = transport.calls[0]
        self.assertEqual(url, "http://srv/oauth/token")
        self.assertIn(("refresh_token", "RT-old"), fields)

    def test_transit_failures_retry_with_same_token_and_doubling_backoff(self) -> None:
        self.seed()
        sleeps: list[float] = []
        transport = FakeTransport(Response(0), Response(502, "<html>"), OK)
        self.assertEqual(self.refresher(transport, sleeps).refresh("RT-old"), "AT-new")
        self.assertEqual(sleeps, [0.5, 1.0])
        self.assertEqual([dict(c[1])["refresh_token"] for c in transport.calls], ["RT-old"] * 3)

    def test_gives_up_after_attempt_bound_and_keeps_credentials(self) -> None:
        self.seed()
        transport = FakeTransport(Response(503))
        with self.assertRaisesRegex(CredentialsError, "gave up after 4 attempt"):
            self.refresher(transport).refresh("RT-old")
        self.assertEqual(len(transport.calls), 4)
        self.assertEqual(self.store.read().refresh_token, "RT-old")

    def test_other_4xx_are_never_retried(self) -> None:
        for status in (400, 401, 403, 429):
            with self.subTest(status=status):
                transport = FakeTransport(Response(status, '{"error":"invalid_request"}'))
                with self.assertRaisesRegex(CredentialsError, "refused to refresh"):
                    self.refresher(transport).refresh("RT-old")
                self.assertEqual(len(transport.calls), 1)

    def test_invalid_grant_explains_and_leaves_file(self) -> None:
        self.seed()
        transport = FakeTransport(Response(400, '{"error":"invalid_grant"}'))
        with self.assertRaisesRegex(CredentialsError, "re-authorize"):
            self.refresher(transport).refresh("RT-old")
        self.assertEqual(self.store.read().access_token, "AT-old")

    def test_retry_uses_what_another_process_stored(self) -> None:
        self.seed()
        store = self.store

        class Racing(FakeTransport):
            def __call__(self, url: str, fields: Sequence[Tuple[str, str]], timeout: float) -> Response:
                store.write(Credentials("AT-other", "RT-other", int(NOW) + 900))
                return super().__call__(url, fields, timeout)

        transport = Racing(Response(0))
        self.assertEqual(self.refresher(transport).refresh("RT-old"), "AT-other")
        self.assertEqual(len(transport.calls), 1)

    def test_retry_sends_rotated_refresh_token(self) -> None:
        self.seed()
        store = self.store

        class Racing(FakeTransport):
            def __call__(self, url: str, fields: Sequence[Tuple[str, str]], timeout: float) -> Response:
                if not self.calls:
                    store.write(Credentials("AT-other", "RT-other", int(NOW) + 30))
                return super().__call__(url, fields, timeout)

        transport = Racing(Response(0), OK)
        self.refresher(transport).refresh("RT-old")
        self.assertEqual(dict(transport.calls[1][1])["refresh_token"], "RT-other")

    def test_non_numeric_expires_in_falls_back(self) -> None:
        transport = FakeTransport(Response(200, '{"access_token":"a","refresh_token":"r","expires_in":"$(x)"}'))
        self.refresher(transport).refresh("RT-old")
        self.assertEqual(self.store.read().expires_at, int(NOW) + 900)

    def test_error_description_is_bounded_and_redacts_the_token_sent(self) -> None:
        body = json.dumps({"error": "x", "error_description": "echo RT-old\x1b[31m " + "A" * 500})
        with self.assertRaises(CredentialsError) as caught:
            self.refresher(FakeTransport(Response(400, body))).refresh("RT-old")
        message = str(caught.exception)
        self.assertNotIn("RT-old", message)
        self.assertNotIn("\x1b", message)
        self.assertLess(len(message), 400)

    def test_unrecognisable_response(self) -> None:
        with self.assertRaisesRegex(CredentialsError, "unrecognisable"):
            self.refresher(FakeTransport(Response(200, "{}"))).refresh("RT-old")


class GetTokenTest(StoreCase):
    def get(self, transport: FakeTransport) -> str:
        return tok.get_token(self.store, lambda: self.refresher(transport), clock=lambda: NOW)

    def test_cached_token_makes_no_request(self) -> None:
        self.seed(expires_at=int(NOW) + 3600)
        transport = FakeTransport(OK)
        self.assertEqual(self.get(transport), "AT-old")
        self.assertEqual(transport.calls, [])

    def test_no_credentials(self) -> None:
        with self.assertRaisesRegex(CredentialsError, "token.sh login"):
            self.get(FakeTransport(OK))

    def test_refresh_after_lock_rereads(self) -> None:
        """A winner that refreshed while we waited for the lock leaves nothing to ask the server."""
        self.seed()
        holder_done = threading.Event()
        transport = FakeTransport(OK)
        self.store.lock_timeout = 10
        result: list[str] = []

        with self.store.locked():
            thread = threading.Thread(target=lambda: (result.append(self.get(transport)), holder_done.set()))
            thread.start()
            time.sleep(0.2)
            self.store.write(Credentials("AT-winner", "RT-winner", int(NOW) + 900))
        thread.join(10)
        self.assertEqual(result, ["AT-winner"])
        self.assertEqual(transport.calls, [])


class MainTest(StoreCase):
    def run_main(self, args: Sequence[str], transport: FakeTransport, env: Optional[dict[str, str]] = None) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        environ = {"SLOP_HAUS_URL": "http://srv", **(env or {})}
        code = tok.main(args, self.tmp, environ, transport, lambda _s: None, out, err)
        return code, out.getvalue(), err.getvalue()

    def test_headers_json(self) -> None:
        self.seed(expires_at=int(time.time()) + 3600)
        code, out, _ = self.run_main(["headers"], FakeTransport(OK))
        self.assertEqual((code, json.loads(out)), (0, {"Authorization": "Bearer AT-old"}))

    def test_headers_json_flag_is_the_headers_command(self) -> None:
        self.seed(expires_at=int(time.time()) + 3600)
        code, out, _ = self.run_main(["--headers-json", "-server=http://named"], FakeTransport(OK))
        self.assertEqual((code, json.loads(out)), (0, {"Authorization": "Bearer AT-old"}))

    def test_headers_refuses_token_that_breaks_out_of_json(self) -> None:
        self.store.write(Credentials('AT"},"X', "RT", int(time.time()) + 3600))
        code, out, err = self.run_main(["headers"], FakeTransport(OK))
        self.assertEqual((code, out), (1, ""))
        self.assertIn("malformed", err)

    def test_headers_server_flag_beats_environment(self) -> None:
        self.seed(expires_at=int(time.time()) + 30)
        transport = FakeTransport(OK)
        self.run_main(["headers", "-server=http://named"], transport, {"SLOP_HAUS_URL": "http://shell"})
        self.assertEqual(transport.calls[0][0], "http://named/oauth/token")

    def test_usage_errors(self) -> None:
        self.assertEqual(self.run_main(["headers", "--bogus"], FakeTransport(OK))[0], 2)
        self.assertEqual(self.run_main(["nope"], FakeTransport(OK))[0], 2)

    def test_no_token_in_output_on_any_failure_path(self) -> None:
        secrets = ("AT-SECRET", "RT-SECRET", "AT-NEW-SECRET")
        body_echo = Response(500, "<html>AT-SECRET RT-SECRET</html>")
        cases = {
            "unreachable": FakeTransport(Response(0)),
            "gateway": FakeTransport(Response(502, "RT-SECRET")),
            "invalid_grant": FakeTransport(Response(400, '{"error":"invalid_grant"}')),
            "server error echoing tokens": FakeTransport(body_echo),
            "unrecognisable": FakeTransport(Response(200, '{"access_token":"AT-NEW-SECRET"}')),
        }
        for name, transport in cases.items():
            with self.subTest(name):
                self.store.write(Credentials("AT-SECRET", "RT-SECRET", int(time.time()) + 30))
                code, out, err = self.run_main([], transport)
                self.assertNotEqual(code, 0)
                for secret in secrets:
                    self.assertNotIn(secret, out + err)

    def test_unexpected_exception_prints_type_only(self) -> None:
        self.seed()

        def boom(_u: str, _f: Sequence[Tuple[str, str]], _t: float) -> Response:
            raise RuntimeError("RT-old leaked in message")

        out, err = io.StringIO(), io.StringIO()
        code = tok.main([], self.tmp, {"SLOP_HAUS_URL": "http://srv"}, boom, lambda _s: None, out, err)
        self.assertEqual(code, 1)
        self.assertNotIn("RT-old", err.getvalue())
        self.assertIn("RuntimeError", err.getvalue())


class PostFormTest(unittest.TestCase):
    """The real transport, against a local server."""

    def serve(self, status: int, body: str = "", location: Optional[str] = None) -> Tuple[str, list[Tuple[str, str, str]]]:
        seen: list[Tuple[str, str, str]] = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                seen.append((self.command, self.path, self.rfile.read(length).decode()))
                self.send_response(status)
                if location:
                    self.send_header("Location", location)
                self.send_header("Content-Length", str(len(body.encode())))
                self.end_headers()
                self.wfile.write(body.encode())

            def do_GET(self) -> None:
                seen.append((self.command, self.path, ""))
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"ok")

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 5)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return f"http://127.0.0.1:{server.server_address[1]}", seen

    def test_posts_urlencoded_fields_and_returns_status_and_body(self) -> None:
        url, seen = self.serve(200, '{"a":1}')
        reply = tok.post_form(url + "/oauth/token", [("refresh_token", "RT old+/x"), ("client_id", "c")], 5)
        self.assertEqual((reply.status, reply.body), (200, '{"a":1}'))
        self.assertEqual(seen, [("POST", "/oauth/token", "refresh_token=RT+old%2B%2Fx&client_id=c")])

    def test_error_statuses_keep_their_body(self) -> None:
        url, _ = self.serve(400, '{"error":"invalid_grant"}')
        reply = tok.post_form(url, [], 5)
        self.assertEqual((reply.status, reply.body), (400, '{"error":"invalid_grant"}'))

    def test_redirects_are_not_followed(self) -> None:
        url, seen = self.serve(302, "", location="/elsewhere")
        self.assertEqual(tok.post_form(url + "/oauth/token", [("a", "b")], 5).status, 302)
        self.assertEqual([entry[0] for entry in seen], ["POST"])

    def test_nothing_listening_is_status_zero(self) -> None:
        self.assertEqual(tok.post_form("http://127.0.0.1:1/x", [], 2).status, 0)
