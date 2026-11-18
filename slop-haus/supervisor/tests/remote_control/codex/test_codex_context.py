from __future__ import annotations

from codex_fixtures import CodexHostCase
from host_fixtures import item


class CodexContextTest(CodexHostCase):
    def test_the_context_is_the_newest_requests_input_not_the_threads_total(self) -> None:
        self.start()
        self.converse(2, "one")
        self.converse(3, "two")
        self.assertEqual([100], [e["tokens"] for e in self.reporter.of("context")], "reported once: the second turn left it unchanged")
        self.assertEqual(100, self.conv().state.context_tokens, "the thread's total would be 200")


class CodexCompactTest(CodexHostCase):
    def test_a_compact_asks_for_the_threads_compaction_and_reports_the_divider(self) -> None:
        self.start()
        self.converse(2, "one")
        self.handle(item(3, "compact"))
        self.pump(lambda: self.turns() == 2)
        self.assertEqual(1, len(self.requests("thread/compact/start")))
        self.assertEqual(1, len(self.reporter.of("context_compacted")))
        self.assertEqual(3, self.host.current_ack())
