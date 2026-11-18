from __future__ import annotations

import os
import stat
import threading
import unittest.mock

from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.envfile import EnvFile, EnvFileError
from support import TempDirTestCase


class ParseTest(TempDirTestCase):
    def test_plain_export_comments_and_blank_lines(self) -> None:
        text = "# note\n\nA=1\nexport B=two\n  export   C = 3  \nD=x # trailing\nE=a#b\n"

        self.assertEqual({"A": "1", "B": "two", "C": "3", "D": "x", "E": "a#b"}, envfile.parse(text))

    def test_quotes_are_removed_and_hold_spaces_and_hashes(self) -> None:
        text = "A=\"has space # not a comment\"\nB='single $HOME'\nC=\"say \\\"hi\\\"\\\\\" # c\nD=''\n"

        self.assertEqual(
            {"A": "has space # not a comment", "B": "single $HOME", "C": 'say "hi"\\', "D": ""}, envfile.parse(text)
        )

    def test_command_substitution_is_inert(self) -> None:
        marker = self.tmp / "ran"
        text = f"A=$(touch {marker})\nB=`touch {marker}`\nC=\"$(touch {marker})\"\n$(touch {marker})\nD=${{HOME}}\n"

        parsed = envfile.parse(text)

        self.assertFalse(marker.exists())
        self.assertEqual({"A": f"$(touch {marker})", "B": f"`touch {marker}`", "C": f"$(touch {marker})", "D": "${HOME}"}, parsed)

    def test_lines_that_are_not_assignments_are_skipped(self) -> None:
        self.assertEqual({"OK": "1"}, envfile.parse("nonsense\n1BAD=x\nA B=y\nOPEN=\"never closed\nOK=1\n"))

    def test_the_last_of_a_repeated_key_wins(self) -> None:
        self.assertEqual({"A": "2"}, envfile.parse("A=1\nA=2\n"))


class FormatTest(TempDirTestCase):
    def test_a_value_survives_a_round_trip(self) -> None:
        for value in ["plain", "with space", "it's", 'say "hi"', "$(x) `y` $Z", "a#b c", "back\\slash", "", " lead", "=x", "'\""]:
            with self.subTest(value=value):
                text = envfile.with_value("", "K", value)
                self.assertEqual({"K": value}, envfile.parse(text))

    def test_control_characters_and_bad_names_are_refused(self) -> None:
        with self.assertRaises(EnvFileError):
            envfile.format_assignment("K", "a\nb")
        with self.assertRaises(EnvFileError):
            envfile.format_assignment("BAD KEY", "v")

    def test_setting_replaces_in_place_and_drops_repeats(self) -> None:
        text = "# keep\nA=1\nexport B=2\nA=3\n"

        self.assertEqual("# keep\nA=new\nexport B=2\n".replace("export ", ""), envfile.with_value(text, "A", "new").replace("export ", ""))
        self.assertEqual({"A": "new", "B": "2"}, envfile.parse(envfile.with_value(text, "A", "new")))

    def test_unsetting_removes_every_assignment_and_keeps_the_rest(self) -> None:
        text, removed = envfile.without("# c\nA=1\nB=2\nA=3\n", "A")

        self.assertTrue(removed)
        self.assertEqual("# c\nB=2\n", text)
        self.assertEqual(("# c\n", False), envfile.without("# c\n", "A"))


class EnvFileTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.path = self.tmp / "secrets.env"
        self.file = EnvFile(self.path)

    def mode(self) -> int:
        return stat.S_IMODE(self.path.stat().st_mode)

    def test_a_new_file_is_private_and_a_widened_one_is_narrowed_on_write(self) -> None:
        self.file.set("A", "1")
        self.assertEqual(0o600, self.mode())

        self.path.chmod(0o644)
        self.file.set("B", "2")

        self.assertEqual(0o600, self.mode())
        self.assertEqual({"A": "1", "B": "2"}, self.file.values())

    def test_a_write_leaves_no_temporary_file_behind(self) -> None:
        self.file.set("A", "1")
        self.file.unset("A")

        self.assertEqual({"secrets.env", ".secrets.env.lock"}, set(os.listdir(self.tmp)))

    def test_a_failed_write_keeps_the_old_file(self) -> None:
        self.file.set("A", "1")

        with self.assertRaises(EnvFileError):
            self.file.set("A", "bad\nvalue")

        self.assertEqual({"A": "1"}, self.file.values())

    def test_the_lock_keeps_edits_from_overlapping(self) -> None:
        inside = 0
        overlapped = threading.Event()
        counter = threading.Lock()
        entered = threading.Event()
        real = envfile.with_value

        def slow_edit(text: str, key: str, value: str) -> str:
            nonlocal inside
            with counter:
                inside += 1
                if inside > 1:
                    overlapped.set()
            # Holds the read-modify-write open until a rival could have joined it; unlocked, one does.
            if not entered.is_set():
                entered.set()
                overlapped.wait(0.3)
            with counter:
                inside -= 1
            return real(text, key, value)

        threads = [threading.Thread(target=self.file.set, args=(f"K{i}", str(i))) for i in range(4)]
        with unittest.mock.patch.object(envfile, "with_value", slow_edit):
            for t in threads:
                t.start()
            for t in threads:
                t.join()

        self.assertFalse(overlapped.is_set())
        self.assertEqual({f"K{i}": str(i) for i in range(4)}, self.file.values())

    def test_unsetting_reports_whether_anything_was_there(self) -> None:
        self.assertFalse(self.file.unset("A"))
        self.file.set("A", "1")
        self.assertTrue(self.file.unset("A"))
        self.assertEqual({}, self.file.values())
