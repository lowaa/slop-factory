from __future__ import annotations

import unittest
from pathlib import Path

HAUS = Path(__file__).resolve().parents[3]
NAME = "slop_haus_token.py"
SOURCE = HAUS / "token" / NAME
SERVER_COPY = HAUS / "server" / "internal" / "workspace" / "assets" / "py" / NAME
VENDORED = HAUS / "supervisor" / "src" / "slop_haus_supervisor" / "slophaus" / NAME


class VendoredTokenModuleTest(unittest.TestCase):
    """The credential module ships as three byte-identical files; the server's suite checks the same three."""

    def test_the_copies_equal_the_source(self) -> None:
        source = SOURCE.read_bytes()
        for copy in (SERVER_COPY, VENDORED):
            self.assertEqual(source, copy.read_bytes(), f"{copy} differs from token/{NAME}; run token/vendor.sh")
