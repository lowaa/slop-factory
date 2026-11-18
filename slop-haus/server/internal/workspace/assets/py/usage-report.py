"""slop-haus LLM usage report, for hook-usage.sh.

Reads a Claude Code Stop or SubagentStop hook payload on stdin, sums the
transcript it names, and writes the body of a POST /hooks/usage on stdout:
full totals per model -- requests, tokens, and per-tool calls, errors, time
and result bytes -- in go-llm-usage's Record format. Writes nothing when
there is nothing to report.

- Stop reports the main session's transcript (`transcript_path`), keyed by
  the session id, scope `main`.
- SubagentStop reports the subagent's own (`agent_transcript_path`), keyed
  `<session_id>:<agent_id>`, scope `subagent:<agent_type>`.

Those field names are the ones Claude Code 2.1.286 sends, measured rather
than read from its docs, which don't list them. A subagent's transcript is
a file of its own and holds none of its parent's or children's messages, so
each transcript is counted once, by whoever owns it.

Claude Code writes a transcript line per content block, each repeating the
message's usage, and while the message streams the later lines carry a
larger output_tokens. So usage is taken once per message id -- the largest
of each count across its lines -- never summed per line.

Only program names leave the machine for a bash call: a command line can
hold a secret. See extract_commands.

It is reporting on a hook that must never hold an agent up, so every
failure is logged on stderr and the exit status is always 0. A transcript
past the size or time budget is skipped whole rather than reported in part:
a report is full totals, and a partial one would replace better numbers.

Installed into a workspace by setup and replaced by every update.
"""

import json
import os
import re
import sys
import time
from datetime import datetime, timezone

MAX_TRANSCRIPT_BYTES = 512 * 1024 * 1024
# A line past this is a huge tool result, never a usage-bearing message
# worth reading into memory; it is skipped.
MAX_LINE_BYTES = 16 * 1024 * 1024
TIME_BUDGET_SECONDS = 20.0
# Claude Code fires the hook before it has flushed the turn's last message
# to the transcript -- measured on 2.1.286, where a Stop hook reading at
# once missed the final reply every time. So the transcript is read once it
# has stopped growing for this long, or after MAX_SETTLE_SECONDS regardless.
# The hook is async, so the wait holds nothing up.
SETTLE_SECONDS = 1.0
MAX_SETTLE_SECONDS = 5.0
SETTLE_POLL_SECONDS = 0.25

PROVIDER = "anthropic"
HARNESS = "claude"
# Claude Code here runs on a Max subscription: the transcript carries
# tokens and no cost, and the server's estimate is only an estimate.
# A console session signed in with an ANTHROPIC_API_KEY would be recorded as
# a subscription too: neither the transcript nor the Stop payload says where
# the credential came from (the stream-json init record's apiKeySource does,
# and a hook never sees it), and the hook's environment is not proof, since
# Claude Code may hold the variable and still use a login. The supervisor's
# runs read apiKeySource and never reach this script (hook-usage.sh).
BILLING_MODE = "subscription"

# The server's patterns and limits: a report with one field outside them is
# refused whole, so what doesn't fit is left out, or counted as "other",
# here first. \Z and not $, which also matches before a trailing newline;
# the shared go-llm-usage/fixtures/record_labels.json is run against these.
MODEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,127}\Z")
TOOL = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}\Z")
SESSION_ID = re.compile(r"^[A-Za-z0-9._:@+-]{1,128}\Z")
AGENT_ID = re.compile(r"^[A-Za-z0-9._@+-]{1,100}\Z")
AGENT_TYPE_UNSAFE = re.compile(r"[^A-Za-z0-9._-]")
MAX_MODELS = 32
MAX_TOOLS = 1000
MAX_TOOLS_PER_MODEL = 500

BASH_TOOL = "Bash"


def log(message):
    sys.stderr.write("slop-haus: usage: %s\n" % message)


