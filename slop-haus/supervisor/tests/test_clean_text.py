from __future__ import annotations

import unittest

from slop_haus_supervisor.text import clean_field, clean_text, split_bytes, truncate_bytes


class TextTest(unittest.TestCase):
    def test_clean_text_removes_credentials_in_urls_and_control_characters(self) -> None:
        text = clean_text("fatal: unable to access 'https://user:tok@github.com/o/r.git/'\x1b[31m\x00", 1000)
        self.assertNotIn("tok", text)
        self.assertNotIn("\x1b", text)
        self.assertIn("https://***@github.com/o/r.git/", text)

    def test_clean_text_keeps_newlines_and_clean_field_does_not(self) -> None:
        self.assertEqual("a\nb", clean_text("a\nb"))
        self.assertEqual("a b c", clean_field("a\tb\nc", 100))

    def test_text_is_cut_on_a_character_boundary(self) -> None:
        self.assertEqual(["é" * 2, "é" * 2, "é"], split_bytes("é" * 5, 4))
        for piece in split_bytes("é" * 5, 3):
            self.assertLessEqual(len(piece.encode()), 3)
        self.assertEqual("é", truncate_bytes("éé", 3))

    def test_the_limit_applies_after_redaction(self) -> None:
        text = clean_text("see https://user:tok@h/" + "é" * 50, 30)
        self.assertLessEqual(len(text.encode()), 30)
        self.assertNotIn("tok", text)
        self.assertEqual(text.encode().decode(), text, "cut on a character boundary")

    def test_a_limit_cuts_by_bytes_with_no_marker(self) -> None:
        self.assertEqual("abc", clean_text("abcdef", 3))
        self.assertEqual("é", clean_text("éé", 3))

    def test_control_characters_are_removed_but_not_newlines_or_tabs(self) -> None:
        self.assertEqual("a\nb\tc", clean_text("a\x00\nb\x1b\tc\x7f"))

    def test_clean_field_is_bounded_in_bytes(self) -> None:
        field = clean_field("é\t" * 100, 11)
        self.assertEqual("é é é é", field)
        self.assertLessEqual(len(field.encode()), 11)
