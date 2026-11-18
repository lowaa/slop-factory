from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.workspace.bundle import BUNDLE_HASH_FILE, read_bundle_hash


class ReadBundleHashTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)

    def write(self, content: bytes) -> None:
        path = self.root / BUNDLE_HASH_FILE
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)

    def test_the_hash_setup_recorded_is_read(self) -> None:
        self.write(b"c1ff58edfd3c9bb6\n")

        self.assertEqual("c1ff58edfd3c9bb6", read_bundle_hash(self.root))

    def test_a_workspace_without_the_file_has_none(self) -> None:
        self.assertEqual("", read_bundle_hash(self.root))

    def test_a_damaged_file_is_not_a_version(self) -> None:
        for content in (b"", b"not hex!\n", b"ABCDEF\n", b"a" * 500, b"\xff\xfe"):
            with self.subTest(content=content[:20]):
                self.write(content)
                self.assertEqual("", read_bundle_hash(self.root))