# -- bash command extraction --
#
# A port of the pi extension's extractCommands; both are tested against
# go-llm-usage's fixtures/bash_commands.json, whose rules they implement.

# Programs whose first argument is reported as a subcommand, with the
# options that may come before it and take a value. go-llm-usage's
# SubcommandPrograms; the fixture repeats it and the tests keep them in step.
SUBCOMMAND_PROGRAMS = {
    "docker": ["--context", "-H", "--host"],
    "gh": ["-R", "--repo"],
    "git": ["-C", "-c"],
    "go": ["-C"],
    "helm": ["-n", "--namespace", "--kube-context"],
    "kubectl": ["-n", "--namespace", "--context", "--kubeconfig"],
    "npm": ["--prefix"],
    "uv": ["--directory", "--project"],
}
WORD = re.compile(r"^[a-z0-9._-]{1,32}$")
ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*\+?=")
# The word before an array assignment's `(`: NAME= or NAME+=.
ARRAY_ASSIGNMENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\+?=")
LEADING_TOKENS = {"(", "{", "!", "if", "then", "else", "elif", "while", "until", "do"}
CLOSING_TOKENS = {")", "}", "fi", "done", "esac"}
COMPOUND_STARTS = {"for", "case", "function"}
# Wrapper -> its options that take a separate value.
WRAPPERS = {
    "sudo": ["-u", "-g"],
    "env": ["-u"],
    "timeout": ["-s", "-k"],
    "nohup": [],
    "time": [],
    "exec": [],
    "command": [],
}


# What ended a simple command. Only a case clause's own terminator matters to
# what follows: it puts the case statement back to expecting a pattern.
END_LIST = "list"  # newline, `;`, `&`, `&&`, `||`, `|` or the end of the line
END_CLAUSE = "clause"  # `;;`, `;&` or `;;&`


def _skip_quoted(line, i, escapes):
    """The index of the quote that closes the one at i, or the last index when
    there is none. Backslash escapes count only in double quotes, unless
    escapes is set (ANSI-C $'...', backticks)."""
    quote = line[i]
    escapes = escapes or quote == '"'
    i += 1
    while i < len(line) and line[i] != quote:
        if escapes and line[i] == "\\":
            i += 1
        i += 1
    return min(i, len(line) - 1)


def _skip_balanced(line, i):
    """The index of the `)` that closes the `(` at i, or the last index when
    there is none. Quotes and backticks hide parentheses."""
    depth = 0
    while i < len(line):
        c = line[i]
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                return i
        elif c in "'\"":
            i = _skip_quoted(line, i, False)
        elif c == "`":
            i = _skip_quoted(line, i, True)
        elif c == "\\":
            i += 1
        i += 1
    return len(line) - 1


def _is_blank(c):
    return c in (" ", "\t", "\n")


def _skip_test(line, i):
    """The index of the last `]` of the `]]` word that closes the `[[` at i,
    or the last index when there is none."""
    i += 2
    while i < len(line):
        c = line[i]
        if c in "'\"":
            i = _skip_quoted(line, i, False)
        elif c == "`":
            i = _skip_quoted(line, i, True)
        elif c == "\\":
            i += 1
        elif (
            c == "]"
            and line.startswith("]]", i)
            and _is_blank(line[i - 1])
            and (i + 2 == len(line) or line[i + 2] in " \t\n;&|)")
        ):
            return i + 1
        i += 1
    return len(line) - 1


def _read_heredoc_delimiter(line, i):
    """The word after `<<` (or `<<-`) starting at i, unquoted, and the index
    of its last character."""
    while i < len(line) and line[i] in " \t":
        i += 1
    delimiter = ""
    while i < len(line) and line[i] not in " \t\n;|&<>()":
        c = line[i]
        if c in "'\"":
            close = line.find(c, i + 1)
            end = len(line) if close < 0 else close
            delimiter += line[i + 1 : end]
            i = end
        elif c == "\\":
            if i + 1 < len(line):
                i += 1
                delimiter += line[i]
        else:
            delimiter += c
        i += 1
    return delimiter, i - 1


