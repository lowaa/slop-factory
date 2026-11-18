// What of an error may leave the browser. An error's message and stack are
// free text: a thrown `new Error(\`bad value ${v}\`)`, a JSON.parse
// SyntaxError quoting the input, or a server `detail` echoing a form field
// can all carry a health value or a name. The operational tier must never
// carry those (go-privacy/README.md §1), and no regex can tell a value from
// prose, so nothing is exported on trust:
//  - the message is exported only if it is one of the engine's own known
//    shapes, and then as a fixed string with the variable part dropped;
//    any other message is withheld, leaving the error type and the stack;
//  - the stack is exported as its frames only (function and file:line:col,
//    which the source map decodes), never its header, which holds the message.

const MAX_FRAMES = 30;
const MAX_FRAME_LENGTH = 200;

export const MESSAGE_WITHHELD = "message withheld";

// The engine's messages, each mapped to a fixed string. A pattern's captures
// are never echoed, except where it captures a closed set (undefined|null).
const KNOWN_MESSAGES: ReadonlyArray<{ pattern: RegExp; text: (match: RegExpMatchArray) => string }> = [
  { pattern: /^Cannot read properties of (undefined|null) \(reading '[^]*'\)$/, text: (m) => `Cannot read properties of ${m[1]}` },
  { pattern: /^Cannot set properties of (undefined|null) \(setting '[^]*'\)$/, text: (m) => `Cannot set properties of ${m[1]}` },
  { pattern: /^[^]* is not a function$/, text: () => "is not a function" },
  { pattern: /^[^]* is not iterable$/, text: () => "is not iterable" },
  { pattern: /^[^]* is not defined$/, text: () => "reference to an undefined variable" },
  { pattern: /^Failed to fetch$/, text: () => "Failed to fetch" },
  { pattern: /^Load failed$/, text: () => "Load failed" },
  { pattern: /^NetworkError when attempting to fetch resource\.?$/, text: () => "NetworkError when attempting to fetch resource" },
  { pattern: /^Failed to fetch dynamically imported module/, text: () => "Failed to fetch dynamically imported module" },
  { pattern: /^Importing a module script failed/, text: () => "Importing a module script failed" },
  { pattern: /^Maximum call stack size exceeded$/, text: () => "Maximum call stack size exceeded" },
  { pattern: /^Script error\.?$/, text: () => "Script error" },
];

// Browser noise that is not a fault of the app, and would only flood the log.
const BENIGN_MESSAGES: readonly RegExp[] = [/^ResizeObserver loop/];

const ERROR_TYPE = /^[A-Za-z][A-Za-z0-9_.]{0,63}$/;

const LINE_TERMINATOR = /[\r\u2028\u2029]/;
const NON_URL_CHAR = /[\s)]/;

// A line is text an attacker can shape, and a stack can run to megabytes, so
// what is matched is bounded first: a longer line is not a frame, and only the
// first lines are looked at.
const MAX_INPUT_LINE_LENGTH = 2000;
const MAX_INPUT_LINES = 500;

// The index of the ":line:column" that ends `text` (before an optional
// closing paren when `allowParen`), or -1. Scanned from the end in one pass:
// the colons are delimiters, so no step has an alternative to retry. The
// frame patterns this replaces had `.*` or `\S+` ahead of the same suffix,
// which backtrack polynomially on a long line of colons and digits.
function locationStart(text: string, allowParen: boolean): number {
  let i = allowParen && text.endsWith(")") ? text.length - 1 : text.length;
  for (let part = 0; part < 2; part++) {
    const end = i;
    while (i > 0 && text.charCodeAt(i - 1) >= 48 && text.charCodeAt(i - 1) <= 57) i--;
    if (i === end || i === 0 || text[i - 1] !== ":") return -1;
    i--;
  }
  return i;
}

// V8 ("    at fn (file:1:2)", "    at file:1:2", "    at <anonymous>"):
// whitespace, "at", whitespace, then a non-space character.
function isV8Frame(line: string): boolean {
  const lead = /^\s+at\s+/.exec(line);
  if (!lead) return false;
  const rest = line.slice(lead[0].length);
  if (rest === "<anonymous>") return true;
  // `.` in the pattern this mirrors does not span these.
  if (LINE_TERMINATOR.test(rest)) return false;
  return locationStart(rest, true) >= 1;
}

// Firefox and Safari ("fn@file:1:2", "@file:1:2"): no whitespace anywhere,
// no colon before the first "@", and something between it and the location.
function isGeckoFrame(line: string): boolean {
  const at = line.indexOf("@");
  if (at < 0 || /\s/.test(line) || line.lastIndexOf(":", at) >= 0) return false;
  return locationStart(line.slice(at + 1), false) >= 1;
}

// Linear in the line whatever its shape; safeStack also bounds the line first.
export function isFrameLine(line: string): boolean {
  return isV8Frame(line) || isGeckoFrame(line);
}

export function errorType(err: unknown): string {
  if (err instanceof Error) return ERROR_TYPE.test(err.name) ? err.name : "Error";
  // The kind of thing thrown says nothing of its content.
  return `NonError:${err === null ? "null" : typeof err}`;
}

export function isBenignError(err: unknown): boolean {
  return err instanceof Error && BENIGN_MESSAGES.some((p) => p.test(err.message));
}

export function safeErrorMessage(err: unknown): string {
  if (!(err instanceof Error)) return MESSAGE_WITHHELD;
  for (const { pattern, text } of KNOWN_MESSAGES) {
    const match = err.message.match(pattern);
    if (match) return text(match);
  }
  return MESSAGE_WITHHELD;
}

// A frame's file is a URL, and its query or fragment can hold anything the
// page was loaded with. Keeps the path and the line:column that follow it.
// The URL runs back from the location to the nearest whitespace or ")", and
// is cut from its first "?" or "#".
export function withoutQueryOrFragment(line: string): string {
  const end = locationStart(line, true);
  if (end < 0) return line;
  let start = -1;
  for (let i = end - 1; i >= 0 && !NON_URL_CHAR.test(line[i]); i--) {
    if (line[i] === "?" || line[i] === "#") start = i;
  }
  return start < 0 ? line : line.slice(0, start) + line.slice(end);
}

// The frames of an error's stack. V8 puts "Name: message" first, and a
// message can itself hold lines that look like frames, so the message is cut
// away by its text, not told apart by shape. Firefox and Safari have no
// header, and their stacks never contain the message.
export function safeErrorStack(err: Error): string | undefined {
  const { stack, message } = err;
  if (!stack) return undefined;
  const at = message === "" ? -1 : stack.indexOf(message);
  return safeStack(at < 0 ? stack : stack.slice(at + message.length));
}

// The frames of a stack with no header, such as React's component stack, or
// of one whose header is already gone: only lines shaped like a frame.
export function safeStack(stack: string | undefined | null): string | undefined {
  if (!stack) return undefined;
  const frames = stack
    .split("\n", MAX_INPUT_LINES)
    .filter((line) => line.length <= MAX_INPUT_LINE_LENGTH && isFrameLine(line))
    .slice(0, MAX_FRAMES)
    .map(withoutQueryOrFragment)
    .map((line) => (line.length > MAX_FRAME_LENGTH ? `${line.slice(0, MAX_FRAME_LENGTH)}…` : line));
  return frames.length > 0 ? frames.join("\n") : undefined;
}
