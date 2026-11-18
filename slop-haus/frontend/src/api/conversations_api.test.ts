// The conversations half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installFetch } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("streamBoardChanges", () => {
  class FakeEventSource {
    static instances: FakeEventSource[] = [];
    static readonly CLOSED = 2;
    url: string;
    readyState = 0;
    onmessage: ((e: MessageEvent<string>) => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(url: string) {
      this.url = url;
      FakeEventSource.instances.push(this);
    }
    close() {}
  }

  let apiClient: ApiClient;

  beforeEach(async () => {
    ({
      api: { apiClient },
    } = await installFetch());
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("connects to /api/tickets/stream and reports each revision", () => {
    const onChange = vi.fn();
    const close = apiClient.streamBoardChanges(onChange);

    expect(FakeEventSource.instances[0].url).toBe("/api/tickets/stream");
    FakeEventSource.instances[0].onmessage?.({ data: JSON.stringify({ revision: 12 }) } as MessageEvent<string>);

    expect(onChange).toHaveBeenCalledWith(12, { revision: 12 });
    close();
  });

  it("passes on the tickets the event names", () => {
    const onChange = vi.fn();
    const close = apiClient.streamBoardChanges(onChange);
    const data = { revision: 13, tickets: [{ id: 5, title: "Moved" }] };

    FakeEventSource.instances[0].onmessage?.({ data: JSON.stringify(data) } as MessageEvent<string>);

    expect(onChange).toHaveBeenCalledWith(13, data);
    close();
  });

  it("resumes from the caller's revision", () => {
    const close = apiClient.streamBoardChanges(() => {}, 12);
    expect(FakeEventSource.instances[0].url).toBe("/api/tickets/stream?since=12");
    close();
  });

  // Reopened after the session is refreshed, it resumes from the last
  // revision it delivered, not the one it was opened with: otherwise every
  // reopen would report a change already refetched for.
  it("reopens a refused stream from the last revision delivered", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } })),
    );
    const close = apiClient.streamBoardChanges(() => {}, 3);
    const first = FakeEventSource.instances[0];
    first.onmessage?.({ data: JSON.stringify({ revision: 12 }) } as MessageEvent<string>);

    first.readyState = FakeEventSource.CLOSED;
    first.onerror?.();

    await vi.waitFor(() => expect(FakeEventSource.instances).toHaveLength(2));
    expect(FakeEventSource.instances[1].url).toBe("/api/tickets/stream?since=12");
    close();
  });

  it("ignores an event without a revision", () => {
    const onChange = vi.fn();
    const close = apiClient.streamBoardChanges(onChange);

    FakeEventSource.instances[0].onmessage?.({ data: "null" } as MessageEvent<string>);
    FakeEventSource.instances[0].onmessage?.({ data: "{}" } as MessageEvent<string>);

    expect(onChange).not.toHaveBeenCalled();
    close();
  });
});
