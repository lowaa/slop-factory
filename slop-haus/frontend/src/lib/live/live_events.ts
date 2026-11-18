import { apiClient, type BoardChange } from "@/api";

// What a subscriber is told. "change" is a server event; "resync" says events
// may have been missed (the tab came back, or the page was restored from the
// back/forward cache), so refetch; "pause" says the stream was closed because
// the page went away, so drop any refetch still waiting.
export type LiveEvent = { kind: "change"; revision: number; change: BoardChange } | { kind: "resync" } | { kind: "pause" };

// "board" gets every event. A ticket's topic gets the changes that name that
// ticket, or that cannot say which tickets changed.
export type LiveTopic = "board" | `ticket:${number}`;

export type LiveListener = (event: LiveEvent) => void;

export const ticketTopic = (id: number): LiveTopic => `ticket:${id}`;

type OpenStream = (onChange: (revision: number, change: BoardChange) => void, since?: number) => () => void;

function concerns(topic: LiveTopic, event: LiveEvent): boolean {
  if (topic === "board" || event.kind !== "change") return true;
  const { tickets, reload } = event.change;
  if (reload || !tickets) return true;
  return tickets.some((t) => `ticket:${t.id}` === topic);
}

// LiveEvents is the app's one connection to the server's board stream. Every
// view that follows live changes subscribes here, so a page costs one of the
// browser's few connections per origin however many views are mounted.
//
// The stream is open only while someone is subscribed and the page is
// visible. A hidden tab costs the server nothing, and closing it on pagehide
// keeps the page eligible for the back/forward cache, which an open
// connection can stop. Reopened, it resumes from the last revision seen, so
// it sends a change only if there has been one. Reconnecting after a drop or
// an expired session, with a delay when refused repeatedly, is
// openEventStream's.
export class LiveEvents {
  private readonly listeners = new Map<LiveListener, LiveTopic>();
  private closeStream: (() => void) | undefined;
  private revision: number | undefined;
  private attached = false;

  private readonly openStream: OpenStream;

  constructor(openStream: OpenStream) {
    this.openStream = openStream;
  }

  // subscribe returns the function that ends the subscription; call it from
  // an effect's cleanup.
  subscribe(topic: LiveTopic, listener: LiveListener): () => void {
    this.listeners.set(listener, topic);
    this.attach();
    this.open();
    // A stream that is already open delivers its first event (the revision as
    // it stands) only to whoever was there. A late subscriber gets a resync
    // in its place, as its own first fetch races the stream's first read.
    if (this.revision !== undefined) queueMicrotask(() => this.listeners.has(listener) && listener({ kind: "resync" }));
    return () => {
      this.listeners.delete(listener);
      if (this.listeners.size === 0) this.detach();
    };
  }

  private open() {
    if (this.closeStream || document.visibilityState !== "visible" || this.listeners.size === 0) return;
    this.closeStream = this.openStream((revision, change) => {
      this.revision = revision;
      this.emit({ kind: "change", revision, change });
    }, this.revision);
  }

  private close() {
    this.closeStream?.();
    this.closeStream = undefined;
  }

  private emit(event: LiveEvent) {
    for (const [listener, topic] of [...this.listeners]) {
      if (this.listeners.has(listener) && concerns(topic, event)) listener(event);
    }
  }

  private leave = () => {
    this.close();
    this.emit({ kind: "pause" });
  };

  private onVisibilityChange = () => {
    if (document.visibilityState === "visible") {
      this.emit({ kind: "resync" });
      this.open();
    } else this.leave();
  };

  // pageshow also fires on an ordinary load, which the views' own fetches
  // cover; only a restore from the cache needs a resync.
  private onPageShow = (event: PageTransitionEvent) => {
    if (!event.persisted) return;
    this.emit({ kind: "resync" });
    this.open();
  };

  private attach() {
    if (this.attached) return;
    this.attached = true;
    document.addEventListener("visibilitychange", this.onVisibilityChange);
    window.addEventListener("pageshow", this.onPageShow);
    window.addEventListener("pagehide", this.leave);
  }

  private detach() {
    this.attached = false;
    document.removeEventListener("visibilitychange", this.onVisibilityChange);
    window.removeEventListener("pageshow", this.onPageShow);
    window.removeEventListener("pagehide", this.leave);
    this.close();
    this.revision = undefined;
  }
}

// Looked up at call time so a test can replace apiClient's method.
export const liveEvents = new LiveEvents((onChange, since) => apiClient.streamBoardChanges(onChange, since));
