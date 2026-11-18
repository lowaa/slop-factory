from __future__ import annotations

import unittest

from harness_fixtures import ALL, NOW, Sample
from slop_haus_supervisor.harness import classify
from slop_haus_supervisor.harness.base import OutcomeKind


class TestClassifyText(unittest.TestCase):
    def test_every_sample(self) -> None:
        for sample in ALL:
            with self.subTest(sample.name):
                failure = classify.classify_text(sample.text, NOW)
                self.assertEqual(sample.kind, failure.kind if failure else None)
                self.assertEqual(sample.reset_at, failure.reset_at if failure else None)

    def test_a_usage_limit_is_not_taken_for_a_transient_or_auth_failure(self) -> None:
        text = "API Error: 429 rate_limit_error: You've hit your limit · Please run /login"
        failure = classify.classify_text(text, NOW)
        assert failure is not None
        self.assertEqual(OutcomeKind.USAGE_LIMIT, failure.kind)

    def test_clock_reset_rolls_to_the_next_day_when_already_past(self) -> None:
        # 10:00 UTC is 06:00 EDT in New York, so 3am there is 21 hours on.
        sample = Sample("", "resets 3am (America/New_York)", None)
        reset = classify.reset_time(sample.text, NOW)
        assert reset is not None
        self.assertEqual(NOW + 21 * 3600, reset)

    def test_unknown_zone_gives_no_reset(self) -> None:
        self.assertIsNone(classify.reset_time("resets 3pm (Nowhere/Land)", NOW))

    def test_resume_failures(self) -> None:
        self.assertTrue(classify.is_resume_failure("No conversation found with session ID: x"))
        self.assertTrue(classify.is_resume_failure("Error: Session ID x is already in use."))
        self.assertFalse(classify.is_resume_failure("API Error: 529"))


if __name__ == "__main__":
    unittest.main()
