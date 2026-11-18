"""What the board's document may and may not make the supervisor do on the host.

Kept apart from the reconciler's happy path: these are the tests that must fail if a name, a field or a
removal is let through that should not be.
"""

from __future__ import annotations

import os
from pathlib import Path

from reconcile_support import ReconcilerTestCase, entry
from slop_haus_supervisor.reconcile.document import parse
from slop_haus_supervisor.workspace import config as cfg

BAD_NAMES = ["../evil", "a/b", "/etc/cron.d", "..", ".", "", "-leading-dash", "a b", "x\n", "w1;touch pwned", "$(id)", "a" * 0 + "․x"]


def tree(root: Path) -> set[str]:
    return {str(p.relative_to(root)) for p in root.rglob("*")}


class NamesOnlyTest(ReconcilerTestCase):
    def test_an_invalid_name_is_rejected_and_reported_and_never_becomes_a_path(self) -> None:
        for name in BAD_NAMES:
            with self.subTest(name=name):
                before = tree(self.tmp)
                self.want(entry(name))

                self.reconciler.tick()

                self.assertEqual("failed", self.api.last_phase()["phase"])
                self.assertEqual([], self.api.calls)
                self.assertEqual([], self.host.started)
                self.assertEqual(before, tree(self.tmp), "nothing may be created for a rejected name")
                self.assertEqual((), cfg.load(self.layout.config_file).slots)
                self.reconciler = self.build()

    def test_an_invalid_name_is_not_removed_by_a_removal_either(self) -> None:
        victim = self.tmp / "victim"
        victim.mkdir()
        (victim / "keep").write_text("x")
        self.want(entry("../../victim", state="removed", generation=2))

        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertTrue((victim / "keep").exists())

    def test_every_directory_made_is_a_child_of_the_slots_directory(self) -> None:
        self.want(entry("w1"), entry("w2", id="22222222-2222-4222-8222-222222222222"))

        self.reconciler.tick()

        made = {p.name for p in self.layout.slots.iterdir()}
        self.assertEqual({"w1", "w2"}, made)
        self.assertEqual([self.layout.slot("w1"), self.layout.slot("w2")], sorted(Path(os.path.join(self.layout.slots, n)) for n in made))

    def test_a_name_that_is_not_a_string_is_set_aside(self) -> None:
        self.want(entry(["w1"]))

        self.reconciler.tick()

        self.assertEqual([], self.api.calls)
        self.assertEqual("failed", self.api.last_phase()["phase"])


class NoPathsOrCommandsTest(ReconcilerTestCase):
    def test_a_field_that_could_carry_a_path_or_command_sets_the_entry_aside(self) -> None:
        for extra in ({"path": "/tmp/x"}, {"directory": "/tmp/x"}, {"command": "touch /tmp/pwned"}, {"cwd": "/"}, {"argv": ["sh"]}):
            with self.subTest(extra=extra):
                self.want(entry("w1", **extra))

                self.reconciler.tick()

                self.assertEqual("failed", self.api.last_phase()["phase"])
                self.assertIn(next(iter(extra)), str(self.api.last_phase()["reason"]))
                self.assertEqual([], self.api.calls)
                self.assertEqual([], self.ran)
                self.assertFalse(self.layout.slot("w1").exists())
                self.reconciler = self.build()

    def test_a_document_that_is_not_the_documented_shape_is_an_error_not_a_guess(self) -> None:
        raws: list[dict[str, object]] = [{}, {"revision": 1}, {"revision": "1", "agents": []}, {"revision": 1, "agents": "w1"}]
        for raw in raws:
            with self.subTest(raw=raw):
                with self.assertRaises(ValueError):
                    parse(raw)

    def test_a_kind_or_state_outside_the_documented_set_is_set_aside(self) -> None:
        for over in ({"kind": "shell"}, {"state": "restart"}, {"generation": -1}, {"generation": True}):
            with self.subTest(over=over):
                self.assertEqual(1, len(parse({"revision": 1, "agents": [entry("w1", **over)]}).rejected))

    def test_the_same_name_twice_is_not_made_twice(self) -> None:
        self.want(entry("w1"), entry("w1", id="22222222-2222-4222-8222-222222222222"))

        self.reconciler.tick()

        self.assertEqual(["create w1"], self.api.calls)
        self.assertEqual("failed", self.api.last_phase("22222222-2222-4222-8222-222222222222")["phase"])
        self.assertIn("twice", str(self.api.last_phase("22222222-2222-4222-8222-222222222222")["reason"]))


class UnpushedWorkTest(ReconcilerTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.want(entry("w1"))
        self.reconciler.tick()
        (self.layout.slot("w1") / "work.txt").write_text("not pushed")

    def test_a_removed_agent_with_unpushed_work_is_refused_reported_and_left_alone(self) -> None:
        self.repo.unpushed = True
        self.want(entry("w1", state="removed", generation=2))

        self.reconciler.tick()

        self.assertEqual("failed", self.api.last_phase()["phase"])
        self.assertIn("not pushed", str(self.api.last_phase()["reason"]))
        self.assertEqual("not pushed", (self.layout.slot("w1") / "work.txt").read_text())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)
        self.assertNotIn("retire w1", self.api.calls)

    def test_the_refusal_is_not_retried_until_the_back_off_has_passed(self) -> None:
        self.repo.unpushed = True
        self.want(entry("w1", state="removed", generation=2))
        self.reconciler.tick()
        asked = len(self.repo.asked)

        self.reconciler.tick()
        self.assertEqual(asked, len(self.repo.asked))

        self.repo.unpushed = False
        self.now += 301
        self.reconciler.tick()

        self.assertEqual("retired", self.api.last_phase()["phase"])
        self.assertFalse(self.layout.slot("w1").exists())
