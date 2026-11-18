from __future__ import annotations

import json
import unittest
from pathlib import Path

from slop_haus_supervisor.harness.variant import PROVIDER, InvalidVariant, Variant, check_choice, parse_variant

# go-llm-usage's shared fixture: a provider the usage server refuses must not pass config.
FIXTURE = Path(__file__).resolve().parents[4] / "go-llm-usage" / "fixtures" / "record_labels.json"
PROVIDERS = json.loads(FIXTURE.read_text(encoding="utf-8"))["provider"]


class TestVariant(unittest.TestCase):
    def test_parse(self) -> None:
        self.assertEqual(Variant(), parse_variant(""))
        self.assertEqual(Variant(model="claude-opus-4-7", effort="high"), parse_variant("model=claude-opus-4-7, effort=high"))

    def test_refused(self) -> None:
        for bad in ("model=-x", "model=a b", "provider=A", "nonsense", "colour=red", "model=a;rm"):
            with self.subTest(bad), self.assertRaises(InvalidVariant):
                parse_variant(bad)

    def test_provider_pattern_matches_the_usage_servers(self) -> None:
        for value in PROVIDERS["valid"]:
            self.assertIsNotNone(PROVIDER.match(value), f"{value!r} is valid")
        for value in PROVIDERS["invalid"]:
            self.assertIsNone(PROVIDER.match(value), f"{value!r} is invalid")

    def test_pi_spells_effort_thinking(self) -> None:
        self.assertEqual(Variant(provider="anthropic", thinking="high"), parse_variant("provider=anthropic,thinking=high"))

    def test_a_setting_given_twice_is_refused(self) -> None:
        with self.assertRaises(InvalidVariant):
            parse_variant("model=a,model=b")

    def test_choice(self) -> None:
        check_choice("effort", "auto", ("auto",))
        check_choice("effort", "", ("auto",))
        with self.assertRaises(InvalidVariant):
            check_choice("effort", "off", ("auto",))


if __name__ == "__main__":
    unittest.main()
