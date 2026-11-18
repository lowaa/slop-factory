export type Attrs = Record<string, unknown>;

// What every part of a consuming app logs through. No fluent interface:
// each call is a single, complete statement.
//
// The tier is part of the method, so it is visible at every call site:
// error() is operational (always exported), event() is analytics
// (exported only with consent). debug/info/warn are console-only -- the
// operational tier is errors only, with no breadcrumb stream.
export interface Logger {
  debug(message: string, attrs?: Attrs): void;
  info(message: string, attrs?: Attrs): void;
  warn(message: string, attrs?: Attrs): void;
  error(message: string, attrs?: Attrs): void;
  event(name: string, attrs?: Attrs): void;
  // Interaction recording: its own tier and consent purpose, more intrusive
  // than event(). A logger that has no such tier doesn't implement it.
  interaction?(name: string, attrs?: Attrs): void;
  // Sends whatever is waiting to be exported, for a page that is about to go
  // away. A logger that holds nothing back has nothing to do.
  flush?(): Promise<void>;
}