def _skip_heredoc_bodies(line, i, heredocs):
    """The index of the newline that ends the last here-document body opened
    on the line ending at the newline at i. heredocs holds (delimiter,
    whether <<- strips leading tabs) pairs: a body's lines up to and
    including the delimiter's own."""
    for delimiter, strip_tabs in heredocs:
        while i < len(line):
            newline = line.find("\n", i + 1)
            end = len(line) if newline < 0 else newline
            body = line[i + 1 : end]
            i = end
            if (body.lstrip("\t") if strip_tabs else body) == delimiter:
                break
    return i


def _only_leading_tokens(words):
    """Whether a command so far is no more than keywords, so the next word is
    in command position."""
    return all(w in LEADING_TOKENS for w in words)


def _split_commands(line):
    """Tokenises a command line into simple commands: (words, end) pairs, end
    being END_LIST or END_CLAUSE. Quotes are stripped; a command substitution
    stays raw inside its word, so what it runs is never counted. Shell data
    -- comments, here-document bodies, arithmetic, [[ tests ]], array values
    -- is skipped whole: read as commands, it would be sent as command names,
    whatever it held."""
    commands = []
    state = {"words": [], "word": "", "in_word": False}

    def add(s):
        state["word"] += s
        state["in_word"] = True

    def end_word():
        if state["in_word"]:
            state["words"].append(state["word"])
        state["word"] = ""
        state["in_word"] = False

    def end_command(end):
        end_word()
        # An empty clause still ends its clause.
        if state["words"] or end == END_CLAUSE:
            commands.append((state["words"], end))
            state["words"] = []

    # Here-documents opened on the current line, whose bodies start on the next.
    heredocs = []
    n = len(line)
    i = 0
    while i < n:
        c = line[i]
        nxt = line[i + 1 : i + 2]
        if c in " \t":
            end_word()
        elif c == "\n":
            end_command(END_LIST)
            i = _skip_heredoc_bodies(line, i, heredocs)
            heredocs = []
        elif c == "#" and not state["in_word"]:
            newline = line.find("\n", i)
            i = n - 1 if newline < 0 else newline - 1
        elif c == "<" and nxt == "<" and line[i + 2 : i + 3] == "<":
            # A here-string's word is an argument, like any other.
            add("<<<")
            i += 2
        elif c == "<" and nxt == "<":
            end_word()
            strip_tabs = line[i + 2 : i + 3] == "-"
            delimiter, end = _read_heredoc_delimiter(line, i + (3 if strip_tabs else 2))
            heredocs.append((delimiter, strip_tabs))
            i = end
        elif c == ";":
            if nxt == ";":
                i += 1
                if line[i + 1 : i + 2] == "&":
                    i += 1
                end_command(END_CLAUSE)
            elif nxt == "&":
                i += 1
                end_command(END_CLAUSE)
            else:
                end_command(END_LIST)
        elif c == "|":
            if nxt == "|":
                i += 1
            end_command(END_LIST)
        elif c == "&":
            # 2>&1, >&file and &>file are redirections, not separators.
            if (i > 0 and line[i - 1] == ">") or nxt == ">":
                add("&")
            else:
                end_command(END_LIST)
        elif c == "(" and not state["in_word"] and nxt == "(":
            # Arithmetic, (( ... )) or for (( ... )), runs no program.
            i = _skip_balanced(line, i)
        elif c == "(" and state["in_word"] and ARRAY_ASSIGNMENT.fullmatch(state["word"]):
            end = _skip_balanced(line, i)
            add(line[i : end + 1])
            i = end
        elif c in "()":
            # Own word so "(cd x && y)" leaves "cd" and "y" clean.
            end_word()
            state["words"].append(c)
        elif (
            c == "["
            and not state["in_word"]
            and nxt == "["
            and (i + 2 == n or _is_blank(line[i + 2]))
            and _only_leading_tokens(state["words"])
        ):
            # A [[ test ]] runs no program; its && and || are not separators.
            i = _skip_test(line, i)
        elif c == "\\":
            if i + 1 < n:
                i += 1
                if line[i] != "\n":
                    add(line[i])
        elif c == "$" and nxt == "'":
            end = _skip_quoted(line, i + 1, True)
            add(line[i : end + 1])
            i = end
        elif c == "'":
            end = line.find("'", i + 1)
            if end < 0:
                end = n
            add(line[i + 1 : end])
            i = end
        elif c == '"':
            state["in_word"] = True
            i += 1
            while i < n and line[i] != '"':
                if line[i] == "\\" and i + 1 < n:
                    i += 1
                state["word"] += line[i]
                i += 1
        elif c == "`":
            end = i + 1
            while end < n and line[end] != "`":
                if line[end] == "\\":
                    end += 1
                end += 1
            end = min(end, n - 1)
            add(line[i : end + 1])
            i = end
        elif c == "$" and nxt == "(":
            end = _skip_balanced(line, i + 1)
            add(line[i : end + 1])
            i = end
        else:
            add(c)
        i += 1
    end_command(END_LIST)
    return commands


