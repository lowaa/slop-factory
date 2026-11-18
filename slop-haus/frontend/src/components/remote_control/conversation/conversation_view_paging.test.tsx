import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type ConversationEvent } from "@/api";
import { makeConductor, makeConversation, makeConversationDetail, makeConversationEvent } from "@/test/fixtures";
import { recentEventsOf } from "@/test/recent_events";
import { renderSignedIn } from "@/test/render_signed_in";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { ProposalsContext } from "@/components/proposals";
import ConversationView from "./conversation_view";

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const CONVERSATION = "44444444-4444-4444-8444-444444444444";

// jsdom has no IntersectionObserver or layout: a test says when the top of
// the transcript comes into view, and what the scroller measures.
class FakeObserver {
  static all: FakeObserver[] = [];
  disconnected = false;
  private readonly callback: (entries: { isIntersecting: boolean }[]) => void;
  constructor(callback: (entries: { isIntersecting: boolean }[]) => void) {
    this.callback = callback;
    FakeObserver.all.push(this);
  }
  // Whether the top of the transcript is on screen, which a real observer
  // reports as soon as it starts watching.
  static topInView = false;
  observe() {
    if (FakeObserver.topInView) queueMicrotask(() => this.see());
  }
  unobserve() {}
  disconnect() {
    this.disconnected = true;
  }
  see() {
    this.callback([{ isIntersecting: true }]);
  }
  static live(): FakeObserver {
    const live = FakeObserver.all.filter((o) => !o.disconnected);
    if (live.length !== 1) throw new Error(`${live.length} live observers`);
    return live[0];
  }
}

let push: (event: ConversationEvent) => void;

// 25 turns of user / assistant / turn_end: the first read is turns 16 to 25.
function chat(turns: number): ConversationEvent[] {
  return Array.from({ length: turns }, (_, i) => [
    makeConversationEvent({ seq: i * 3 + 1, kind: "user", body: `question ${i + 1}`, turn: null }),
    makeConversationEvent({ seq: i * 3 + 2, kind: "assistant", body: `answer ${i + 1}`, turn: i + 1 }),
    makeConversationEvent({ seq: i * 3 + 3, kind: "turn_end", body: "", turn: i + 1 }),
  ]).flat();
}

// What the browser would report for the transcript scroller, installed before
// the view renders so its first measurements are these too.
const geometry = { scrollHeight: 0, clientHeight: 0, scrollTop: 0, extraHeight: () => 0 };
function installGeometry(initial: { scrollHeight: number; clientHeight: number; scrollTop: number }) {
  Object.assign(geometry, initial, { extraHeight: () => 0 });
  const proto = HTMLOListElement.prototype;
  Object.defineProperty(proto, "scrollHeight", { configurable: true, get: () => geometry.scrollHeight + geometry.extraHeight() });
  Object.defineProperty(proto, "clientHeight", { configurable: true, get: () => geometry.clientHeight });
  Object.defineProperty(proto, "scrollTop", { configurable: true, get: () => geometry.scrollTop, set: (v: number) => (geometry.scrollTop = v) });
  return geometry;
}

function scrolled(list: HTMLElement) {
  act(() => {
    list.dispatchEvent(new Event("scroll"));
  });
}

async function show(history: ConversationEvent[]) {
  vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
  const read = vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(history));
  await renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
      <UserSettingsProvider>
        <ConversationView conductor={conductor} conversationId={CONVERSATION} />
      </UserSettingsProvider>
    </ProposalsContext.Provider>,
  );
  const list = await screen.findByRole("list", { name: "Transcript" });
  // The top is watched from an effect after the first paint.
  if (history.filter((e) => e.kind === "user").length > 10 && typeof IntersectionObserver !== "undefined") {
    await waitFor(() => expect(FakeObserver.all.some((o) => !o.disconnected)).toBe(true));
  }
  return { list, read };
}

