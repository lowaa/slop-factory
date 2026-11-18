from __future__ import annotations

import unittest

from slop_haus_supervisor.harness.text import SecretMasker


class TestSecretMasker(unittest.TestCase):
    def test_values_are_masked_longest_first(self) -> None:
        masker = SecretMasker(["abcd", "abcdef-long"])

        self.assertEqual("x *** y ***", masker.mask("x abcdef-long y abcd"))

    def test_values_too_short_to_be_credentials_are_left_alone(self) -> None:
        self.assertEqual("a b c", SecretMasker(["a", ""]).mask("a b c"))


if __name__ == "__main__":
    unittest.main()
