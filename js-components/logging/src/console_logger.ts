import type { Attrs, Logger } from "./logger";

type ConsoleMethod = "debug" | "info" | "warn" | "error";

// Logs to the browser console only -- OtelLogger delegates every call to an
// instance of this too (see otel_logger.ts), so devtools always show the
// same output regardless of whether OTel export is actually reachable.
export class ConsoleLogger implements Logger {
  debug(message: string, attrs?: Attrs): void {
    this.log("debug", message, attrs);
  }

  info(message: string, attrs?: Attrs): void {
    this.log("info", message, attrs);
  }

  warn(message: string, attrs?: Attrs): void {
    this.log("warn", message, attrs);
  }

  error(message: string, attrs?: Attrs): void {
    this.log("error", message, attrs);
  }

  event(name: string, attrs?: Attrs): void {
    this.log("info", `event: ${name}`, attrs);
  }

  // Not logged to the console: it is sampled several times a second, and
  // would drown everything else in devtools.
  interaction(): void {}

  private log(level: ConsoleMethod, message: string, attrs?: Attrs): void {
    if (attrs) console[level](message, attrs);
    else console[level](message);
  }
}
