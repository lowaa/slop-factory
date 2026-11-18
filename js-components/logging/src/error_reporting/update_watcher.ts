import { BuildTag, fetchBuildTag, UNKNOWN_BUILD_TAG } from "./build_tag";

export interface UpdateWatcherOptions {
  buildTag: BuildTag;
  intervalMs?: number;
  doc?: Document;
  fetchTag?: () => Promise<string>;
}

const DEFAULT_INTERVAL_MS = 5 * 60_000;

// Notices that the server now serves a different build than this tab is
// running. It only reports: nothing here reloads, as someone may be typing.
// Polls while the tab is visible, and checks at once when it becomes visible
// again (a phone left open for hours).
export class UpdateWatcher {
  private readonly o: Required<UpdateWatcherOptions>;
  private readonly listeners = new Set<() => void>();
  private readonly dismissed = new Set<string>();
  private available: string | undefined;
  private timer: ReturnType<typeof setInterval> | undefined;
  private checking = false;

  constructor(options: UpdateWatcherOptions) {
    this.o = { intervalMs: DEFAULT_INTERVAL_MS, doc: document, fetchTag: fetchBuildTag, ...options };
  }

  // The newer tag to tell the user about, if any and not dismissed.
  getSnapshot = (): string | undefined => {
    return this.available !== undefined && !this.dismissed.has(this.available) ? this.available : undefined;
  };

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => void this.listeners.delete(listener);
  };

  dismiss(): void {
    if (this.available !== undefined) this.dismissed.add(this.available);
    this.notify();
  }

  // Returns the stop function.
  start(): () => void {
    const onVisibility = () => this.sync();
    this.o.doc.addEventListener("visibilitychange", onVisibility);
    this.sync(false);
    return () => {
      this.o.doc.removeEventListener("visibilitychange", onVisibility);
      this.stopTimer();
    };
  }

  private sync(checkNow = true): void {
    if (this.o.doc.visibilityState === "visible") {
      this.startTimer();
      if (checkNow) void this.check();
    } else {
      this.stopTimer();
    }
  }

  private startTimer(): void {
    this.timer ??= setInterval(() => void this.check(), this.o.intervalMs);
  }

  private stopTimer(): void {
    if (this.timer !== undefined) clearInterval(this.timer);
    this.timer = undefined;
  }

  async check(): Promise<void> {
    if (this.checking) return;
    this.checking = true;
    try {
      const [running, latest] = await Promise.all([this.o.buildTag.get(), this.o.fetchTag()]);
      // Either unknown (a failed request, a server without the file): can't
      // say it changed, so say nothing.
      if (running === UNKNOWN_BUILD_TAG || latest === UNKNOWN_BUILD_TAG || latest === running) return;
      if (latest === this.available) return;
      this.available = latest;
      this.o.buildTag.noteServerTag(latest);
      this.notify();
    } finally {
      this.checking = false;
    }
  }

  private notify(): void {
    for (const l of this.listeners) l();
  }
}
