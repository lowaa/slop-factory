from __future__ import annotations

import http.server
import threading
from pathlib import Path

from slop_haus_supervisor.workspace import package
from support import PYPROJECT, TempDirTestCase, make_archive


class InstallTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.root = self.tmp / "root"
        self.root.mkdir()

    def test_replaces_the_old_tree_and_leaves_no_scratch_directories(self) -> None:
        package.install(make_archive(self.tmp / "a.tar.gz"), self.root)
        stale = self.root / "app" / "stale.txt"
        stale.write_text("old")

        package.install(make_archive(self.tmp / "b.tar.gz", {"pyproject.toml": PYPROJECT}), self.root)

        self.assertFalse(stale.exists())
        self.assertEqual(["app"], sorted(p.name for p in self.root.iterdir()))

    def test_a_bad_archive_leaves_the_running_version_in_place(self) -> None:
        package.install(make_archive(self.tmp / "a.tar.gz"), self.root)
        broken = make_archive(self.tmp / "broken.tar.gz", {"README": b"no project here"})

        with self.assertRaises(package.PackageError):
            package.install(broken, self.root)

        self.assertTrue((self.root / "app" / "src" / "pkg" / "__init__.py").is_file())
        self.assertEqual(["app"], sorted(p.name for p in self.root.iterdir()))

    def test_an_install_interrupted_between_its_renames_is_recovered_by_the_next(self) -> None:
        package.install(make_archive(self.tmp / "a.tar.gz"), self.root)
        (self.root / "app").rename(self.root / ".app-old-crashed")

        with self.assertRaises(package.PackageError):
            package.install(make_archive(self.tmp / "broken.tar.gz", {"README": b"x"}), self.root)

        self.assertTrue((self.root / "app" / "src" / "pkg" / "__init__.py").is_file())
        self.assertEqual(["app"], sorted(p.name for p in self.root.iterdir()))

    def test_a_path_that_leaves_the_directory_is_refused(self) -> None:
        evil = make_archive(self.tmp / "evil.tar.gz", {"pyproject.toml": PYPROJECT, "../escaped.txt": b"x"})

        with self.assertRaises(package.PackageError):
            package.install(evil, self.root)

        self.assertFalse((self.tmp / "escaped.txt").exists())
        self.assertFalse((self.root / "app").exists())


class FetchTest(TempDirTestCase):
    def serve(self, body: bytes, status: int = 200) -> tuple[str, list[str]]:
        seen: list[str] = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                seen.append(f"{self.path} {self.headers.get('Authorization')}")
                self.send_response(status)
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format: str, *args: object) -> None:
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.addCleanup(server.server_close)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.shutdown)
        return f"http://127.0.0.1:{server.server_port}/", seen

    def test_fetches_the_package_as_the_agent(self) -> None:
        archive = make_archive(self.tmp / "served.tar.gz")
        url, seen = self.serve(archive.read_bytes())
        out = self.tmp / "got.tar.gz"

        package.fetch(url, "tok", out)

        self.assertEqual(archive.read_bytes(), out.read_bytes())
        self.assertEqual(["/api/supervisor/package Bearer tok"], seen)

    def test_an_error_status_is_a_package_error(self) -> None:
        url, _ = self.serve(b"nope", status=401)

        with self.assertRaises(package.PackageError):
            package.fetch(url, "tok", Path(self.tmp / "got.tar.gz"))
