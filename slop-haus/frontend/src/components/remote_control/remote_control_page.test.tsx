import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED } from "@/api";
import { makeConductor, makeConductorDirectoryEntry, makeTenant, makeConversation, makeConversationDetail, makeConversationEvent, makeHarnessConfig } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { stubNarrowLayout } from "@/test/layout";
import { stubLocalStorage } from "@/test/storage";
import { ProposalsContext } from "@/components/proposals";
import RemoteControlPage, { type RemoteControlSelection } from "./remote_control_page";
import { NO_EVENTS } from "@/test/recent_events";

const laptop = makeConductor({ id: "c-laptop", name: "my-laptop" });
const desktop = makeConductor({ id: "c-desktop", name: "my-desktop" });
const OLD = "55555555-5555-4555-8555-555555555555";
const NEW = "66666666-6666-4666-8666-666666666666";

const SESSIONS: Record<string, ReturnType<typeof makeConversation>[]> = {
  "c-laptop": [
    makeConversation({ id: NEW, conductor_id: "c-laptop", state: "ready", updated_at: "2026-01-02T00:00:00Z" }),
    makeConversation({ id: OLD, conductor_id: "c-laptop", state: "stopped", updated_at: "2026-01-01T00:00:00Z" }),
  ],
  "c-desktop": [],
};

function serve() {
  vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [laptop, desktop], others: [] });
  vi.spyOn(apiClient, "getConductorHost").mockImplementation(async (id) => ({ online: id === "c-laptop" }));
  vi.spyOn(apiClient, "listConversations").mockImplementation(async (id) => SESSIONS[id] ?? []);
  vi.spyOn(apiClient, "conductorDirectory").mockResolvedValue([makeConductorDirectoryEntry({ id: "c-laptop", name: "my-laptop" })]);
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant()]);
  vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
  vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
  vi.spyOn(apiClient, "getConversation").mockImplementation(async (_c, id) => makeConversationDetail({ id }));
  vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
  vi.spyOn(apiClient, "streamConversation").mockReturnValue(() => {});
  // Already explained, so a switch asks nothing these tests are not about.
  vi.spyOn(apiClient, "listMySettings").mockResolvedValue([
    { name: REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type: "bool", bool_val: true, is_default: false } as never,
  ]);
}

// Stands in for the router: the address is the state, and a selection the
// page makes replaces it. replaced records those that did not push.
function AddressedPage({ start, replaced, selections }: { start: RemoteControlSelection; replaced: RemoteControlSelection[]; selections: RemoteControlSelection[] }) {
  const [address, setAddress] = useState<RemoteControlSelection>(start);
  return (
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
      <RemoteControlPage
        {...address}
        onSelect={(next) => {
          if (next.replace) replaced.push(next);
          selections.push(next);
          setAddress(next);
        }}
      />
    </ProposalsContext.Provider>
  );
}

function addressedPage(start: RemoteControlSelection = {}) {
  const replaced: RemoteControlSelection[] = [];
  const selections: RemoteControlSelection[] = [];
  return { replaced, selections, rendered: renderSignedIn(<AddressedPage start={start} replaced={replaced} selections={selections} />) };
}

function page(props: Partial<React.ComponentProps<typeof RemoteControlPage>> = {}) {
  return renderSignedIn(
    <ProposalsContext.Provider value={{ status: "loaded", error: null, proposals: [], reload: async () => {} }}>
      <RemoteControlPage onSelect={vi.fn()} {...props} />
    </ProposalsContext.Provider>,
  );
}

