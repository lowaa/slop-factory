from __future__ import annotations

import getpass
import sys


class TerminalPrompts:
    """Questions on the terminal; with piped input each answer is the next line, so setup can be scripted."""

    def ask(self, question: str, default: str) -> str:
        if not sys.stdin.isatty():
            return sys.stdin.readline().strip() or default
        return input(f"{question} [{default}] ").strip() or default

    def ask_hidden(self, question: str) -> str:
        if not sys.stdin.isatty():
            return sys.stdin.readline().rstrip("\r\n")
        return getpass.getpass(f"{question}: ")
