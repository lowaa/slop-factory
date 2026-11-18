from __future__ import annotations

from host_fixtures import item
from pi_fixtures import PiHostCase


class PiContextTest(PiHostCase):
    def test_the_context_is_pis_context_usage_from_the_session_stats(self) -> None:
        self.start()
        self.converse(2, "one")
        self.converse(3, "two")
        self.assertEqual([1010, 1020], [e["tokens"] for e in self.reporter.of("context")])


class PiCompactTest(PiHostCase):
    def test_a_compact_sends_pis_compact_command_and_refreshes_the_context(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "compact"))
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(["prompt", "get_session_stats", "compact"], [c["type"] for c in self.received() if c["type"] != "-settled-"])
        self.assertEqual(1, len(self.reporter.of("context_compacted")))
        self.assertEqual([1010, 321], [e["tokens"] for e in self.reporter.of("context")])
