from __future__ import annotations

import io
import tarfile
import tempfile
import unittest
from pathlib import Path

PYPROJECT = b'[project]\nname = "slop-haus-supervisor"\nversion = "0.1.0"\n'


def make_archive(path: Path, files: dict[str, bytes] | None = None) -> Path:
    """A package archive shaped like the server's: pyproject.toml, uv.lock and src/ at the top."""
    members = files if files is not None else {"pyproject.toml": PYPROJECT, "uv.lock": b"", "src/pkg/__init__.py": b"V = 1\n"}
    with tarfile.open(path, "w:gz") as tar:
        for name, body in members.items():
            info = tarfile.TarInfo(name)
            info.size = len(body)
            tar.addfile(info, io.BytesIO(body))
    return path


class TempDirTestCase(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
