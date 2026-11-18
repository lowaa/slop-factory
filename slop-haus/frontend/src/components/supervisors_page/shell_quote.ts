const SAFE_WORD = /^[A-Za-z0-9._\-/]+$/;

// shellQuote makes one POSIX shell word of value, leaving it bare when it
// needs no quoting so that typical commands stay readable. Otherwise it
// quotes as the server's workspace.ShellQuote does.
export function shellQuote(value: string): string {
  if (SAFE_WORD.test(value)) {
    return value;
  }
  return `'${value.replaceAll("'", `'\\''`)}'`;
}