def _skip_wrapper(words):
    value_options = WRAPPERS[words[0]]
    is_timeout = words[0] == "timeout"
    rest = words[1:]
    while rest and rest[0].startswith("-"):
        rest = rest[2:] if rest[0] in value_options and len(rest) > 1 else rest[1:]
    return rest[1:] if is_timeout and rest else rest


def _command_of(words):
    while words:
        head = words[0]
        if head in WRAPPERS:
            words = _skip_wrapper(words)
        elif head in LEADING_TOKENS or ASSIGNMENT.match(head):
            words = words[1:]
        else:
            break
    if not words or words[0] in COMPOUND_STARTS or (len(words) == 1 and words[0] in CLOSING_TOKENS):
        return None
    program = words[0][words[0].rfind("/") + 1 :]
    if not WORD.match(program):
        return "other"
    value_options = SUBCOMMAND_PROGRAMS.get(program)
    if value_options is not None:
        i = 1
        while i < len(words):
            if words[i] in value_options:
                i += 1
            elif not words[i].startswith("-"):
                return "%s %s" % (program, words[i]) if WORD.match(words[i]) else program
            i += 1
    return program


# A case statement's parsing: its patterns are data, and only its clauses'
# bodies run programs.
CASE_IN = "in"  # after `case WORD`, before `in`
CASE_PATTERN = "pattern"  # expecting a pattern, up to its `)`, or esac
CASE_BODY = "body"  # in a clause's body, until ;; ;& ;;& or esac


def extract_commands(line):
    """The program, and for a few programs the subcommand, each simple
    command in a bash command line runs -- what a bash call is reported
    as."""
    out = []
    cases = []  # innermost last
    for words, end in _split_commands(line):
        while words:
            state = cases[-1] if cases else None
            if state == CASE_IN:
                if "in" in words:
                    cases[-1] = CASE_PATTERN
                    words = words[words.index("in") + 1 :]
                else:
                    words = []
                continue
            if state == CASE_PATTERN:
                if words[0] == "esac":
                    cases.pop()
                    words = words[1:]
                elif ")" in words:
                    cases[-1] = CASE_BODY
                    words = words[words.index(")") + 1 :]
                else:
                    # An alternative ending in `|`, as in `a|b)`, is a pattern too.
                    words = []
                continue
            first = next((k for k, w in enumerate(words) if w not in LEADING_TOKENS), -1)
            if first >= 0 and words[first] == "case":
                cases.append(CASE_IN)
                # The word after `case` is its subject, not `in`.
                words = words[min(first + 2, len(words)) :]
                continue
            if state is not None and words[0] == "esac":
                # The last clause needs no ;;.
                cases.pop()
                words = words[1:]
                continue
            command = _command_of(words)
            if command is not None:
                out.append(command)
            break
        if end == END_CLAUSE and cases and cases[-1] == CASE_BODY:
            cases[-1] = CASE_PATTERN
    return out


