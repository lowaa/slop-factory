from __future__ import annotations

import json
import unittest

from slop_haus_supervisor.review.result import RunResult, parse_run


def output(review: object, report: object | None = None) -> str:
    whole: dict[str, object] = {"review": review, "markdown": "# review"}
    if report is not None:
        whole["report"] = report
    return json.dumps(whole)


class ParseRunTest(unittest.TestCase):
    def test_a_posted_review_carries_its_verdict_and_finding_count(self) -> None:
        stdout = output({"verdict": "request_changes", "findings": [{"file": "a"}, {"file": "b"}]}, {"run_id": "r1"})
        self.assertEqual(
            RunResult("request_changes", 2, incomplete=False, reported=True),
            parse_run(0, stdout, ""),
        )

    def test_an_incomplete_review_that_was_not_posted_says_so(self) -> None:
        result = parse_run(0, output({"verdict": "incomplete", "findings": [], "incomplete": True}), "")
        self.assertEqual((True, False, False), (result.incomplete, result.reported, result.failed))

    def test_a_non_zero_exit_is_a_failure_named_by_the_last_stderr_line(self) -> None:
        result = parse_run(1, output({"verdict": "approve", "findings": []}), "starting\nslop-review: provider claude: usage limit\n\n")
        self.assertTrue(result.failed)
        self.assertEqual("exit 1: slop-review: provider claude: usage limit", result.error)

    def test_a_failure_with_no_stderr_names_the_exit_code(self) -> None:
        self.assertEqual("exit 2", parse_run(2, "", "").error)

    def test_a_long_stderr_line_is_cut(self) -> None:
        self.assertLessEqual(len(parse_run(1, "", "x" * 5000).error), 220)

    def test_output_that_is_not_a_review_is_a_failure(self) -> None:
        for stdout in ("", "not json", "[]", output("approve"), output({"verdict": "lgtm", "findings": []}), output({"findings": []})):
            result = parse_run(0, stdout, "")
            self.assertTrue(result.failed, stdout)
            self.assertEqual("", result.verdict)
