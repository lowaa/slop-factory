from __future__ import annotations

import io
import os
import stat
from typing import IO, Any

from host_fixtures import HostCase, item
from slop_haus_supervisor.remote_control.host.attachments import AttachmentStore, TooLarge, copy_capped, safe_name
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.settings import Settings

A1 = "aaaaaaaa-1111-4222-8333-444444444444"
A2 = "bbbbbbbb-1111-4222-8333-444444444444"


class FakeDownloader:
    """Serves what the test gives it; the cap is enforced by the same helper the real one uses."""

    def __init__(self) -> None:
        self.bodies: dict[str, bytes] = {}
        self.failing: set[str] = set()
        self.paths: list[str] = []

    def download(self, path: str, sink: IO[bytes], max_bytes: int) -> None:
        self.paths.append(path)
        key = path.rsplit("/", 1)[-1]
        if key in self.failing:
            raise OSError("slop-haus answered 500")
        copy_capped(io.BytesIO(self.bodies[key]), sink, max_bytes)


def meta(attachment_id: str, filename: str, size: int) -> dict[str, Any]:
    return {"id": attachment_id, "filename": filename, "content_type": "image/png", "size_bytes": size}


class AttachmentTest(HostCase):
    def setUp(self) -> None:
        super().setUp()
        self.downloader = FakeDownloader()
        self.store = AttachmentStore(self.root, self.downloader)
        self.host.shutdown()
        self.host = Host(self.root, self.reporter, self.host.harness, Settings(harness_grace=5, idle_timeout=300),
                         clock=self.clock, attachments=self.store)
        self.addCleanup(self.host.shutdown)
        self.dir = os.path.join(self.root, ".slop-haus", "conductor", "attachments")

    def send(self, seq: int, text: str, *attachments: dict[str, Any]) -> None:
        before = self.turns()
        self.handle(item(seq, "message", text=text, message_seq=seq, attachments=list(attachments)))
        self.pump(lambda: self.turns() > before)

    def test_downloads_into_the_working_copy_and_names_it_in_the_message(self) -> None:
        self.downloader.bodies[A1] = b"png-bytes"
        self.start()
        self.send(2, "look", meta(A1, "shot.png", 9))
        name = f"{A1}-shot.png"
        with open(os.path.join(self.dir, name), "rb") as f:
            self.assertEqual(b"png-bytes", f.read())
        self.assertEqual(["look\n[attached: shot.png -> .slop-haus/conductor/attachments/" + name + "]"],
                         [r["text"] for r in self.received()])
        self.assertEqual([f"/api/agent/attachments/{A1}"], self.downloader.paths)
        self.assertEqual(0o700, stat.S_IMODE(os.stat(self.dir).st_mode))
        self.assertEqual(0o600, stat.S_IMODE(os.stat(os.path.join(self.dir, name)).st_mode))
        self.assertEqual([name], os.listdir(self.dir), "no temp file left")

    def test_a_traversing_filename_stays_in_the_directory(self) -> None:
        self.downloader.bodies[A1] = b"x"
        self.start()
        self.send(2, "hi", meta(A1, "../../x", 1))
        self.assertEqual([f"{A1}-x"], os.listdir(self.dir))
        self.assertFalse(os.path.exists(os.path.join(self.root, ".slop-haus", "x")))
        self.assertFalse(os.path.exists(os.path.join(self.root, "x")))

    def test_safe_names(self) -> None:
        self.assertEqual("x", safe_name("../../x", A1))
        self.assertEqual("passwd", safe_name("/etc/passwd", A1))
        self.assertEqual("x", safe_name("..\\..\\x", A1))
        self.assertEqual("bashrc", safe_name(".bashrc", A1))
        self.assertEqual("a_b_.png", safe_name("a b\n.png", A1))
        self.assertEqual(A1, safe_name("..", A1))
        self.assertEqual(A1, safe_name("", A1))
        self.assertEqual(100, len(safe_name("a" * 500, A1)))

    def test_more_than_the_advertised_size_is_aborted_and_deleted(self) -> None:
        self.downloader.bodies[A1] = b"0123456789"
        self.start()
        self.send(2, "hi", meta(A1, "big.bin", 4))
        self.assertEqual([], os.listdir(self.dir))
        self.assertEqual(["hi\n[attachment big.bin could not be downloaded]"], [r["text"] for r in self.received()])

    def test_a_failed_download_still_delivers_the_message(self) -> None:
        self.downloader.failing.add(A1)
        self.downloader.bodies[A2] = b"ok"
        self.start()
        self.send(2, "hi", meta(A1, "a.png", 3), meta(A2, "b.png", 2))
        self.assertEqual(
            ["hi\n[attachment a.png could not be downloaded]\n[attached: b.png -> .slop-haus/conductor/attachments/" + f"{A2}-b.png]"],
            [r["text"] for r in self.received()])
        self.assertEqual("echo: hi", self.reporter.said().split("\n")[0])

    def test_a_malformed_entry_is_reported_not_fetched(self) -> None:
        self.start()
        self.send(2, "hi", meta("../evil", "a.png", 3))
        self.assertEqual([], self.downloader.paths)
        self.assertEqual(["hi\n[attachment a.png could not be downloaded]"], [r["text"] for r in self.received()])

    def test_stop_removes_that_conversations_copies_only(self) -> None:
        self.downloader.bodies[A1] = b"1"
        self.start()
        self.send(2, "hi", meta(A1, "a.png", 1))
        other = os.path.join(self.dir, f"{A2}-other.png")
        with open(other, "w") as f:
            f.write("other conversation")
        self.handle(item(3, "stop"))
        self.assertEqual([os.path.basename(other)], os.listdir(self.dir))

    def test_clear_removes_the_copies(self) -> None:
        self.downloader.bodies[A1] = b"1"
        self.start()
        self.send(2, "hi", meta(A1, "a.png", 1))
        self.handle(item(3, "clear"))
        self.assertEqual([], os.listdir(self.dir))

    def test_the_ids_survive_a_restart_so_a_stop_still_cleans_up(self) -> None:
        self.downloader.bodies[A1] = b"1"
        self.start()
        self.send(2, "hi", meta(A1, "a.png", 1))
        self.host.shutdown()
        self.host = Host(self.root, self.reporter, self.host.harness, Settings(harness_grace=5, idle_timeout=300),
                         clock=self.clock, attachments=self.store)
        self.addCleanup(self.host.shutdown)
        self.handle(item(3, "stop"))
        self.assertEqual([], os.listdir(self.dir))

    def test_copy_capped_allows_exactly_the_size(self) -> None:
        out = io.BytesIO()
        copy_capped(io.BytesIO(b"abcd"), out, 4)
        self.assertEqual(b"abcd", out.getvalue())
        with self.assertRaises(TooLarge):
            copy_capped(io.BytesIO(b"abcde"), io.BytesIO(), 4)