# -- transcript --


def parse_time(value):
    """Milliseconds since the epoch of an ISO 8601 timestamp, or None."""
    if not isinstance(value, str):
        return None
    try:
        # fromisoformat only takes a Z suffix from Python 3.11.
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return int(parsed.timestamp() * 1000)


def format_time(ms):
    return datetime.fromtimestamp(ms / 1000, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def count(value):
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else 0


def result_bytes(content):
    """The size of what a tool handed back to the model: its text, or for
    anything else (an image, say) its JSON."""
    if isinstance(content, str):
        return len(content.encode("utf-8"))
    if isinstance(content, list):
        total = 0
        for block in content:
            if isinstance(block, dict) and block.get("type") == "text" and isinstance(block.get("text"), str):
                total += len(block["text"].encode("utf-8"))
            else:
                total += len(json.dumps(block).encode("utf-8"))
        return total
    return 0


class Message:
    """One API request: its model, its usage, and when it was written."""

    FIELDS = ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "thinking_tokens")

    def __init__(self, model, at):
        self.model = model
        self.at = at
        self.usage = dict.fromkeys(self.FIELDS, 0)

    def merge(self, usage):
        """Takes the largest of each count: lines of one message repeat its
        usage, the later ones with more output as it streamed."""
        if not isinstance(usage, dict):
            return
        details = usage.get("output_tokens_details")
        seen = dict(usage, thinking_tokens=details.get("thinking_tokens") if isinstance(details, dict) else 0)
        for field in self.FIELDS:
            self.usage[field] = max(self.usage[field], count(seen.get(field)))


class ToolCall:
    def __init__(self, name, commands, model, at):
        self.name = name
        self.commands = commands
        self.model = model
        self.at = at
        self.is_error = False
        self.duration_ms = 0
        self.result_bytes = 0