describe("RemoteControlPage", () => {
  let storage: Storage;
  beforeEach(() => {
    storage = stubLocalStorage();
    serve();
  });

  it("lists the sessions grouped by machine, each machine marked online or offline", async () => {
    await page();

    const laptopGroup = await screen.findByRole("region", { name: "my-laptop" });
    expect(within(laptopGroup).getByText("Online")).toBeInTheDocument();
    expect(within(laptopGroup).getByRole("button", { name: /Connected/ })).toBeInTheDocument();
    expect(within(laptopGroup).getByRole("button", { name: /Disconnected/ })).toBeInTheDocument();
    const desktopGroup = screen.getByRole("region", { name: "my-desktop" });
    expect(within(desktopGroup).getByText("Offline")).toBeInTheDocument();
    // + New session, and the ⋯ menu.
    expect(within(desktopGroup).getAllByRole("button")).toHaveLength(2);
  });

  it("hides a removed remote control's sessions and lists it under Show removed", async () => {
    const OFF = "2026-01-02T00:00:00Z";
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({
      own: [laptop, makeConductor({ id: "c-desktop", name: "my-desktop", disabled_at: OFF, archived_at: OFF })],
      others: [],
    });
    await page();

    await screen.findByRole("region", { name: "my-laptop" });
    expect(apiClient.listConductors).toHaveBeenCalledWith({ includeArchived: true });
    expect(screen.queryByRole("region", { name: "my-desktop" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Show removed (1)" }));
    expect(screen.getByText("my-desktop")).toBeInTheDocument();
  });

  it("lists a disabled remote control to enable again, but starts and opens nothing on it", async () => {
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({
      own: [laptop, makeConductor({ id: "c-desktop", name: "my-desktop", disabled_at: "2026-01-02T00:00:00Z" })],
      others: [],
    });
    const listed = vi.spyOn(apiClient, "listConversations");
    await page();

    const group = await screen.findByRole("region", { name: "my-desktop" });
    expect(within(group).getByText("Disabled")).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: "+ New session" })).not.toBeInTheDocument();
    expect(listed).not.toHaveBeenCalledWith("c-desktop");
  });

  it("leaves a deleted session for a new one and reads the list again", async () => {
    vi.spyOn(apiClient, "deleteConversation").mockResolvedValue();
    const onSelect = vi.fn();
    await page({ onSelect, conductorId: "c-laptop", sessionId: OLD });
    const list = vi.mocked(apiClient.listConversations);

    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    list.mockClear();
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(onSelect).toHaveBeenLastCalledWith({ conductorId: "c-laptop", isNew: true }));
    await waitFor(() => expect(list).toHaveBeenCalled());
  });

  it("reads the person's settings once for the whole page", async () => {
    await page({ conductorId: "c-laptop", sessionId: OLD });

    await screen.findByRole("textbox", { name: "Message" });
    expect(apiClient.listMySettings).toHaveBeenCalledTimes(1);
  });

  it("shows neither Online nor Offline for a machine whose host could not be read", async () => {
    vi.spyOn(apiClient, "getConductorHost").mockImplementation(async (id) => {
      if (id === "c-desktop") throw new Error("down");
      return { online: true };
    });
    await page();

    const desktopGroup = await screen.findByRole("region", { name: "my-desktop" });
    expect(within(desktopGroup).queryByText("Online")).not.toBeInTheDocument();
    expect(within(desktopGroup).queryByText("Offline")).not.toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "my-laptop" })).getByText("Online")).toBeInTheDocument();
  });

  it("shows the host's version from the host route, with the update hint when the server ships another", async () => {
    vi.spyOn(apiClient, "getConductorHost").mockImplementation(async (id) =>
      id === "c-laptop" ? { online: true, version: "aaaa11112222", current_version: "bbbb33334444" } : { online: true, version: "bbbb33334444", current_version: "bbbb33334444" },
    );
    await page();

    const laptop = await screen.findByRole("region", { name: "my-laptop" });
    expect(within(laptop).getByText(/Update available: restart the host to run bbbb33334444/)).toBeInTheDocument();
    const desktop = screen.getByRole("region", { name: "my-desktop" });
    expect(within(desktop).getByText(/Host version bbbb33334444/)).toBeInTheDocument();
    expect(within(desktop).queryByText(/Update available/)).not.toBeInTheDocument();
  });

  it("says so when a machine's sessions cannot be read, rather than listing none", async () => {
    vi.spyOn(apiClient, "listConversations").mockImplementation(async (id) => {
      if (id === "c-laptop") throw new Error("down");
      return SESSIONS[id] ?? [];
    });
    // Another machine is connected, which must not stand in for the unreadable one.
    SESSIONS["c-desktop"] = [makeConversation({ id: OLD, conductor_id: "c-desktop", state: "ready" })];
    const onSelect = vi.fn();
    await page({ onSelect, conductorId: "c-laptop" });

    const laptopGroup = await screen.findByRole("region", { name: "my-laptop" });
    expect(onSelect).not.toHaveBeenCalled();
    expect(within(laptopGroup).getByRole("status")).toHaveTextContent("Could not read this machine's sessions.");
    expect(within(screen.getByRole("region", { name: "my-desktop" })).queryByRole("status")).not.toBeInTheDocument();
    SESSIONS["c-desktop"] = [];
  });

  it("highlights the open session and shows its conversation on the right", async () => {
    await page({ conductorId: "c-laptop", sessionId: OLD });

    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
    const group = screen.getByRole("region", { name: "my-laptop" });
    const open = within(group)
      .getAllByRole("button")
      .filter((b) => b.getAttribute("aria-current") === "true");
    expect(open).toHaveLength(1);
    expect(open[0]).toHaveTextContent("Disconnected");
    expect(apiClient.getConversation).toHaveBeenCalledWith("c-laptop", OLD);
  });

  it("reports a session that is not on one of the person's remote controls, and never reads it", async () => {
    await page({ conductorId: "c-other", sessionId: OLD });

    expect(await screen.findByRole("alert")).toHaveTextContent("not on one of your remote controls");
    expect(apiClient.getConversation).not.toHaveBeenCalled();
  });

  it("opens a session or a new one on a machine from the panel", async () => {
    const onSelect = vi.fn();
    await page({ onSelect });

    const group = await screen.findByRole("region", { name: "my-laptop" });
    await userEvent.click(within(group).getByRole("button", { name: /Disconnected/ }));
    expect(onSelect).toHaveBeenLastCalledWith({ conductorId: "c-laptop", sessionId: OLD });

    await userEvent.click(within(screen.getByRole("region", { name: "my-desktop" })).getByRole("button", { name: "+ New session" }));
    expect(onSelect).toHaveBeenLastCalledWith({ conductorId: "c-desktop", isNew: true, draft: undefined });
  });

  it("is a new session on the first machine when none is named, with the draft in the message box and nothing sent", async () => {
    const start = vi.spyOn(apiClient, "startConversation");
    await page({ draft: "discuss slop-haus #7", isNew: true });

    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
    expect(screen.getByRole("region", { name: "New session" })).toBeInTheDocument();
    expect(start).not.toHaveBeenCalled();
  });

  describe("focus for a draft from the address", () => {
    const PROMPT = "discuss slop-haus #7";
    const message = () => screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement;

    it("focuses the connected session's message box, cursor at the end", async () => {
      addressedPage({ draft: PROMPT });
      await screen.findByRole("region", { name: "Session" });
      expect(message()).toHaveValue(PROMPT);
      await waitFor(() => expect(message()).toHaveFocus());
      expect([message().selectionStart, message().selectionEnd]).toEqual([PROMPT.length, PROMPT.length]);
    });

    it("focuses the New session box when no session is connected", async () => {
      SESSIONS["c-laptop"] = [];
      try {
        page({ draft: PROMPT });
        await screen.findByRole("region", { name: "New session" });
        expect(message()).toHaveValue(PROMPT);
        await waitFor(() => expect(message()).toHaveFocus());
        expect(message().selectionStart).toBe(PROMPT.length);
      } finally {
        SESSIONS["c-laptop"] = [
          makeConversation({ id: NEW, conductor_id: "c-laptop", state: "ready", updated_at: "2026-01-02T00:00:00Z" }),
          makeConversation({ id: OLD, conductor_id: "c-laptop", state: "stopped", updated_at: "2026-01-01T00:00:00Z" }),
        ];
      }
    });

    it("focuses nothing without a draft", async () => {
      page({ conductorId: "c-laptop", sessionId: OLD });
      await screen.findByRole("region", { name: "Session" });
      expect(message()).not.toHaveFocus();
    });

    it("does not focus a restored stored draft", async () => {
      const first = await page({ conductorId: "c-laptop", sessionId: OLD });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "stored");
      first.unmount();
      (document.activeElement as HTMLElement | null)?.blur();

      page({ conductorId: "c-laptop", sessionId: OLD });
      await screen.findByRole("region", { name: "Session" });
      expect(message()).toHaveValue("stored");
      expect(message()).not.toHaveFocus();
    });

    it("leaves focus alone when it is already in another input", async () => {
      const other = document.createElement("input");
      document.body.append(other);
      other.focus();
      try {
        addressedPage({ draft: PROMPT });
        await screen.findByRole("region", { name: "Session" });
        expect(other).toHaveFocus();
      } finally {
        other.remove();
      }
    });

    it("does not take focus back after the address drops the draft", async () => {
      const { replaced } = addressedPage({ draft: PROMPT });
      await screen.findByRole("region", { name: "Session" });
      await vi.waitFor(() => expect(replaced.at(-1)).toEqual({ conductorId: "c-laptop", sessionId: NEW, replace: true }));
      message().blur();
      // A button is not an input the focus guard skips, so only the mount-only effect keeps this from refocusing.
      await userEvent.click(screen.getByRole("button", { name: /sessions$/ }));
      await userEvent.click(screen.getByRole("button", { name: /sessions$/ }));
      expect(message()).not.toHaveFocus();
    });
  });

  describe("the connected session", () => {
    const liveDesktop = makeConversation({ id: "77777777-7777-4777-8777-777777777777", conductor_id: "c-desktop", state: "starting", updated_at: "2026-01-03T00:00:00Z" });

    it("is what the page opens on when no session is named, by replacing the address", async () => {
      const onSelect = vi.fn();
      await page({ onSelect });

      await vi.waitFor(() => expect(onSelect).toHaveBeenCalledWith({ conductorId: "c-laptop", sessionId: NEW, draft: undefined, replace: true }));
      expect(screen.queryByRole("region", { name: "New session" })).not.toBeInTheDocument();
    });

    it("is the most recent one when two machines each have one", async () => {
      SESSIONS["c-desktop"] = [liveDesktop];
      const onSelect = vi.fn();
      try {
        await page({ onSelect });

        await vi.waitFor(() => expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ conductorId: "c-desktop", sessionId: liveDesktop.id })));
      } finally {
        SESSIONS["c-desktop"] = [];
      }
    });

    it("prefers the machine the address names", async () => {
      SESSIONS["c-desktop"] = [liveDesktop];
      const onSelect = vi.fn();
      try {
        await page({ onSelect, conductorId: "c-laptop" });

        await vi.waitFor(() => expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ conductorId: "c-laptop", sessionId: NEW })));
      } finally {
        SESSIONS["c-desktop"] = [];
      }
    });

    it("falls back to another machine's when the named one has none", async () => {
      const onSelect = vi.fn();
      await page({ onSelect, conductorId: "c-desktop" });

      await vi.waitFor(() => expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ conductorId: "c-laptop", sessionId: NEW })));
    });

    it("carries a message typed into the default New session when a connected one turns up", async () => {
      SESSIONS["c-laptop"] = SESSIONS["c-laptop"].map((s) => ({ ...s, state: "stopped" as const }));
      const list = vi.spyOn(apiClient, "listConversations");
      // The first read of the laptop's sessions fails, so none is known to be connected.
      list.mockRejectedValueOnce(new Error("down"));
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const { replaced } = addressedPage();
      try {
        const box = await screen.findByRole("textbox", { name: "Message" });
        await userEvent.type(box, "typed early");
        SESSIONS["c-laptop"][0] = { ...SESSIONS["c-laptop"][0], state: "ready" };
        await vi.advanceTimersByTimeAsync(20_000);

        await screen.findByRole("region", { name: "Session" });
        expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("typed early");
        expect(replaced[0]).toEqual(expect.objectContaining({ sessionId: NEW, draft: "typed early" }));
      } finally {
        vi.useRealTimers();
        SESSIONS["c-laptop"][0] = { ...SESSIONS["c-laptop"][0], state: "ready" };
      }
    });

    it("shows New session when none is connected", async () => {
      SESSIONS["c-laptop"] = SESSIONS["c-laptop"].map((s) => ({ ...s, state: "stopped" as const }));
      const onSelect = vi.fn();
      try {
        await page({ onSelect });

        expect(await screen.findByRole("region", { name: "New session" })).toBeInTheDocument();
        expect(onSelect).not.toHaveBeenCalled();
      } finally {
        SESSIONS["c-laptop"][0] = { ...SESSIONS["c-laptop"][0], state: "ready" };
      }
    });

    it("leaves an explicit new session alone", async () => {
      const onSelect = vi.fn();
      await page({ onSelect, isNew: true });

      expect(await screen.findByRole("region", { name: "New session" })).toBeInTheDocument();
      expect(onSelect).not.toHaveBeenCalled();
    });

    it("takes a draft into its message box unsent, then drops it from the address", async () => {
      const start = vi.spyOn(apiClient, "startConversation");
      const send = vi.spyOn(apiClient, "sendConversationMessage");
      const { replaced } = addressedPage({ draft: "discuss slop-haus #7" });

      expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
      await vi.waitFor(() => expect(replaced.at(-1)).toEqual({ conductorId: "c-laptop", sessionId: NEW, replace: true }));
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
      expect(start).not.toHaveBeenCalled();
      expect(send).not.toHaveBeenCalled();
    });

    it("goes to a new session with the draft, when + New session is chosen from the session", async () => {
      const { replaced, selections } = addressedPage({ draft: "discuss slop-haus #7" });
      await screen.findByRole("region", { name: "Session" });
      await vi.waitFor(() => expect(replaced.length).toBeGreaterThan(1));
      const box = screen.getByRole("textbox", { name: "Message" });
      await userEvent.clear(box);
      await userEvent.type(box, "edited by hand");

      await userEvent.click(within(screen.getByRole("region", { name: "my-laptop" })).getByRole("button", { name: "+ New session" }));

      expect(await screen.findByRole("region", { name: "New session" })).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("edited by hand");
      // What was typed stays out of the address.
      expect(selections.filter((sel) => sel.draft === "edited by hand")).toEqual([]);
    });
  });

  describe("unsent text across a visit to another tab", () => {
    const box = () => screen.getByRole("textbox", { name: "Message" });

    it("is kept for a live session, and not shared with another session", async () => {
      const first = await page({ conductorId: "c-laptop", sessionId: OLD });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "half a thought");
      first.unmount();

      await page({ conductorId: "c-laptop", sessionId: OLD });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("half a thought");
    });

    it("does not leak into another session", async () => {
      const first = await page({ conductorId: "c-laptop", sessionId: OLD });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "for old");
      first.unmount();

      await page({ conductorId: "c-laptop", sessionId: NEW });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("");
    });

    it("is kept for a new session", async () => {
      const first = await page({ conductorId: "c-laptop", isNew: true });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "new idea");
      first.unmount();

      await page({ conductorId: "c-laptop", isNew: true });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("new idea");
    });

    it("is cleared by a send, and kept by a failed one", async () => {
      const send = vi.spyOn(apiClient, "sendConversationMessage").mockRejectedValueOnce(new Error("down"));
      const first = await page({ conductorId: "c-laptop", sessionId: OLD });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "ping");
      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      await vi.waitFor(() => expect(send).toHaveBeenCalledTimes(1));
      first.unmount();

      const second = await page({ conductorId: "c-laptop", sessionId: OLD });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("ping");

      send.mockResolvedValue(makeConversationEvent({ seq: 1, kind: "user", body: "ping", turn: null }));
      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      await vi.waitFor(() => expect(box()).toHaveValue(""));
      second.unmount();

      await page({ conductorId: "c-laptop", sessionId: OLD });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("");
    });

    it("lets an address draft win over a stored one", async () => {
      const first = await page({ conductorId: "c-laptop", sessionId: OLD });
      await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "stored");
      first.unmount();

      await page({ conductorId: "c-laptop", sessionId: OLD, draft: "discuss slop-haus #7" });
      await screen.findByRole("textbox", { name: "Message" });
      expect(box()).toHaveValue("discuss slop-haus #7");
    });
  });

  it("opens the session it started, and the list follows", async () => {
    const onSelect = vi.fn();
    vi.spyOn(apiClient, "startConversation").mockResolvedValue(makeConversation({ id: NEW, conductor_id: "c-laptop" }));
    await page({ onSelect, conductorId: "c-laptop", isNew: true });

    await userEvent.type(await screen.findByRole("textbox", { name: "Message" }), "hi");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await vi.waitFor(() => expect(onSelect).toHaveBeenCalledWith({ conductorId: "c-laptop", sessionId: NEW }));
    expect(vi.mocked(apiClient.listConversations).mock.calls.length).toBeGreaterThan(2);
  });

  it("keeps no setup or help sections above the chat on desktop", async () => {
    const { container } = await page();

    await screen.findByRole("region", { name: "my-laptop" });
    expect(container.querySelector("details")).toBeNull();
    expect(screen.queryByText("Add a remote control", { selector: "summary" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Add a remote control" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Running a remote control" })).not.toBeInTheDocument();
  });

  it("with no remote control, the empty pane links to both views", async () => {
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
    const { selections, rendered } = addressedPage();
    await rendered;

    const main = await screen.findByRole("main");
    await userEvent.click(await within(main).findByRole("button", { name: "add a remote control" }));
    expect(selections).toEqual([{ view: "setup" }]);
    expect(await screen.findByRole("heading", { name: "Add a remote control" })).toBeInTheDocument();
  });

  it("with no remote control, the empty pane can open how to run one", async () => {
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
    const { selections, rendered } = addressedPage();
    await rendered;

    await userEvent.click(await screen.findByRole("button", { name: "how to run one" }));
    expect(selections).toEqual([{ view: "running" }]);
    expect(await screen.findByRole("heading", { name: "Running a remote control" })).toBeInTheDocument();
  });

  describe("the setup and running views", () => {
    const nav = () => screen.findByRole("navigation", { name: "Remote control help" });

    it("sit under Hide sessions in the sessions panel, and neither is current at first", async () => {
      await page();

      const links = within(await nav()).getAllByRole("button");
      expect(links.map((l) => l.textContent)).toEqual(["Add a remote control", "Running a remote control", "Remote controls in this tenant"]);
      links.forEach((l) => expect(l).not.toHaveAttribute("aria-current"));
      const toggle = screen.getByRole("button", { name: "Hide sessions" });
      expect(toggle.compareDocumentPosition(await nav()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("are hidden when the panel is collapsed", async () => {
      storage.setItem("remote_control.sessions_panel", "closed");
      await page();

      await screen.findByRole("button", { name: "Show sessions" });
      expect(screen.queryByRole("navigation", { name: "Remote control help" })).not.toBeInTheDocument();
    });

    it("open in the main pane through the address, and are marked current", async () => {
      const { selections, rendered } = addressedPage({ isNew: true });
      await rendered;

      await userEvent.click(within(await nav()).getByRole("button", { name: "Add a remote control" }));

      expect(selections).toEqual([{ view: "setup" }]);
      expect(await screen.findByRole("heading", { name: "Add a remote control" })).toBeInTheDocument();
      expect(within(await nav()).getByRole("button", { name: "Add a remote control" })).toHaveAttribute("aria-current", "page");
      expect(screen.queryByRole("textbox", { name: "Message" })).not.toBeInTheDocument();

      await userEvent.click(within(await nav()).getByRole("button", { name: "Running a remote control" }));
      expect(selections[1]).toEqual({ view: "running" });
      expect(await screen.findByRole("heading", { name: "Running a remote control" })).toBeInTheDocument();
      expect(within(await nav()).getByRole("button", { name: "Add a remote control" })).not.toHaveAttribute("aria-current");
    });

    it("open the tenant's remote controls in the main pane", async () => {
      const { selections, rendered } = addressedPage({ isNew: true });
      await rendered;

      await userEvent.click(within(await nav()).getByRole("button", { name: "Remote controls in this tenant" }));

      expect(selections).toEqual([{ view: "tenant" }]);
      expect(await screen.findByRole("heading", { name: "Remote controls in this tenant" })).toBeInTheDocument();
      expect(within(await nav()).getByRole("button", { name: "Remote controls in this tenant" })).toHaveAttribute("aria-current", "page");
    });

    it("open directly from an address that names one, without opening the connected session", async () => {
      const { selections, rendered } = addressedPage({ view: "setup" });
      await rendered;

      expect(await screen.findByRole("heading", { name: "Add a remote control" })).toBeInTheDocument();
      await screen.findByRole("region", { name: "my-laptop" });
      expect(selections).toEqual([]);
    });

    it("are left by opening a session", async () => {
      const { selections, rendered } = addressedPage({ view: "running" });
      await rendered;

      const group = await screen.findByRole("region", { name: "my-laptop" });
      await userEvent.click(within(group).getByRole("button", { name: /Disconnected/ }));

      expect(selections).toEqual([{ conductorId: "c-laptop", sessionId: OLD }]);
      expect(await screen.findByRole("textbox", { name: "Message" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Running a remote control" })).not.toBeInTheDocument();
    });

    it("are left by New session", async () => {
      const { selections, rendered } = addressedPage({ view: "setup" });
      await rendered;

      const group = await screen.findByRole("region", { name: "my-laptop" });
      await userEvent.click(within(group).getByRole("button", { name: "+ New session" }));

      expect(selections).toEqual([{ conductorId: "c-laptop", isNew: true }]);
      expect(await screen.findByRole("textbox", { name: "Message" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Add a remote control" })).not.toBeInTheDocument();
    });
  });

  it("gives the message box three lines on desktop", async () => {
    await page({ conductorId: "c-laptop", sessionId: OLD });

    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveAttribute("rows", "3");
  });

  it("gives a new session's message box three lines on desktop", async () => {
    await page({ isNew: true });

    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveAttribute("rows", "3");
  });

  describe("the sessions panel", () => {
    it("collapses and opens, and the choice is kept for this device", async () => {
      await page();
      await screen.findByRole("region", { name: "my-laptop" });
      expect(screen.getByRole("button", { name: "Hide sessions" })).toHaveAttribute("aria-expanded", "true");

      await userEvent.click(screen.getByRole("button", { name: "Hide sessions" }));

      expect(screen.queryByRole("region", { name: "my-laptop" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Show sessions" })).toHaveAttribute("aria-expanded", "false");
      expect(storage.getItem("remote_control.sessions_panel")).toBe("closed");
    });

    it("comes back closed on a later visit, on a wide screen", async () => {
      storage.setItem("remote_control.sessions_panel", "closed");
      await page();

      expect(await screen.findByRole("button", { name: "Show sessions" })).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "my-laptop" })).not.toBeInTheDocument();
    });

  });

  describe("on a phone", () => {
    beforeEach(() => {
      stubNarrowLayout();
    });

    it("with a session in the address shows the chat alone: no setup, help or sessions", async () => {
      const { selections, rendered } = addressedPage({ conductorId: "c-laptop", sessionId: OLD });
      await rendered;

      await screen.findByRole("textbox", { name: "Message" });
      expect(screen.queryByText("Add a remote control", { selector: "summary" })).not.toBeInTheDocument();
      expect(screen.queryByText("Running a remote control", { selector: "summary" })).not.toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "my-laptop" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^(show|hide) sessions$/i })).not.toBeInTheDocument();
      expect(selections).toEqual([]);
    });

    it("‹ Sessions goes to the list through onSelect and selects no session", async () => {
      const { selections, rendered } = addressedPage({ conductorId: "c-laptop", sessionId: OLD });
      await rendered;
      await screen.findByRole("textbox", { name: "Message" });

      await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));

      // isNew, or the connected session would be opened again at once.
      expect(selections).toEqual([{ conductorId: "c-laptop", isNew: true }]);
      expect(await screen.findByRole("region", { name: "my-laptop" })).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "Session" })).not.toBeInTheDocument();
      expect(screen.queryByRole("textbox", { name: "Message" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Connected/ })).not.toHaveAttribute("aria-current");
    });

    it("without a session shows the list, with the setup and help below it, and no chat", async () => {
      const { rendered: promise } = addressedPage({ isNew: true });
      const rendered = await promise;

      const group = await screen.findByRole("region", { name: "my-laptop" });
      expect(within(group).getByRole("button", { name: "+ New session" })).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "Session" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Show sessions" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Hide sessions" })).not.toBeInTheDocument();
      const root = rendered.container.querySelector(".remote-control") as HTMLElement;
      expect(root.lastElementChild).toHaveClass("tenant-remote-controls");
      expect(root.lastElementChild?.previousElementSibling).toHaveClass("running-help");
      expect(screen.getByText("Add a remote control", { selector: "summary" })).toBeInTheDocument();
    });

    it("ignores a view in the address: the list screen, the sections below it, two-line box", async () => {
      await addressedPage({ view: "setup", isNew: true }).rendered;

      expect(await screen.findByRole("region", { name: "my-laptop" })).toBeInTheDocument();
      expect(screen.queryByRole("navigation", { name: "Remote control help" })).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Add a remote control" })).not.toBeInTheDocument();
      expect(screen.getByText("Add a remote control", { selector: "summary" })).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute("rows", "2");
    });

    it("keeps the message box at two lines on the chat screen", async () => {
      await addressedPage({ conductorId: "c-laptop", sessionId: OLD }).rendered;

      expect(await screen.findByRole("textbox", { name: "Message" })).toHaveAttribute("rows", "2");
    });

    it("says to add a remote control above when there is none, as before", async () => {
      vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [], others: [] });
      await page();

      expect(await screen.findByText("Add a remote control above to start a session.")).toBeInTheDocument();
    });

    it("opens the connected session on the chat screen", async () => {
      await addressedPage().rendered;

      expect(await screen.findByRole("textbox", { name: "Message" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "‹ Sessions" })).toBeInTheDocument();
    });

    it("can go back while the machines are still loading", async () => {
      vi.spyOn(apiClient, "listConductors").mockReturnValue(new Promise(() => {}));
      const { selections, rendered } = addressedPage({ conductorId: "c-laptop", sessionId: OLD });
      await rendered;

      await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));

      expect(selections).toEqual([{ conductorId: "c-laptop", isNew: true }]);
    });

    it("can go back when the machines could not be read", async () => {
      vi.spyOn(apiClient, "listConductors").mockRejectedValue(new Error("down"));
      const { selections, rendered } = addressedPage({ conductorId: "c-laptop", sessionId: OLD });
      await rendered;

      expect(await screen.findByRole("alert")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "‹ Sessions" }));

      expect(selections).toEqual([{ conductorId: "c-laptop", isNew: true }]);
    });

    it("can still go back when the session is not one of the person's", async () => {
      await addressedPage({ conductorId: "c-nowhere", sessionId: OLD }).rendered;

      expect(await screen.findByRole("alert")).toHaveTextContent("That session is not on one of your remote controls.");
      expect(screen.getByRole("button", { name: "‹ Sessions" })).toBeInTheDocument();
    });
  });
});
