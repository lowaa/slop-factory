from __future__ import annotations

import logging
import os
import queue
import threading
import time
import uuid
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from slop_haus_supervisor.harness.base import RunSpec
from slop_haus_supervisor.harness.process import HarnessSession
from slop_haus_supervisor.remote_control.host.activity import fail_open_calls
from slop_haus_supervisor.remote_control.host.attachments import AttachmentStore
from slop_haus_supervisor.remote_control.host.claude_records import ClaudeRecords
from slop_haus_supervisor.remote_control.host.conversation import Conversation, ConversationState, StateStore, WorkItem
from slop_haus_supervisor.remote_control.host.errors import Undelivered
from slop_haus_supervisor.workspace.host_files import IdleMarker
from slop_haus_supervisor.remote_control.host.live_harness import HarnessEvent, LiveHarness
from slop_haus_supervisor.remote_control.host.prompt import HOST_PREFIX
from slop_haus_supervisor.remote_control.host.records import RecordHandler
from slop_haus_supervisor.remote_control.host.settings import Settings
from slop_haus_supervisor.remote_control.repo.workspace_copy import WorkspaceCopy
from slop_haus_supervisor.remote_control.wire.errors import Refused
from slop_haus_supervisor.text import clean_text, truncate_bytes
from slop_haus_supervisor.usage.conversation import USAGE_REVISION

log = logging.getLogger(__name__)

# The agent name the harness keys its MCP config file by.
AGENT = "remote-control"
# Bounds the state file; a conversation that pushes more than this keeps the first (sorted) ones.
MAX_PUSHED_PATHS = 5000


class ReporterPort(Protocol):
    def text(self, conversation: str, text: str, block: str = "") -> int: ...
    def activity(self, conversation: str, call_id: str, tool: str, line: str, status: str) -> int: ...
    def turn_end(self, conversation: str, turn: int) -> int: ...
    def ready(self, conversation: str, work_seq: int) -> int: ...
    def failed(self, conversation: str, reason: str, work_seq: int) -> int: ...
    def repo_state(self, conversation: str, repo: dict[str, Any], work_seq: int, turn: int | None = None) -> int: ...
    def unsupported(self, conversation: str, work_kind: str) -> int: ...
    def context(self, conversation: str, tokens: int) -> int: ...
    def context_cleared(self, conversation: str) -> int: ...
    def context_compacted(self, conversation: str) -> int: ...
    def report_usage(self, conversation: str, body: dict[str, Any]) -> None: ...
    def wait_delivered(self, conversation: str, token: int, timeout: float) -> bool: ...


class SessionHarness(Protocol):
    """The harness the host converses with: one live process per conversation."""

    @property
    def name(self) -> str: ...
    def check_installed(self) -> None: ...
    def open_session(self, spec: RunSpec, session_id: str | None = None, resume: bool = False) -> HarnessSession: ...


class ConfigSource(Protocol):
    """The Remote Control's harness config, as the board has it now."""

    def changed(self) -> bool:
        """Whether it changed since the harness was last started; when it did, the next harness started has it."""
        ...


def is_uuid(value: str) -> bool:
    try:
        return str(uuid.UUID(value)) == value.lower()
    except (ValueError, AttributeError, TypeError):
        return False