describe("ConversationView paging", () => {
  beforeEach(() => {
    FakeObserver.all = [];
    FakeObserver.topInView = false;
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([makeConversation({ id: CONVERSATION, state: "ready" })]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "streamConversation").mockImplementation((_c, _id, _after, onEvent) => {
      push = onEvent;
      return () => {};
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    for (const name of ["scrollHeight", "clientHeight", "scrollTop"]) Reflect.deleteProperty(HTMLOListElement.prototype, name);
  });

  it("shows the recent turns only, from the newest", async () => {
    const { list, read } = await show(chat(25));

    expect(read).toHaveBeenCalledWith(conductor.id, CONVERSATION, { before: 0, turns: 10 });
    expect(within(list).queryByText("question 15")).not.toBeInTheDocument();
    expect(within(list).getByText("question 16")).toBeInTheDocument();
    expect(within(list).getByText("answer 25")).toBeInTheDocument();
    expect(within(list).queryByText("Start of session")).not.toBeInTheDocument();
  });

  it("reads the next older turns when the top comes into view, once while the read is in flight", async () => {
    const { list, read } = await show(chat(25));
    const history = recentEventsOf(chat(25));
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    read.mockImplementation(async (c, id, range) => {
      await gate;
      return history(c, id, range);
    });

    const observer = FakeObserver.live();
    act(() => observer.see());
    act(() => observer.see());

    expect(await within(list).findByText("Loading earlier messages…")).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(2);
    expect(read).toHaveBeenLastCalledWith(conductor.id, CONVERSATION, { before: 46, turns: 10 });

    release();
    expect(await within(list).findByText("question 6")).toBeInTheDocument();
    expect(within(list).queryByText("Loading earlier messages…")).not.toBeInTheDocument();
    expect(within(list).queryByText("question 5")).not.toBeInTheDocument();
  });

  it("goes on to the next page while the top is still in view, until the session starts", async () => {
    const { list, read } = await show(chat(15));

    act(() => FakeObserver.live().see());
    await within(list).findByText("question 1");

    expect(within(list).getByText("Start of session")).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(2);
    expect(FakeObserver.all.filter((o) => !o.disconnected)).toHaveLength(0);
  });

  it("goes on reading pages while the top stays in view, which is how a tall screen fills, down to the start", async () => {
    const { list, read } = await show(chat(35));

    FakeObserver.topInView = true;
    act(() => FakeObserver.live().see());
    await within(list).findByText("Start of session");

    expect(within(list).getByText("question 1")).toBeInTheDocument();
    expect(read.mock.calls.map((c) => c[2])).toEqual([
      { before: 0, turns: 10 },
      { before: 76, turns: 10 },
      { before: 46, turns: 10 },
      { before: 16, turns: 10 },
    ]);
  });

  it("shows an error with a Retry that reads the same page again", async () => {
    const { list, read } = await show(chat(25));
    read.mockRejectedValueOnce(new ApiError(503, "down"));

    act(() => FakeObserver.live().see());
    expect(await within(list).findByRole("alert")).toHaveTextContent("down");
    expect(within(list).getByText("question 16")).toBeInTheDocument();

    await userEvent.click(within(list).getByRole("button", { name: "Retry" }));

    expect(await within(list).findByText("question 6")).toBeInTheDocument();
    expect(read).toHaveBeenLastCalledWith(conductor.id, CONVERSATION, { before: 46, turns: 10 });
    expect(within(list).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the content in view where it was when older turns are inserted above, spinner included", async () => {
    const m = installGeometry({ scrollHeight: 1000, clientHeight: 300, scrollTop: 0 });
    const { list, read } = await show(chat(25));
    const history = recentEventsOf(chat(25));
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    read.mockImplementation(async (c, id, range) => {
      await gate;
      return history(c, id, range);
    });
    // Showing the first turns follows them down; the person then scrolls up.
    m.scrollTop = 0;
    scrolled(list);
    // The spinner adds 28px above what is in view while it shows, and the
    // older turns 600px once they are in; the spinner then goes.
    m.extraHeight = () =>
      (within(list).queryByText("Loading earlier messages…") ? 28 : 0) + (within(list).queryByText("question 6") ? 600 : 0);

    act(() => FakeObserver.live().see());
    await within(list).findByText("Loading earlier messages…");
    release();
    await within(list).findByText("question 6");

    // What was at 0 (under the spinner) is now 600 - 28 further down.
    expect(m.scrollTop).toBe(572);
  });

  it("follows new output while at the bottom", async () => {
    const m = installGeometry({ scrollHeight: 1000, clientHeight: 300, scrollTop: 0 });
    const { list } = await show(chat(2));
    expect(m.scrollTop).toBe(1000);
    scrolled(list);

    act(() => {
      m.scrollHeight = 1100;
      push(makeConversationEvent({ seq: 7, kind: "assistant", body: "more", turn: 3 }));
    });

    expect(m.scrollTop).toBe(1100);
    expect(screen.queryByRole("button", { name: "Jump to latest" })).not.toBeInTheDocument();
  });

  it("stays put when scrolled up, offers Jump to latest, and following resumes after it", async () => {
    const m = installGeometry({ scrollHeight: 1000, clientHeight: 300, scrollTop: 0 });
    const { list } = await show(chat(2));
    m.scrollTop = 200;
    scrolled(list);

    act(() => {
      m.scrollHeight = 1100;
      push(makeConversationEvent({ seq: 7, kind: "assistant", body: "more", turn: 3 }));
    });

    expect(m.scrollTop).toBe(200);
    await userEvent.click(screen.getByRole("button", { name: "Jump to latest" }));
    expect(m.scrollTop).toBe(1100);
    expect(screen.queryByRole("button", { name: "Jump to latest" })).not.toBeInTheDocument();

    act(() => {
      m.scrollHeight = 1200;
      push(makeConversationEvent({ seq: 8, kind: "assistant", body: "again", turn: 3 }));
    });
    expect(m.scrollTop).toBe(1200);
  });

  it("goes to the end when you send a message from higher up", async () => {
    const m = installGeometry({ scrollHeight: 1000, clientHeight: 300, scrollTop: 0 });
    const { list } = await show(chat(2));
    vi.spyOn(apiClient, "sendConversationMessage").mockImplementation(async () => makeConversationEvent({ seq: 7, kind: "user", body: "hello" }));
    m.scrollTop = 100;
    scrolled(list);

    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "hello");
    m.scrollHeight = 1200;
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await within(list).findByText("hello");
    expect(m.scrollTop).toBe(1200);
    expect(screen.queryByRole("button", { name: "Jump to latest" })).not.toBeInTheDocument();
  });

  it("offers up-arrow history from the server's recent prompts, not only the loaded turns", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue({
      events: chat(1),
      hasMore: true,
      nextBefore: 1,
      prompts: ["asked long ago", "question 1"],
    });
    await renderSignedIn(
      <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
        <UserSettingsProvider>
          <ConversationView conductor={conductor} conversationId={CONVERSATION} />
        </UserSettingsProvider>
      </ProposalsContext.Provider>,
    );
    await screen.findByRole("list", { name: "Transcript" });

    act(() => push(makeConversationEvent({ seq: 4, kind: "user", body: "just now" })));
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "{ArrowUp}");

    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["asked long ago", "question 1", "just now"]);
  });

  it("does not read older turns when the browser has no IntersectionObserver", async () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    const { list, read } = await show(chat(25));

    expect(within(list).getByText("question 16")).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(1);
  });
});
