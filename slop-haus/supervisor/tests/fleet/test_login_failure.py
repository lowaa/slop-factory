from __future__ import annotations

import unittest

from slop_haus_supervisor.fleet import login_failure
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.text import SecretMasker


class LoginFailureReasonTest(unittest.TestCase):
    def reason(self, harness: str) -> str:
        return login_failure.reason(harness, "", SecretMasker([]))

    def test_each_harness_gets_its_own_fix(self) -> None:
        for name in registry.NAMES:
            self.assertIn(f"`slop-haus harness login {name}`", self.reason(name))
        self.assertIn("restart the supervisor", self.reason("claude"))
        self.assertNotIn("restart the supervisor", self.reason("pi"))

    def test_an_unknown_harness_gets_the_generic_fix(self) -> None:
        self.assertIn("fix the harness login", self.reason("nope"))

    def test_the_registry_derived_name_lists(self) -> None:
        self.assertEqual(("claude", "pi", "codex"), registry.NAMES)
        self.assertEqual(("claude", "pi", "codex"), registry.WORKER_NAMES)
        self.assertEqual(("claude", "pi", "codex"), registry.REMOTE_CONTROL_NAMES)
