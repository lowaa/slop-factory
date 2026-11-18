from __future__ import annotations

import json
import unittest

from slop_haus_token import Credentials, RefreshPolicy, needs_refresh, numeric_or, parse_credentials


class NumericOrTest(unittest.TestCase):
    def test_plain_integers_pass(self) -> None:
        self.assertEqual(numeric_or("42", 7), 42)
        self.assertEqual(numeric_or("0", 7), 0)
        self.assertEqual(numeric_or(42, 7), 42)

    def test_everything_else_falls_back(self) -> None:
        bad_values: list[object] = ["", "-1", "1.5", " 1", "1\n", "$(rm -rf ~)", "٣", "1e3", None, True, 1.5, -3, [], {}]
        for bad in bad_values:
            with self.subTest(bad=bad):
                self.assertEqual(numeric_or(bad, 7), 7)


class NeedsRefreshTest(unittest.TestCase):
    def test_margin_boundary(self) -> None:
        self.assertFalse(needs_refresh(1000, 1061))
        self.assertTrue(needs_refresh(1000, 1060))
        self.assertTrue(needs_refresh(1000, 1059))

    def test_expired_and_zero(self) -> None:
        self.assertTrue(needs_refresh(1000, 500))
        self.assertTrue(needs_refresh(1000, 0))

    def test_custom_margin(self) -> None:
        self.assertFalse(needs_refresh(1000, 1011, margin=10))
        self.assertTrue(needs_refresh(1000, 1010, margin=10))


class CredentialsTest(unittest.TestCase):
    def test_write_is_exactly_three_keys(self) -> None:
        out = json.loads(Credentials("a", "r", 5).to_json())
        self.assertEqual(out, {"access_token": "a", "refresh_token": "r", "expires_at": 5})

    def test_round_trip(self) -> None:
        creds = Credentials("a", "r", 5)
        self.assertEqual(parse_credentials(creds.to_json()), creds)

    def test_parse_ignores_extra_keys_and_does_not_write_them(self) -> None:
        creds = parse_credentials('{"access_token":"a","refresh_token":"r","expires_at":9,"extra":1}')
        self.assertEqual(creds, Credentials("a", "r", 9))
        self.assertNotIn("extra", creds.to_json())

    def test_parse_accepts_string_expiry(self) -> None:
        self.assertEqual(parse_credentials('{"expires_at":"12"}').expires_at, 12)

    def test_parse_is_tolerant(self) -> None:
        empty = Credentials("", "", 0)
        for bad in ["", "not json", "[]", "null", '"x"', "{", '{"access_token":5,"refresh_token":null,"expires_at":"$(x)"}']:
            with self.subTest(bad=bad):
                self.assertEqual(parse_credentials(bad), empty)

    def test_usable(self) -> None:
        self.assertTrue(Credentials("a", "r", 2000).usable(1000))
        self.assertFalse(Credentials("a", "r", 1040).usable(1000))
        self.assertFalse(Credentials("", "r", 2000).usable(1000))

    def test_repr_hides_tokens(self) -> None:
        text = repr(Credentials("SECRET-A", "SECRET-R", 1))
        self.assertNotIn("SECRET", text)


class RefreshPolicyTest(unittest.TestCase):
    def test_defaults_match_token_sh(self) -> None:
        self.assertEqual(RefreshPolicy.from_env({}), RefreshPolicy(7, 4, 4, 0.5))

    def test_env_overrides(self) -> None:
        policy = RefreshPolicy.from_env(
            {
                "SLOP_HAUS_REFRESH_BUDGET": "3",
                "SLOP_HAUS_REFRESH_ATTEMPT_MAX": "2",
                "SLOP_HAUS_REFRESH_ATTEMPTS": "5",
                "SLOP_HAUS_REFRESH_BACKOFF": "0.01",
            }
        )
        self.assertEqual(policy, RefreshPolicy(3, 2, 5, 0.01))

    def test_bad_env_falls_back(self) -> None:
        policy = RefreshPolicy.from_env(
            {
                "SLOP_HAUS_REFRESH_BUDGET": "$(id)",
                "SLOP_HAUS_REFRESH_ATTEMPT_MAX": "-1",
                "SLOP_HAUS_REFRESH_ATTEMPTS": "x",
                "SLOP_HAUS_REFRESH_BACKOFF": "1e3",
            }
        )
        self.assertEqual(policy, RefreshPolicy())

    def test_delay_doubles(self) -> None:
        policy = RefreshPolicy()
        self.assertEqual([policy.delay(n) for n in (1, 2, 3, 4)], [0.5, 1.0, 2.0, 4.0])

    def test_attempt_timeout_is_capped_and_floored(self) -> None:
        policy = RefreshPolicy()
        self.assertEqual(policy.attempt_timeout(7), 4)
        self.assertEqual(policy.attempt_timeout(2.5), 2.5)
        self.assertEqual(policy.attempt_timeout(0), 1)
        self.assertEqual(policy.attempt_timeout(-3), 1)
        self.assertEqual(RefreshPolicy(attempt_max=0).attempt_timeout(7), 1)

    def test_next_delay_gives_up(self) -> None:
        policy = RefreshPolicy()
        self.assertEqual(policy.next_delay(1, left=5), 0.5)
        self.assertEqual(policy.next_delay(3, left=5), 2.0)
        self.assertIsNone(policy.next_delay(4, left=5))
        self.assertIsNone(policy.next_delay(1, left=1))
        self.assertIsNone(policy.next_delay(1, left=0))

    def test_next_delay_never_overshoots_budget(self) -> None:
        policy = RefreshPolicy()
        self.assertIsNone(policy.next_delay(3, left=2.5))
        self.assertEqual(policy.next_delay(2, left=2.5), 1.0)

    def test_huge_values_do_not_crash(self) -> None:
        self.assertEqual(RefreshPolicy.from_env({"SLOP_HAUS_REFRESH_BACKOFF": "9" * 400}), RefreshPolicy())
        self.assertGreater(RefreshPolicy().delay(5000), 0)


if __name__ == "__main__":
    unittest.main()
