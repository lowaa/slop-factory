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

// V8 ("    at fn (file:1:2)", "    at file:1:2") and Firefox/Safari
// ("fn@file:1:2") frames. A line of any other shape is part of the message.
const FRAME_LINE = /^\s+at\s+\S.*:\d+:\d+\)?$|^\s+at\s+<anonymous>$|^[^\s:]*@\S+:\d+:\d+$/;

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
function withoutQueryOrFragment(line: string): string {
  return line.replace(/[?#][^\s)]*?(?=:\d+:\d+\)?$)/, "");
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
    .split("\n")
    .filter((line) => FRAME_LINE.test(line))
    .slice(0, MAX_FRAMES)
    .map(withoutQueryOrFragment)
    .map((line) => (line.length > MAX_FRAME_LENGTH ? `${line.slice(0, MAX_FRAME_LENGTH)}…` : line));
  return frames.length > 0 ? frames.join("\n") : undefined;
}
