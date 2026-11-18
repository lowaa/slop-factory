from __future__ import annotations

import json
import unittest
from pathlib import Path

from slop_haus_supervisor.usage.conversation import MODEL, PI_PROVIDER, TOOL, ConversationUsage

# go-llm-usage's shared fixture, which every client's label patterns and the server's validation run.
FIXTURE = Path(__file__).resolve().parents[4] / "go-llm-usage" / "fixtures" / "record_labels.json"
LABELS = json.loads(FIXTURE.read_text(encoding="utf-8"))


class RecordLabelsTest(unittest.TestCase):
    def check(self, label: str, pattern: object) -> None:
        match = getattr(pattern, "match")
        for value in LABELS[label]["valid"]:
            self.assertIsNotNone(match(value), f"{label} {value!r} is valid")
        for value in LABELS[label]["invalid"]:
            self.assertIsNone(match(value), f"{label} {value!r} is invalid")

    def test_model_pattern_matches_the_servers(self) -> None:
        self.check("model", MODEL)

    def test_provider_pattern_matches_the_servers(self) -> None:
        self.check("provider", PI_PROVIDER)

    def test_tool_pattern_matches_the_servers(self) -> None:
        self.check("tool", TOOL)

    def test_a_record_carries_only_labels_the_server_accepts(self) -> None:
        usage = ConversationUsage()
        for value in LABELS["model"]["valid"] + LABELS["model"]["invalid"]:
            usage.add_delta("anthropic", value, "metered", {"requests": 1})
            usage.add_call(value, "Bash")
        for value in LABELS["provider"]["valid"] + LABELS["provider"]["invalid"]:
            usage.add_delta(value, "m", "metered", {"requests": 1})
        for value in LABELS["tool"]["valid"] + LABELS["tool"]["invalid"]:
            usage.add_call("m", value)
        records = usage.records("metered", "pi")
        valid_models = set(LABELS["model"]["valid"])
        valid_tools = set(LABELS["tool"]["valid"])
        self.assertEqual(valid_models | {"m"}, {r["model"] for r in records})
        for record in records:
            self.assertIn(record["provider"], set(LABELS["provider"]["valid"]) | {"anthropic"})
            for tool in record.get("tools", []):
                self.assertIn(tool["tool"], valid_tools | {"Bash"})
