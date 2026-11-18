from __future__ import annotations

import json
import logging
import re
import threading
from collections import deque
from typing import Any, Protocol

from slop_haus_supervisor.remote_control.wire.client import JsonObject
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.remote_control.wire.outgoing import Outgoing
from slop_haus_supervisor.text import clean_field, clean_text, split_bytes

log = logging.getLogger(__name__)

# The server's limits (conductor.go), kept inside them here so a report is
# never refused whole for one oversized field.
MAX_DELTA_BYTES = 64 * 1024
MAX_EVENTS_PER_BATCH = 200
# The server refuses a report body over 1MB (conductor routes.go); a batch stays
# well inside it, which 200 events of 64KB of text, or of repo state, would not.
MAX_BATCH_BYTES = 512 * 1024
# The most records the server takes in one usage report (llmusage.MaxRecordsPerReport).
MAX_USAGE_RECORDS = 32
MAX_FAILURE_BYTES = 2048
# The server's limit on the work kind named in an unsupported report.
MAX_WORK_KIND_BYTES = 40
# The server refuses a block id longer than this (conductor.MaxBlockBytes).
MAX_BLOCK_BYTES = 128


class Poster(Protocol):
    def post(self, path: str, body: JsonObject, timeout: float) -> JsonObject: ...


class Reporter:
    """What the host tells slop-haus about a conversation, in order, once.

    The server numbers nothing for the host: each conversation's events carry
    the host's own host_seq, from the server's last acknowledged number
    onward, which is learned from the server (an empty report answers with
    it) rather than remembered, so a restarted host carries on where the
    server is. A batch that was sent is sent again as it was until the server
    has it, since the server skips what it has by number: text merged into a
    batch later would otherwise be skipped as already stored.

    Text coalesces while a report is in flight, so the model's output goes
    out as often as the network allows, in batches.
    """

    def __init__(self, client: Poster, host_id: str, stop: threading.Event, min_interval: float = 0.25,
                 retry_delay: float = 1.0, max_backoff: float = 30.0, drain_timeout: float = 40.0) -> None:
        self.client = client
        self.host_id = host_id
        self.stop = stop
        self.drain_timeout = drain_timeout
        self.min_interval = min_interval
        self.retry_delay = retry_delay
        self.max_backoff = max_backoff
        self.cond = threading.Condition()
        self.outbox: dict[str, deque[Outgoing]] = {}
        self.in_flight: dict[str, list[tuple[int, Outgoing]]] = {}
        self.next_seq: dict[str, int] = {}
        self.usage: dict[str, dict[str, Any]] = {}
        self.issued = 0
        self.delivered: dict[str, int] = {}
        self.thread = threading.Thread(target=self.run, name="reporter", daemon=True)

    def start(self) -> None:
        self.thread.start()

    def drain(self) -> None:
        """After stop is set: one last attempt at whatever is waiting, so an
        interruption does not drop what the model just said."""
        # The stop event is set by a signal handler, which cannot take the
        # condition to notify it: an idle reporter would sleep through the
        # join's whole timeout.
        with self.cond:
            self.cond.notify_all()
        self.thread.join(timeout=self.drain_timeout)
        if self.thread.is_alive():
            # Still inside a flush, which owns in_flight and next_seq without
            # the lock: a second flusher would send the same batch twice or
            # number events over it. The daemon thread finishes what it began.
            log.warning("the reporter is still sending: not starting another flush")
            return
        try:
            self.flush_once()
        except (Unreachable, ServerError) as e:
            log.warning("could not report everything before stopping: %s", clean_text(str(e), 300))

    # -- called by the host --

    def submit(self, conversation: str, kind: str, **fields: Any) -> int:
        with self.cond:
            self.issued += 1
            self.outbox.setdefault(conversation, deque()).append(Outgoing(token=self.issued, kind=kind, **fields))
            self.cond.notify_all()
            return self.issued

    def text(self, conversation: str, text: str, block: str = "") -> int:
        return self.submit(conversation, "assistant", text=text, block=clean_field(block, MAX_BLOCK_BYTES))

    def activity(self, conversation: str, call_id: str, tool: str, line: str, status: str) -> int:
        """A tool call began or ended: its name and one line, never its input or output."""
        return self.submit(conversation, "activity", activity={"call_id": call_id, "tool": tool, "line": line, "status": status})

    def turn_end(self, conversation: str, turn: int) -> int:
        return self.submit(conversation, "turn_end", turn=turn)

    def ready(self, conversation: str, work_seq: int) -> int:
        return self.submit(conversation, "ready", work_seq=work_seq)

    def repo_state(self, conversation: str, repo: dict[str, Any], work_seq: int, turn: int | None = None) -> int:
        return self.submit(conversation, "repo_state", repo=repo, work_seq=work_seq, turn=turn)

    def context(self, conversation: str, tokens: int) -> int:
        return self.submit(conversation, "context", context_tokens=max(tokens, 0))

    def context_cleared(self, conversation: str) -> int:
        """The divider; the server sets context_tokens to 0 with it."""
        return self.submit(conversation, "context_cleared")

    def context_compacted(self, conversation: str) -> int:
        """The divider; the context size that follows is reported with `context`."""
        return self.submit(conversation, "context_compacted")

    def failed(self, conversation: str, reason: str, work_seq: int) -> int:
        return self.submit(conversation, "failed", reason=clean_text(reason, MAX_FAILURE_BYTES) or "failed", work_seq=work_seq)

    def unsupported(self, conversation: str, work_kind: str) -> int:
        return self.submit(conversation, "unsupported", reason=clean_text(work_kind, MAX_WORK_KIND_BYTES) or "unknown")

    def report_usage(self, conversation: str, body: dict[str, Any]) -> None:
        # Full totals, so only the newest matters.
        with self.cond:
            self.usage[conversation] = body
            self.cond.notify_all()

    def wait_delivered(self, conversation: str, token: int, timeout: float) -> bool:
        """Whether the server has the event submitted as token, waiting up to
        timeout. An event the server refused for good counts as done: there
        is nothing more to deliver."""
        with self.cond:
            self.cond.wait_for(lambda: self.delivered.get(conversation, 0) >= token or self.stop.is_set(), timeout)
            return self.delivered.get(conversation, 0) >= token

    # -- the sending thread --

    def run(self) -> None:
        backoff = self.min_interval
        while not self.stop.is_set():
            with self.cond:
                self.cond.wait_for(lambda: self.has_work() or self.stop.is_set())
                if self.stop.is_set():
                    return
            try:
                self.flush_once()
                backoff = self.min_interval
            except (Unreachable, ServerError) as e:
                backoff = min(max(backoff * 2, self.retry_delay), self.max_backoff)
                log.warning("report failed, trying again in %.0fs: %s", backoff, clean_text(str(e), 300))
            self.stop.wait(backoff)

    def has_work(self) -> bool:
        return bool(self.usage) or bool(self.in_flight) or any(self.outbox.values())

    def flush_once(self) -> None:
        """One pass over everything pending. Raises if the server could not
        be reached or refused something worth another try."""
        with self.cond:
            conversations = sorted(set(self.outbox) | set(self.in_flight))
            usage = dict(self.usage)
        for conversation in conversations:
            self.flush_conversation(conversation)
        for conversation, body in usage.items():
            self.post_usage(conversation, body)

    def flush_conversation(self, conversation: str) -> None:
        if conversation not in self.next_seq:
            self.next_seq[conversation] = self.acked(conversation) + 1
        while True:
            batch = self.in_flight.get(conversation) or self.take_batch(conversation)
            if not batch:
                return
            self.in_flight[conversation] = batch
            body = {"host_id": self.host_id, "events": [o.wire(seq) for seq, o in batch]}
            try:
                reply = self.client.post(f"/api/conductor/conversations/{conversation}/events", body, timeout=30)
            except ServerError as e:
                if e.status == 409 and self.resync(conversation, batch, e.detail):
                    continue
                if e.status in (404, 422):
                    # Not something sending again will fix; the next batch is
                    # numbered from what the server has, so nothing wedges.
                    log.warning("dropping %d events the server refused (%d: %s)", len(batch), e.status, e.detail)
                    self.finish(conversation, batch, acked=None)
                    continue
                raise
            self.finish(conversation, batch, acked=int(reply.get("acked", 0)))

    def acked(self, conversation: str) -> int:
        reply = self.client.post(f"/api/conductor/conversations/{conversation}/events",
                                 {"host_id": self.host_id, "events": []}, timeout=30)
        return int(reply.get("acked", 0))

    def take_batch(self, conversation: str) -> list[tuple[int, Outgoing]]:
        """The next batch, numbered. Consecutive text of one block is one event: text of
        another block stays apart, so the board can show the model's messages separately."""
        with self.cond:
            box = self.outbox.get(conversation)
            if not box:
                return []
            batch: list[tuple[int, Outgoing]] = []
            seq = self.next_seq[conversation]
            size = 0
            while box and len(batch) < MAX_EVENTS_PER_BATCH and size < MAX_BATCH_BYTES:
                item = box.popleft()
                if item.kind == "assistant":
                    text, token, block = item.text, item.token, item.block
                    while (box and box[0].kind == "assistant" and box[0].block == block
                           and len((text + box[0].text).encode()) <= MAX_DELTA_BYTES):
                        nxt = box.popleft()
                        text, token = text + nxt.text, nxt.token
                    pieces = split_bytes(text, MAX_DELTA_BYTES)
                    for i, piece in enumerate(pieces):
                        batch.append((seq, Outgoing(token=token if i == len(pieces) - 1 else 0, kind="assistant", text=piece, block=block)))
                        seq += 1
                        size += len(piece.encode())
                        if size >= MAX_BATCH_BYTES and i < len(pieces) - 1:
                            # What does not fit goes first in the next batch.
                            box.appendleft(Outgoing(token=token, kind="assistant", text="".join(pieces[i + 1:]), block=block))
                            break
                else:
                    batch.append((seq, item))
                    seq += 1
                    size += len(json.dumps(item.repo)) if item.repo is not None else 256
            self.next_seq[conversation] = seq
            return batch

    def resync(self, conversation: str, batch: list[tuple[int, Outgoing]], detail: str) -> bool:
        """After a 409 for a gap: the server says what it has, and what is
        left is numbered again from there."""
        found = re.search(r"host_seq (\d+)", detail)
        if not found:
            return False
        acked = int(found.group(1))
        kept = [o for seq, o in batch if seq > acked]
        self.in_flight[conversation] = [(acked + 1 + i, o) for i, o in enumerate(kept)]
        self.next_seq[conversation] = acked + 1 + len(kept)
        return True

    def finish(self, conversation: str, batch: list[tuple[int, Outgoing]], acked: int | None) -> None:
        with self.cond:
            self.in_flight.pop(conversation, None)
            tokens = [o.token for _, o in batch if o.token]
            if tokens:
                self.delivered[conversation] = max(self.delivered.get(conversation, 0), max(tokens))
            if acked is not None:
                self.next_seq[conversation] = max(self.next_seq.get(conversation, 1), acked + 1)
            self.cond.notify_all()

    def post_usage(self, conversation: str, body: dict[str, Any]) -> None:
        # The server takes a bounded number of records a report, and a report
        # replaces the last one under its transcript_key: the overflow goes
        # under keys of its own (the server allows "<conversation>:<suffix>"),
        # or each part would erase the one before.
        records: list[dict[str, Any]] = body.get("records") or []
        parts = [records[i:i + MAX_USAGE_RECORDS] for i in range(0, len(records), MAX_USAGE_RECORDS)] or [records]
        for n, part in enumerate(parts):
            extra = {"transcript_key": f"{body.get('transcript_key', conversation)}:usage-part-{n}"} if n else {}
            try:
                self.client.post("/hooks/usage", dict(body, records=part, **extra), timeout=30)
            except ServerError as e:
                if e.status in (401, 403, 429) or e.status >= 500:
                    raise
                # A report the server will not take is not made better by
                # repeating it.
                log.warning("usage for %s refused (%d: %s)", conversation, e.status, e.detail)
        with self.cond:
            if self.usage.get(conversation) is body:
                del self.usage[conversation]
