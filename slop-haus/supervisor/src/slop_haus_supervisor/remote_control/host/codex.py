from __future__ import annotations

import dataclasses
import uuid
from collections.abc import Sequence
from pathlib import Path

from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.codex_login import CodexLogin
from slop_haus_supervisor.harness.codex_protocol import is_uuid
from slop_haus_supervisor.harness.codex_session import CodexSession
from slop_haus_supervisor.harness.process import HarnessProcess, HarnessSession
from slop_haus_supervisor.harness.variant import parse_variant
from slop_haus_supervisor.remote_control.host.conversation import atomic_write


class RemoteControlCodex(CodexHarness):
    """Codex as the Remote Control starts it: one `codex app-server` per conversation, kept live.

    Its login and settings live in <state>/codex-home, where a person has already logged it in. Codex
    names its threads itself, so the thread of each conversation is kept in <state>/codex-threads.
    """

    def __init__(self, state_dir: Path, server: str, system_prompt: str, variant: str = "", *,
                 command: Sequence[str] = ("codex",), grace: float = 5.0,
                 login_dir: Path | None = None, login: CodexLogin | None = None) -> None:
        super().__init__(state_dir, server, command=command, grace=grace, login=login)
        self._system_prompt = system_prompt
        self._variant = variant
        self._login_dir = login_dir

    def use_variant(self, variant: str) -> None:
        """The model and effort of the sessions it starts from now on."""
        self._variant = variant

    def config_dir(self, root: Path) -> Path:
        """`login_dir` is the supervisor's Codex home, shared with its worker slots: settings go per thread
        (thread_params), never into a config.toml there."""
        return self._login_dir or root / "codex-home"

    def threads_dir(self) -> Path:
        return self._root / "codex-threads"

    def open_session(self, spec: RunSpec, session_id: str | None = None, resume: bool = False) -> HarnessSession:
        """A live process for the caller to converse with; `spec.prompt` is not sent."""
        conversation = session_id or str(uuid.uuid4())
        if not is_uuid(conversation):
            # It names a file.
            raise ValueError(f"conversation id {conversation!r} is not a UUID")
        # The briefing is not assumed to be persisted by Codex, so it goes on a resume too.
        spec = dataclasses.replace(spec, variant=self._variant, system_prompt=self._system_prompt)
        plan = self.plan(spec)
        # Whatever the host's state says: a host that died between Codex naming the thread and saving
        # `created` must find the thread, not start a second.
        thread = self._thread_of(conversation)
        process = HarnessProcess(plan.argv, plan.env, plan.cwd)
        return CodexSession(
            process, self.thread_params(spec), thread, parse_variant(spec.variant).effort,
            lambda thread_id: self._remember(conversation, thread_id), conversation, self._grace,
            fresh=resume and not thread,
        )

    def _thread_of(self, conversation: str) -> str:
        try:
            thread = (self.threads_dir() / conversation).read_text(encoding="utf-8").strip()
        except OSError:
            return ""
        return thread if is_uuid(thread) else ""

    def _remember(self, conversation: str, thread: str) -> None:
        atomic_write(str(self.threads_dir() / conversation), thread)