class Host:
    """One Remote Control's host: the queue in, the harness and the working copy in
    the middle, the reports out. Everything that changes state runs on one
    thread (run); the poller and the harness readers only put events on its
    inbox."""

    def __init__(
        self,
        root: str,
        reporter: ReporterPort,
        harness: SessionHarness,
        settings: Settings | None = None,
        clock: Callable[[], float] | None = None,
        stopping: threading.Event | None = None,
        records: Callable[[Host], RecordHandler] | None = None,
        idle_marker: IdleMarker | None = None,
        config: ConfigSource | None = None,
        attachments: AttachmentStore | None = None,
    ) -> None:
        self.root = root
        self.idle_marker = idle_marker
        self.config = config
        self.attachments = attachments
        self.settings = settings or Settings()
        self.reporter = reporter
        self.harness = harness
        self.clock = clock or time.monotonic
        # Built here because a handler reports through the host: pi's ends a turn by finish_turn.
        self.records: RecordHandler = records(self) if records else ClaudeRecords(reporter, self)
        self.state_dir = os.path.join(root, ".slop-haus", "conductor")
        self.store = StateStore(os.path.join(self.state_dir, "conversations"))
        self.working_copy = WorkspaceCopy(root, self.settings.git_timeout)
        self.inbox: queue.Queue[Any] = queue.Queue()
        self.conv: Conversation | None = None
        self.generations = 0
        self.acked = 0
        # Messages held for the end of a turn: not handled yet, so
        # never acknowledged, or a host that dies meanwhile would lose them.
        self.unresolved: set[int] = set()
        self.batch_done = threading.Event()
        self.acked_lock = threading.Lock()
        self.stopping = stopping or threading.Event()

    # -- the loop --

    def run(self) -> None:
        while not self.stopping.is_set():
            try:
                event = self.inbox.get(timeout=self.wait_seconds())
            except queue.Empty:
                event = None
            try:
                if event is not None:
                    self.dispatch(event)
                self.tick()
            except Exception as e:  # a bug here must not end the host
                log.error("unexpected %s: %s", type(e).__name__, clean_text(str(e), 300))
            if self.idle_marker is not None:
                self.idle_marker.publish(self.is_idle())
        self.shutdown()
        if self.idle_marker is not None:
            self.idle_marker.clear()

    def is_idle(self) -> bool:
        """No harness process is alive, so nothing a restart or a workspace sync could cut off."""
        return self.conv is None or self.conv.live is None

    def wait_seconds(self) -> float:
        deadlines = [d for d in (self.idle_deadline(), self.interrupt_deadline()) if d is not None]
        if not deadlines:
            return 1.0
        return max(0.05, min(1.0, min(deadlines) - self.clock()))

    def dispatch(self, event: Any) -> None:
        if isinstance(event, HarnessEvent):
            self.on_harness(event)
        elif isinstance(event, list):
            try:
                self.on_items(event)  # pyright: ignore[reportUnknownArgumentType]
            finally:
                self.batch_done.set()

    def shutdown(self) -> None:
        if self.conv is not None:
            # The turn in flight is not settled: this host is going away, not the
            # owner's message, which the server sends again to the next one.
            self.end_harness(self.conv, settle=False)

    def idle_deadline(self) -> float | None:
        conv = self.conv
        if conv is None or conv.live is None or conv.turn_active or conv.idle_since is None:
            return None
        return conv.idle_since + self.settings.idle_timeout

    def interrupt_deadline(self) -> float | None:
        conv = self.conv
        if conv is None or not conv.turn_active:
            return None
        return conv.interrupt_deadline

    def tick(self) -> None:
        """Stops a harness that has had nothing to do for the idle timeout.
        The conversation stays live on the server; the next message resumes
        the session."""
        deadline = self.idle_deadline()
        if deadline is not None and self.clock() >= deadline and self.conv is not None:
            log.info("stopping the idle harness of %s", self.conv.state.id)
            self.end_harness(self.conv)
        deadline = self.interrupt_deadline()
        if deadline is not None and self.clock() >= deadline and self.conv is not None:
            log.info("the harness of %s did not end the turn it was asked to stop", self.conv.state.id)
            self.cut_turn(self.conv)

    # -- the queue --

    def offer(self, items: list[WorkItem]) -> None:
        """A poll's items, for the host's thread; handled() says when it is done with them."""
        self.batch_done.clear()
        self.inbox.put(items)

    def ack(self, seq: int) -> None:
        with self.acked_lock:
            self.acked = max(self.acked, seq)

    def current_ack(self) -> int:
        """The highest item that is handled, and every one before it."""
        with self.acked_lock:
            if self.unresolved:
                return min(self.acked, min(self.unresolved) - 1)
            return self.acked

    def on_items(self, items: list[WorkItem]) -> None:
        """A poll's items, in order. A conversation that is stopped later in
        the same batch is not started or messaged first."""
        stops = {i.conversation: i.seq for i in items if i.type == "stop"}
        for item in items:
            stop_seq = stops.get(item.conversation)
            if stop_seq is not None and item.seq < stop_seq and item.type != "stop":
                self.ack(item.seq)
                continue
            try:
                self.handle(item)
            except Refused as e:
                log.warning("%s %d: %s", item.type, item.seq, e)
            except Undelivered as e:
                # Not acknowledged, nor anything after it: the server sends
                # them again with the next poll.
                log.warning("%s %d: %s", item.type, item.seq, e)
                break
            except Exception as e:
                # Acknowledged all the same: an item that fails the same way
                # every time would otherwise come round for ever.
                log.error("%s %d failed unexpectedly (%s): %s", item.type, item.seq, type(e).__name__, clean_text(str(e), 300))
            self.ack(item.seq)

    def handle(self, item: WorkItem) -> None:
        if item.seq in self.unresolved:
            # Sent again because it is not acknowledged yet, and held here.
            return
        if not is_uuid(item.conversation):
            log.warning("ignoring %s %d for %r: not a conversation id", item.type, item.seq, item.conversation)
            return
        handler = {
            "start": self.on_start, "message": self.on_message, "interrupt": self.on_interrupt, "stop": self.on_stop,
            "clear": self.on_clear, "compact": self.on_compact,
        }.get(item.type)
        if handler is None:
            # Said to the server as well as logged: a host that is behind the
            # server would otherwise drop the work without anyone seeing it.
            log.warning("ignoring item %d of unsupported type %r: this host is older than the server; update and restart it", item.seq, item.type)
            self.reporter.unsupported(item.conversation, item.type)
            return
        handler(item)

    # -- start --

    def on_start(self, item: WorkItem) -> None:
        # The server queues a stop for the live conversation ahead of a start
        # that replaces it; ending whatever runs here as well means a start never
        # depends on that stop having arrived.
        previous = self.conv
        if previous is not None:
            self.end_harness(previous)
        loaded = self.store.load(item.conversation)
        state = loaded or ConversationState(id=item.conversation)
        # Whether there is a session to resume is this machine's to know, and
        # it keeps a record even of a start that failed. The server's resume
        # says only that the conversation went on before, so it is the guess for
        # a machine with no record of the conversation, and no more.
        if loaded is None:
            state.created = bool(item.raw.get("resume"))
        state.work_seq = item.seq
        conv = Conversation(state, self.store)
        self.conv = conv
        try:
            try:
                self.harness.check_installed()
            except FileNotFoundError:
                raise Refused(
                    f"{self.harness.name} is not on this machine's PATH: install it, then send a message to try again"
                ) from None
            if not state.base:
                state.base = self.working_copy.base_commit()
            if self.config is not None:
                # Nothing runs yet to restart: the harness about to start is the one that takes it up.
                self.config.changed()
            self.spawn(conv)
        except Refused as e:
            conv.save()
            self.fail(conv, str(e), item.seq)
            return
        conv.save()
        self.deliver(conv.state.id, self.reporter.ready(conv.state.id, item.seq))
        self.report_repo(conv)

    def report_repo(self, conv: Conversation, turn: int | None = None) -> None:
        """Tells the board where the repo stands. Not a step that can fail the
        conversation: a git error here is only logged."""
        try:
            if conv.snapshot is not None:
                self.keep_pushed_paths(conv)
            state = self.working_copy.repo_state(conv.state.base, conv.snapshot, conv.state.pushed_paths)
        except Refused as e:
            log.warning("repo state of %s not reported: %s", conv.state.id, e)
            return
        conv.snapshot = None
        self.reporter.repo_state(conv.state.id, state, conv.state.work_seq, turn)

    def keep_pushed_paths(self, conv: Conversation) -> None:
        assert conv.snapshot is not None
        pushed = self.working_copy.pushed_paths(conv.state.base, conv.snapshot)
        merged = sorted(set(conv.state.pushed_paths) | set(pushed))[:MAX_PUSHED_PATHS]
        if merged != conv.state.pushed_paths:
            conv.state.pushed_paths = merged
            conv.save()

    def fail(self, conv: Conversation, reason: str, work_seq: int) -> None:
        log.warning("conversation %s failed: %s", conv.state.id, clean_text(reason, 300))
        self.end_harness(conv)
        self.conv = None
        self.deliver(conv.state.id, self.reporter.failed(conv.state.id, reason, work_seq))

    def deliver(self, conversation: str, token: int) -> None:
        """Waits for the server to have what the host just reported, so the
        item that asked for it is acknowledged only once its answer is safe."""
        if not self.reporter.wait_delivered(conversation, token, self.settings.deliver_timeout):
            raise Undelivered("the server has not confirmed the report; the item stays unacknowledged")

    # -- messages --

    def on_message(self, item: WorkItem) -> None:
        conv = self.live(item.conversation)
        if conv is None:
            log.warning("message %d for a conversation this host does not hold: dropped", item.seq)
            return
        text = str(item.raw.get("text") or "")
        attached = item.raw.get("attachments")
        if not text and not attached:
            return
        if item.seq <= conv.state.last_message_seq:
            return  # handled before a restart, and sent again
        text = self.with_attachments(conv, text, attached)
        self.hold(conv, text, item.seq)
        self.run_pending(conv)

    def with_attachments(self, conv: Conversation, text: str, attached: Any) -> str:
        """The message with a line per attachment, fetched here so every harness gets them as files in the working copy."""
        if not attached or self.attachments is None:
            return text
        fetched: list[str] = []
        lines = self.attachments.lines(attached, fetched)
        if fetched:
            conv.state.attachments.extend(i for i in fetched if i not in conv.state.attachments)
            conv.save()
        return "\n".join([text, *lines]) if text else "\n".join(lines)

    def drop_attachments(self, conv: Conversation) -> None:
        if self.attachments is not None and conv.state.attachments:
            self.attachments.remove(conv.state.attachments)
        conv.state.attachments = []

    def live(self, conversation: str) -> Conversation | None:
        """The conversation, loaded from disk if the host has restarted since
        it was started."""
        if self.conv is not None and self.conv.state.id == conversation:
            return self.conv
        if self.conv is not None:
            return None
        state = self.store.load(conversation)
        if state is None:
            return None
        self.conv = Conversation(state, self.store)
        if state.usage:
            # Saved before a restart, perhaps reported never: full totals, so
            # saying them again is harmless.
            body = self.usage_body(self.conv)
            if body["records"]:
                self.reporter.report_usage(conversation, body)
        return self.conv

    def hold_seq(self, seq: int) -> None:
        with self.acked_lock:
            self.unresolved.add(seq)

    def hold(self, conv: Conversation, text: str, seq: int) -> None:
        self.hold_seq(seq)
        conv.pending.append((text, seq))

    def drop_pending(self, conv: Conversation) -> None:
        for _, seq in conv.pending:
            self.release(seq)
        conv.pending.clear()

    def release(self, seq: int) -> None:
        with self.acked_lock:
            self.unresolved.discard(seq)

    def run_pending(self, conv: Conversation) -> None:
        """Starts the next waiting message, if no turn is running."""
        while conv.pending and not conv.turn_active and self.conv is conv:
            text, seq = conv.pending.popleft()
            try:
                self.take_up_config(conv)
                if conv.live is None:
                    # Brings back a harness the idle timeout or a restart ended.
                    # The working copy is left as it is: it has moved on with the
                    # conversation's own commits, branches and switches.
                    self.spawn(conv)
                self.begin_turn(conv, text)
                # Held, not handled, until the turn has a result (settle_turn): a
                # harness or host that dies before then must see it sent again.
                conv.in_flight_seq = seq
            except Refused as e:
                self.release(seq)
                self.fail(conv, str(e), conv.state.work_seq)
                return
            except BaseException:
                self.release(seq)
                raise

    def take_up_config(self, conv: Conversation) -> None:
        """A model or effort the owner changed on the board applies from the next message: a harness
        process reads its settings only when it starts, so one started with the old config is ended
        here, between turns, and the session resumes in a new one."""
        if self.config is None or not self.config.changed():
            return
        live, conv.live = conv.live, None
        if live is None:
            return
        log.info("the harness config of %s changed: resuming the session in a harness started with it", conv.state.id)
        conv.expected_exit = True
        live.terminate()
        # The init record may have come in since the last save: the session is resumed, not started again.
        conv.save()
        self.reporter.text(conv.state.id, HOST_PREFIX + "the harness settings changed on the board, so this session continues with them.")

    def settle_turn(self, conv: Conversation) -> None:
        """The message of the turn in flight is handled: it got its result, or the
        owner was told why it never will (a stop, a harness that died)."""
        seq, conv.in_flight_seq = conv.in_flight_seq, None
        if seq is None:
            return
        conv.state.last_message_seq = max(conv.state.last_message_seq, seq)
        conv.save()
        self.release(seq)

    def begin_turn(self, conv: Conversation, text: str) -> None:
        assert conv.live is not None
        conv.current_text = text
        conv.streamed = {}
        conv.streaming_id = None
        conv.turn_active = True
        conv.idle_since = None
        try:
            conv.snapshot = self.working_copy.snapshot()
        except Refused as e:
            conv.snapshot = None
            log.warning("repo snapshot before a turn failed: %s", e)
        conv.live.send(text)

    # -- interrupt --

    def on_interrupt(self, item: WorkItem) -> None:
        """Stops the reply in flight, if it is the one the owner saw. A late or repeated request
        names a turn that is over or already being stopped, and must never stop the next one: this
        thread handles both the harness's records and these items, so the turn it sees is current."""
        conv = self.conv
        if conv is None or conv.state.id != item.conversation or not conv.turn_active or conv.live is None:
            return
        turn = item.raw.get("turn")
        if isinstance(turn, bool) or turn != conv.state.turns + 1 or conv.interrupt_requested:
            return
        conv.interrupt_requested = True
        try:
            asked = conv.live.interrupt()
        except RuntimeError:
            asked = False  # the session closed under us; the exit is on its way
        if asked:
            conv.interrupt_deadline = self.clock() + self.settings.interrupt_timeout
        else:
            self.cut_turn(conv)

    def cut_turn(self, conv: Conversation) -> None:
        """Ends the harness process under the turn in flight; the session resumes with the next
        message, and what the owner queued meanwhile is sent then. Disconnect's path, but the
        conversation stays."""
        live, conv.live = conv.live, None
        if live is not None:
            conv.expected_exit = True
            live.terminate()
        conv.state.turns += 1
        self.finish_turn(conv, lambda: None)

    # -- stop --

    def on_stop(self, item: WorkItem) -> None:
        if self.conv is not None and self.conv.state.id == item.conversation:
            self.end_harness(self.conv)
            self.drop_attachments(self.conv)
            self.conv.save()
            self.conv = None
        elif self.attachments is not None:
            # A host restarted since the conversation's messages: what it fetched is still on disk.
            state = self.store.load(item.conversation)
            if state is not None and state.attachments:
                self.attachments.remove(state.attachments)
                state.attachments = []
                self.store.save(state)

    # -- clear --

    def on_clear(self, item: WorkItem) -> None:
        """Ends the harness and starts the conversation's next one on a session of a new id, so the
        model's context is empty. The old session is left on disk and never resumed; the server's
        transcript is untouched."""
        conv = self.live(item.conversation)
        if conv is None:
            log.warning("clear %d for a conversation this host does not hold: dropped", item.seq)
            return
        # The server only sends a clear once no turn runs (or after an interrupt); a message held
        # here is the owner's and goes to the new session.
        if item.seq <= conv.state.clear_seq:
            return  # sent again because the report was not confirmed: the clear is done, and its report is on its way
        held = list(conv.pending)
        conv.pending.clear()
        if conv.turn_active:
            # An interrupt queued ahead of this clear may not have taken effect yet: the turn ends
            # as it does for any cut, reported and settled, before the session is replaced.
            self.cut_turn(conv)
        self.end_harness(conv)
        for text, seq in held:
            self.hold(conv, text, seq)
        self.drop_attachments(conv)
        conv.state.session = str(uuid.uuid4())
        conv.state.created = False
        conv.state.clear_seq = item.seq
        conv.state.context_tokens = 0
        conv.state.codex_totals = {}
        conv.state.pi_stats = {}
        # A new session's modelUsage counts from zero, not from the old session's last result.
        conv.usage.reported = {}
        conv.retried_session = False
        conv.save()
        # The server sets context_tokens to 0 with the divider.
        conv.reported_context = 0
        self.deliver(conv.state.id, self.reporter.context_cleared(conv.state.id))
        self.run_pending(conv)

    # -- compact --

    def capabilities(self, base: list[str]) -> list[str]:
        return [*base, "compact"] if self.records.can_compact else list(base)

    def on_compact(self, item: WorkItem) -> None:
        """Has the harness compact the session's context, as a turn of its own: the owner's messages
        wait behind it, and its end reports the divider and the new context size."""
        conv = self.live(item.conversation)
        if conv is None:
            log.warning("compact %d for a conversation this host does not hold: dropped", item.seq)
            return
        if item.seq <= conv.state.compact_seq or not self.records.can_compact:
            return
        if conv.turn_active:
            # The server sends a compact only once no turn runs; a turn started since is the owner's.
            log.warning("compact %d while a turn runs: dropped", item.seq)
            return
        try:
            self.take_up_config(conv)
            if conv.live is None:
                self.spawn(conv)
            assert conv.live is not None
            conv.compacting = True
            conv.compact_error = ""
            conv.current_text = ""
            conv.turn_active = True
            conv.idle_since = None
            # Unresolved until the compaction ends, as a message is: a host that dies under it sees the item again.
            self.hold_seq(item.seq)
            self.records.compact(conv, conv.live)
        except Refused as e:
            self.release(item.seq)
            self.fail(conv, str(e), conv.state.work_seq)
            return
        except RuntimeError:
            conv.compacting, conv.turn_active = False, False
            self.release(item.seq)
            raise Refused("the harness closed before it could compact") from None
        conv.in_flight_seq = item.seq
        conv.save()

    # -- the harness --

    def spawn(self, conv: Conversation) -> None:
        # Counted across conversations, not per conversation: a harness that was
        # ended answers with its exit a moment later, and when its conversation
        # has been started again by then that exit must not be taken for the
        # new harness's.
        self.generations += 1
        conv.generation = self.generations
        conv.expected_exit = False
        spec = RunSpec(agent=AGENT, directory=Path(self.root), ticket_id=0, branch="", prompt="")
        try:
            session = self.harness.open_session(spec, conv.state.session_id, resume=conv.state.created)
        except OSError as e:
            raise Refused(f"could not start {self.harness.name}: {e}") from e
        conv.live = LiveHarness(session, conv.state.id, conv.generation, self.inbox, started=not self.records.retries_session)
        conv.idle_since = self.clock()

    def end_harness(self, conv: Conversation, settle: bool = True) -> None:
        live, conv.live = conv.live, None
        was_active, conv.turn_active = conv.turn_active, False
        conv.compacting = False
        self.clear_interrupt(conv)
        self.drop_pending(conv)
        if settle:
            self.settle_turn(conv)
        if live is not None:
            conv.expected_exit = True
            live.terminate()
            # The init record may have come in since the last save: a session
            # this machine made must be resumed, not started again.
            conv.save()
            if was_active:
                # What the turn did before it was cut short is still on disk, and
                # the board's overlap warning should be about the same thing.
                self.report_repo(conv, conv.state.turns)

    def on_harness(self, event: HarnessEvent) -> None:
        conv = self.conv
        if conv is None or conv.state.id != event.conversation or conv.generation != event.generation:
            return
        if event.record is None:
            self.on_exit(conv, event.exit_code)
            return
        live = conv.live
        if live is None:
            # A late record from a harness that has been ended.
            return
        self.records.on_record(conv, live, event.record)

    def on_turn_result(self, conv: Conversation, record: dict[str, Any], add_usage: Callable[[], None]) -> None:
        if record.get("is_error") and not conv.interrupt_requested:
            self.report_turn_error(conv, record)
        self.finish_turn(conv, add_usage)

    def finish_turn(self, conv: Conversation, add_usage: Callable[[], None]) -> None:
        """What ends a turn: the turn is reported, then the repo and the usage,
        and the next waiting message starts."""
        if conv.interrupt_requested:
            self.reporter.text(conv.state.id, HOST_PREFIX + "stopped by you.")
        self.clear_interrupt(conv)
        self.reporter.turn_end(conv.state.id, conv.state.turns)
        self.report_repo(conv, conv.state.turns)
        add_usage()
        conv.save()
        self.finish_compaction(conv)
        self.report_context(conv)
        body = self.usage_body(conv)
        # The server refuses a report with no records.
        if body["records"]:
            self.reporter.report_usage(conv.state.id, body)
        conv.turn_active = False
        conv.current_text = ""
        conv.idle_since = self.clock()
        self.settle_turn(conv)
        self.run_pending(conv)

    def finish_compaction(self, conv: Conversation) -> None:
        if not conv.compacting:
            return
        conv.compacting = False
        conv.state.compact_seq = max(conv.state.compact_seq, conv.in_flight_seq or 0)
        conv.save()
        error, conv.compact_error = conv.compact_error, ""
        if error:
            self.report_error_text(conv, error)
        else:
            self.reporter.context_compacted(conv.state.id)
            # The size after is reported even if it equals the one before the divider.
            conv.reported_context = None

    def report_context(self, conv: Conversation) -> None:
        tokens = conv.state.context_tokens
        if tokens > 0 and tokens != conv.reported_context:
            conv.reported_context = tokens
            self.reporter.context(conv.state.id, tokens)

    @staticmethod
    def clear_interrupt(conv: Conversation) -> None:
        conv.interrupt_requested = False
        conv.interrupt_deadline = None

    def report_turn_error(self, conv: Conversation, record: dict[str, Any]) -> None:
        # An API or login failure ends a turn with nothing streamed; the owner
        # should see why rather than a silent end.
        self.report_error_text(conv, str(record.get("result") or record.get("subtype") or "the turn failed"))

    def report_error_text(self, conv: Conversation, reason: str) -> None:
        reason = self.records.explain(clean_text(reason))
        self.reporter.text(conv.state.id, HOST_PREFIX + "the turn ended with an error: " + truncate_bytes(reason, 1500))

    def usage_body(self, conv: Conversation) -> dict[str, Any]:
        harness = self.harness.name
        key = conv.state.id + (":" + conv.usage.key_suffix if conv.usage.key_suffix else "")
        return {
            "transcript_key": key, "harness": harness, "session_id": conv.state.id, "scope": "main",
            "conversation_id": conv.state.id, "records": conv.usage.records(conv.state.billing_mode, harness),
            "source": "host", "reporter_revision": USAGE_REVISION,
        }

    def on_exit(self, conv: Conversation, code: int | None) -> None:
        live = conv.live
        fail_open_calls(conv, self.reporter)
        if conv.expected_exit:
            return
        stderr = self.records.describe_exit(live.stderr_tail()) if live is not None else ""
        started = live.started if live is not None else False
        startup_result = live.startup_result if live is not None else None
        conv.live = None
        if live is not None:
            # Whatever the model's commands left running outlives a harness that died on its own.
            live.terminate()
        # The two start-up refusals that mean this host guessed wrongly about
        # whether the session exists. Neither gets as far as a session, though
        # one does print a result first.
        if live is not None and not started and not conv.retried_session:
            wrong_guess = (not conv.state.created and "already in use" in stderr) or (
                conv.state.created and "No conversation found" in stderr
            )
            if wrong_guess:
                conv.retried_session = True
                conv.state.created = not conv.state.created
                if not conv.state.created:
                    self.reporter.text(
                        conv.state.id,
                        HOST_PREFIX + "this machine has no session for the conversation, so it continues in a new one without the earlier messages.",
                    )
                try:
                    self.spawn(conv)
                    if conv.turn_active and conv.live is not None:
                        conv.live.send(conv.current_text)
                    return
                except Refused as e:
                    stderr = str(e)
        if startup_result is not None and startup_result.get("is_error"):
            self.report_turn_error(conv, startup_result)
        was_active = conv.turn_active
        conv.turn_active = False
        self.clear_interrupt(conv)
        self.drop_pending(conv)
        self.settle_turn(conv)
        detail = ": " + stderr if stderr else ""
        self.reporter.text(conv.state.id, truncate_bytes(f"{HOST_PREFIX}the harness exited (status {code}){detail}", 1800))
        if was_active:
            self.reporter.turn_end(conv.state.id, conv.state.turns)
            self.report_repo(conv, conv.state.turns)