class Transcript:
    """A transcript's messages and tool calls, read once, line by line."""

    def __init__(self):
        self.messages = {}
        self.tool_calls = {}
        # Model -> [first, last] timestamp of its messages and its tools'
        # results.
        self.spans = {}
        self.malformed = 0

    def _touch(self, model, at):
        if at is None:
            return
        span = self.spans.get(model)
        if span is None:
            self.spans[model] = [at, at]
        else:
            span[0] = min(span[0], at)
            span[1] = max(span[1], at)

    def add_line(self, raw):
        try:
            entry = json.loads(raw)
        except ValueError:
            self.malformed += 1
            return
        if not isinstance(entry, dict):
            self.malformed += 1
            return
        message = entry.get("message")
        if not isinstance(message, dict):
            return
        at = parse_time(entry.get("timestamp"))
        kind = entry.get("type")
        if kind == "assistant":
            self._add_assistant(message, at)
        elif kind == "user":
            self._add_results(message, at)

    def _add_assistant(self, message, at):
        model = message.get("model")
        message_id = message.get("id")
        # Claude Code writes its own notices as model "<synthetic>": no
        # request was made.
        if not isinstance(model, str) or not MODEL.match(model) or not isinstance(message_id, str):
            return
        known = self.messages.get(message_id)
        if known is None:
            known = self.messages[message_id] = Message(model, at)
        known.merge(message.get("usage"))
        self._touch(known.model, at)
        content = message.get("content")
        if not isinstance(content, list):
            return
        for block in content:
            if not isinstance(block, dict) or block.get("type") != "tool_use":
                continue
            tool_id, name = block.get("id"), block.get("name")
            if not isinstance(tool_id, str) or not isinstance(name, str) or tool_id in self.tool_calls:
                continue
            commands = []
            tool_input = block.get("input")
            if name == BASH_TOOL and isinstance(tool_input, dict) and isinstance(tool_input.get("command"), str):
                commands = extract_commands(tool_input["command"])
            self.tool_calls[tool_id] = ToolCall(name, commands, known.model, at)

    def _add_results(self, message, at):
        content = message.get("content")
        if not isinstance(content, list):
            return
        for block in content:
            if not isinstance(block, dict) or block.get("type") != "tool_result":
                continue
            call = self.tool_calls.get(block.get("tool_use_id"))
            if call is None:
                continue
            call.is_error = block.get("is_error") is True
            call.result_bytes = result_bytes(block.get("content"))
            if at is not None and call.at is not None:
                call.duration_ms = max(0, at - call.at)
            self._touch(call.model, at)

    def records(self):
        """The transcript's totals, one go-llm-usage Record per model."""
        totals = {}
        for message in self.messages.values():
            record = totals.get(message.model)
            if record is None:
                if len(totals) >= MAX_MODELS:
                    continue
                record = totals[message.model] = {
                    "requests": 0,
                    "usage": dict.fromkeys(Message.FIELDS, 0),
                    "tools": {},
                }
            record["requests"] += 1
            for field, value in message.usage.items():
                record["usage"][field] += value

        tool_count = 0
        for call in self.tool_calls.values():
            record = totals.get(call.model)
            if record is None:
                continue
            # A bash call counts once for each program it ran; its time,
            # output and outcome go to the last, whose exit status is the
            # call's, so the totals across commands are the call's own.
            names = call.commands or [""]
            for i, command in enumerate(names):
                tool = call.name if TOOL.match(call.name) else "other"
                key = (tool, command)
                if key not in record["tools"] and (tool_count >= MAX_TOOLS or len(record["tools"]) >= MAX_TOOLS_PER_MODEL):
                    key = ("other", "")
                entry = record["tools"].get(key)
                if entry is None:
                    entry = record["tools"][key] = {"calls": 0, "errors": 0, "duration_ms": 0, "result_bytes": 0}
                    tool_count += 1
                entry["calls"] += 1
                if i == len(names) - 1:
                    entry["errors"] += 1 if call.is_error else 0
                    entry["duration_ms"] += call.duration_ms
                    entry["result_bytes"] += call.result_bytes

        out = []
        for model, record in totals.items():
            span = self.spans.get(model)
            if span is None:
                continue
            usage = record["usage"]
            tools = []
            for (tool, command), entry in record["tools"].items():
                item = {"tool": tool}
                if command:
                    item["command"] = command
                item.update(entry)
                tools.append(item)
            out.append(
                {
                    "schema_version": 1,
                    "provider": PROVIDER,
                    "model": model,
                    "harness": HARNESS,
                    "billing_mode": BILLING_MODE,
                    "requests": record["requests"],
                    "input_tokens": usage["input_tokens"],
                    "output_tokens": usage["output_tokens"],
                    "cache_read_tokens": usage["cache_read_input_tokens"],
                    "cache_write_tokens": usage["cache_creation_input_tokens"],
                    # Part of output, and never more than it.
                    "reasoning_tokens": min(usage["thinking_tokens"], usage["output_tokens"]),
                    "started_at": format_time(span[0]),
                    "ended_at": format_time(span[1]),
                    "tools": tools,
                }
            )
        return out


def wait_for_settle(path, settle=SETTLE_SECONDS, max_wait=MAX_SETTLE_SECONDS, clock=time.monotonic, sleep=time.sleep):
    """Returns once the file at path has kept its size for settle seconds,
    or max_wait has passed."""
    if settle <= 0:
        return
    start = clock()
    last_size = None
    stable_since = start
    while True:
        try:
            size = os.stat(path).st_size
        except OSError:
            return
        now = clock()
        if size != last_size:
            last_size, stable_since = size, now
        elif now - stable_since >= settle:
            return
        if now - start >= max_wait:
            return
        sleep(SETTLE_POLL_SECONDS)


class BudgetExceeded(Exception):
    pass


