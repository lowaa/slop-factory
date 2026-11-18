import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openEventStream, type StreamSession } from "./event_stream";

// jsdom has no EventSource; this one records what the wrapper does with it
// and lets a test play the server's part.
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  url: string;
  readyState = FakeEventSource.CONNECTING;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  send(data: string) {
    this.onmessage?.({ data } as MessageEvent<string>);
  }

  // refused is the server answering a reconnect with a 401: the browser
  // closes the stream for good and says so through onerror.
  refused() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }

  // dropped is a connection lost mid-stream, which the browser retries by
  // itself: still CONNECTING when onerror runs.
  dropped() {
    this.readyState = FakeEventSource.CONNECTING;
    this.onerror?.();
  }
}

function fakeSession(refreshed: boolean) {
  let settle: () => void = () => {};
  const done = new Promise<void>((resolve) => {
    settle = resolve;
  });
  const session = {
    generation: vi.fn(() => 3),
    refresh: vi.fn(async () => {
      settle();
      return refreshed;
    }),
    announceSignedOut: vi.fn(),
  } satisfies StreamSession;
  return { session, refreshed: done };
}

function latest(): FakeEventSource {
  return FakeEventSource.instances[FakeEventSource.instances.length - 1];
}

// A turn of the microtask queue, for the refresh's .then to have run.
const settled = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("openEventStream", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("hands each event's data on, parsed, and drops a malformed one", () => {
    const onData = vi.fn();
    openEventStream(fakeSession(true).session, { url: () => "/stream", onData });

    expect(latest().url).toBe("/stream");
    latest().send("not json");
    latest().send(JSON.stringify({ revision: 4 }));

    expect(onData).toHaveBeenCalledTimes(1);
    expect(onData).toHaveBeenCalledWith({ revision: 4 });
  });

  it("closes the stream when the returned function is called", () => {
    const close = openEventStream(fakeSession(true).session, { url: () => "/stream", onData: vi.fn() });

    close();

    expect(latest().closed).toBe(true);
  });

  // A refusal is what the first reconnect after the access token expires
  // gets: the session is refreshed and the stream reopened, at the URL as
  // it is now -- the caller's cursor has moved on since the first open.
  it("refreshes the session and reopens a stream the server refused", async () => {
    const { session, refreshed } = fakeSession(true);
    let cursor = 1;
    openEventStream(session, { url: () => `/stream?since=${cursor}`, onData: vi.fn() });
    cursor = 5;

    latest().refused();
    await refreshed;
    await settled();

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(latest().url).toBe("/stream?since=5");
    expect(session.announceSignedOut).not.toHaveBeenCalled();
  });

  it("announces the sign-out when the session can't be refreshed", async () => {
    const { session, refreshed } = fakeSession(false);
    openEventStream(session, { url: () => "/stream", onData: vi.fn() });

    latest().refused();
    await refreshed;
    await settled();

    expect(FakeEventSource.instances).toHaveLength(1);
    expect(session.announceSignedOut).toHaveBeenCalledWith(3);
  });

  // The browser retries a dropped connection itself; a refresh here would
  // be a wasted request, and a second stream beside the retried one.
  it("leaves a dropped connection to the browser's own retry", () => {
    const { session } = fakeSession(true);
    openEventStream(session, { url: () => "/stream", onData: vi.fn() });

    latest().dropped();

    expect(session.refresh).not.toHaveBeenCalled();
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("doesn't reopen a stream closed while its refresh was under way", async () => {
    const { session, refreshed } = fakeSession(true);
    const close = openEventStream(session, { url: () => "/stream", onData: vi.fn() });

    latest().refused();
    close();
    await refreshed;
    await settled();

    expect(FakeEventSource.instances).toHaveLength(1);
  });

  // A refusal a refresh doesn't cure -- the session has left the page's
  // tenant -- must not become a tight reopen loop.
  it("waits before reopening a stream that was refused again without delivering anything", async () => {
    vi.useFakeTimers();
    try {
      const { session } = fakeSession(true);
      openEventStream(session, { url: () => "/stream", onData: vi.fn(), retryDelayMs: 5000 });

      latest().refused();
      await vi.advanceTimersByTimeAsync(0);
      expect(FakeEventSource.instances).toHaveLength(2);

      latest().refused();
      await vi.advanceTimersByTimeAsync(4999);
      expect(FakeEventSource.instances).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(1);
      expect(FakeEventSource.instances).toHaveLength(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not reopen on a pending retry once closed", async () => {
    vi.useFakeTimers();
    try {
      const { session } = fakeSession(true);
      const close = openEventStream(session, { url: () => "/stream", onData: vi.fn(), retryDelayMs: 5000 });
      latest().refused();
      await vi.advanceTimersByTimeAsync(0);
      latest().refused();
      await vi.advanceTimersByTimeAsync(0);
      close();
      await vi.advanceTimersByTimeAsync(10000);
      expect(FakeEventSource.instances).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reopens at once after a stream that delivered something is refused", async () => {
    const { session } = fakeSession(true);
    openEventStream(session, { url: () => "/stream", onData: vi.fn() });
    latest().refused();
    await settled();
    latest().send(JSON.stringify({ revision: 1 }));
    latest().refused();
    await settled();
    expect(FakeEventSource.instances).toHaveLength(3);
  });
});
