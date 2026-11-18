import { CATALOG, type EventAttrs, type Screen, type UsageEventName } from "./catalog";

// The events the screens send. screen_time is the tracker's own.
export type CallSiteEvent = Exclude<UsageEventName, "screen_view" | "screen_time">;

// What a call site gives: the catalog's attributes except the screen, which is
// filled in from where the person is, so it cannot be got wrong.
export type CallSiteAttrs<E extends CallSiteEvent> = Omit<EventAttrs<E>, "screen">;

export interface UsageEvent {
  name: UsageEventName;
  attrs: Record<string, string | number | boolean>;
}

// Where a batch goes. Fire and forget: usage is dropped, never retried.
export type UsageSender = (events: UsageEvent[]) => void;

// The most events held between flushes, and the most in one request (the
// server refuses more). Past the cap new events are dropped until the next
// flush: usage is a sample of how the board is used, not a log.
export const QUEUE_CAP = 200;
export const BATCH_SIZE = 50;

// Batches events in memory and sends them when the page is hidden. Nothing is written to the device, so a tab that crashes loses
// what had not been sent. The one place that decides what an event looks
// like when it is sent.
export class Usage {
  private readonly send: UsageSender;
  private readonly online: () => boolean;
  private screen: Screen | null = null;
  private queue: UsageEvent[] = [];

  constructor(send: UsageSender, online: () => boolean = () => navigator.onLine) {
    this.send = send;
    this.online = online;
  }

  get currentScreen(): Screen | null {
    return this.screen;
  }

  setScreen(screen: Screen | null): void {
    this.screen = screen;
  }

  event<E extends CallSiteEvent>(name: E, attrs: CallSiteAttrs<E>): void {
    // An event off any screen is one the catalog has no way to place.
    if (this.screen === null) return;
    this.enqueue(name, { ...attrs, screen: this.screen });
  }

  // For the tracker, whose events carry their own screen.
  track<E extends UsageEventName>(name: E, attrs: EventAttrs<E>): void {
    this.enqueue(name, attrs);
  }

  private enqueue(name: UsageEventName, attrs: object): void {
    if (!(name in CATALOG) || this.queue.length >= QUEUE_CAP) return;
    this.queue.push({ name, attrs: { ...attrs } });
  }

  // Sends what is waiting. Offline, the events are dropped rather than kept
  // for later: keeping them would mean holding a record of use past the visit.
  // Never throws: this is called while the page is closing.
  flush(): void {
    const events = this.queue;
    this.queue = [];
    if (events.length === 0 || !this.online()) return;
    for (let i = 0; i < events.length; i += BATCH_SIZE) {
      try {
        this.send(events.slice(i, i + BATCH_SIZE));
      } catch {
        // Usage is never worth a visible failure.
      }
    }
  }
}