def read_transcript(path, max_bytes=MAX_TRANSCRIPT_BYTES, max_line_bytes=MAX_LINE_BYTES, deadline=None):
    """Streams the transcript at path into a Transcript. Raises
    BudgetExceeded past max_bytes or the deadline, OSError if it can't be
    read."""
    transcript = Transcript()
    read = 0
    with open(path, "rb") as f:
        while True:
            line = f.readline(max_line_bytes + 1)
            if not line:
                break
            read += len(line)
            oversized = len(line) > max_line_bytes
            # The rest of an oversized line, a chunk at a time.
            while oversized and not line.endswith(b"\n"):
                line = f.readline(max_line_bytes)
                if not line:
                    break
                read += len(line)
                if read > max_bytes:
                    break
            if read > max_bytes:
                raise BudgetExceeded("transcript is over %d bytes" % max_bytes)
            if deadline is not None and time.monotonic() > deadline:
                raise BudgetExceeded("transcript took over %gs to read" % TIME_BUDGET_SECONDS)
            if oversized:
                transcript.malformed += 1
                continue
            if line.strip():
                transcript.add_line(line)
    return transcript


def build_report(payload, time_budget=None, settle=0.0, **limits):
    """The /hooks/usage body for a hook payload, or None with a reason."""
    event = payload.get("hook_event_name")
    session_id = payload.get("session_id")
    if not isinstance(session_id, str) or not SESSION_ID.match(session_id):
        return None, "no usable session_id"
    if event == "SubagentStop":
        path = payload.get("agent_transcript_path")
        agent_id = payload.get("agent_id")
        agent_type = payload.get("agent_type")
        if not isinstance(agent_id, str) or not AGENT_ID.match(agent_id):
            return None, "no usable agent_id"
        if not isinstance(agent_type, str) or not agent_type:
            agent_type = "unknown"
        key = "%s:%s" % (session_id, agent_id)
        scope = "subagent:" + AGENT_TYPE_UNSAFE.sub("_", agent_type)[:64]
    elif event == "Stop":
        path = payload.get("transcript_path")
        key = session_id
        scope = "main"
    else:
        return None, "not a Stop or SubagentStop payload (%r)" % (event,)
    if not isinstance(path, str) or not path:
        return None, "no transcript path"
    wait_for_settle(path, settle=settle)
    deadline = None if time_budget is None else time.monotonic() + time_budget
    try:
        transcript = read_transcript(path, deadline=deadline, **limits)
    except BudgetExceeded as err:
        return None, "skipping %s: %s" % (path, err)
    except OSError as err:
        return None, "cannot read %s: %s" % (path, err.strerror or err)
    records = transcript.records()
    if not records:
        # An empty report would wipe the stored totals.
        return None, "no model calls in %s" % path
    return {
        "transcript_key": key,
        "harness": HARNESS,
        "session_id": session_id,
        "scope": scope,
        "records": records,
    }, None


def settle_seconds(env):
    """SETTLE_SECONDS, or the SLOP_HAUS_USAGE_SETTLE_SECONDS a test sets."""
    try:
        return max(0.0, min(float(env.get("SLOP_HAUS_USAGE_SETTLE_SECONDS", SETTLE_SECONDS)), MAX_SETTLE_SECONDS))
    except ValueError:
        return SETTLE_SECONDS


def main():
    try:
        payload = json.loads(sys.stdin.buffer.read())
        if not isinstance(payload, dict):
            raise ValueError("not an object")
    except ValueError as err:
        log("unreadable hook payload: %s" % err)
        return
    try:
        report, reason = build_report(payload, time_budget=TIME_BUDGET_SECONDS, settle=settle_seconds(os.environ))
    except Exception as err:  # Reporting must never fail the hook.
        log("could not summarise the transcript: %r" % err)
        return
    if report is None:
        log(reason)
        return
    sys.stdout.write(json.dumps(report))


if __name__ == "__main__":
    try:
        main()
    except Exception as err:
        log("%r" % err)
    sys.exit(0)
