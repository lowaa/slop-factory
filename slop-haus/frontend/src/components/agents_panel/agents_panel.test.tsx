import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { apiClient, ApiError, type AgentMessage, type Grant, type SupervisorStatus } from "@/api";
import {
  makeAgent,
  makeAgentMessage,
  makeAgentSession,
  makeGrant,
  makeHarnessConfig,
  makeHarnessConfigScope,
  makeLease,
} from "@/test/fixtures";
import { stubDownloads } from "@/test/stub_downloads";
import { renderSignedIn } from "@/test/render_signed_in";
import { LIVE_REFRESH_THROTTLE_MS } from "@/lib/live";
import AgentsPanel from "./agents_panel";
import { stubPanelDefaults, stubSideLists } from "./agents_panel_test_support";

// The setup help's harness tab is also called "Claude Code".
const GRANT_CLIENT = { selector: ":not([role=tab])" };
stubPanelDefaults();

describe("AgentsPanel add a machine", () => {
  // Adding a worker lives on the machine's group; Add a machine is setup only.
  it("no longer renders the Add a worker help", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    await renderSignedIn(<AgentsPanel />);
    await screen.findByRole("link", { name: "Add a machine" });
    expect(screen.queryByText("Add a worker")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download setup script" })).not.toBeInTheDocument();
    expect(document.querySelector("details.add-slot-manually")).toBeNull();
  });

  it("links to the add-a-machine page", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    await renderSignedIn(<AgentsPanel />);
    const link = await screen.findByRole("link", { name: "Add a machine" });
    expect(link.getAttribute("href")).toBe("/workers/add-machine");
  });
});

describe("AgentsPanel supervisor signal", () => {
  beforeEach(() => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "builder-1" })]);
  });

  it("says the supervisor is offline, with when it last polled and how much is claimable", async () => {
    vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue({
      offline: true,
      claimable: 4,
      last_polled_at: "2026-10-19T10:00:00Z",
      slots: [],
      supervisors: [],
      current_bundle_hash: "",
      current_protocol: 0,
    });
    await renderSignedIn(<AgentsPanel />);
    const banner = await screen.findByText(/Supervisor offline: last polled/);
    expect(banner).toHaveTextContent(/4 tickets claimable/);
    expect(banner).toHaveTextContent(new Date("2026-10-19T10:00:00Z").toLocaleString());
  });

  it("shows nothing while the supervisor is online", async () => {
    vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue({
      offline: false,
      claimable: 4,
      last_polled_at: "2026-10-19T10:00:00Z",
      slots: [],
      supervisors: [],
      current_bundle_hash: "",
      current_protocol: 0,
    });
    await renderSignedIn(<AgentsPanel />);
    await screen.findByText("builder-1");
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByText(/Supervisor offline/)).not.toBeInTheDocument();
  });

  it("shows nothing when no supervisor has ever polled", async () => {
    await renderSignedIn(<AgentsPanel />);
    await screen.findByText("builder-1");
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByText(/Supervisor offline/)).not.toBeInTheDocument();
  });

  it("notices a supervisor going quiet on an otherwise idle page", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const status = vi.spyOn(apiClient, "getSupervisorStatus");
      status.mockResolvedValue({ offline: false, claimable: 1, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("builder-1");
      expect(screen.queryByText(/Supervisor offline/)).not.toBeInTheDocument();

      status.mockResolvedValue({ offline: true, claimable: 1, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
      await act(async () => void (await vi.advanceTimersByTimeAsync(60_000)));
      expect(await screen.findByText(/Supervisor offline/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("refreshes the signal when the board stream reports a change", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const status = vi.spyOn(apiClient, "getSupervisorStatus");
      status.mockResolvedValue({ offline: false, claimable: 1, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
      let boardChanged: (revision: number) => void = () => {};
      vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange) => {
        boardChanged = (revision) => onChange(revision, { revision, reload: true });
        return () => {};
      });
      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("builder-1");

      status.mockResolvedValue({ offline: true, claimable: 2, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
      await act(async () => {
        boardChanged(2);
        await vi.advanceTimersByTimeAsync(LIVE_REFRESH_THROTTLE_MS);
      });
      expect(await screen.findByText(/2 tickets claimable/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the page working when the status cannot be fetched", async () => {
    vi.spyOn(apiClient, "getSupervisorStatus").mockRejectedValue(new ApiError(500, "boom"));
    await renderSignedIn(<AgentsPanel />);
    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByText(/Supervisor offline/)).not.toBeInTheDocument();
    expect(screen.queryByText("boom")).not.toBeInTheDocument();
  });
});

describe("AgentsPanel", () => {
  it("does not list remote controls: they are set up on the Remote Control tab", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "builder-1" })]);
    const conductors = vi.spyOn(apiClient, "listConductors");

    await renderSignedIn(<AgentsPanel />);

    await screen.findByText("builder-1");
    expect(conductors).not.toHaveBeenCalled();
    expect(screen.queryByText(/Add a remote control/)).not.toBeInTheDocument();
  });
});

describe("AgentsPanel messages to agents", () => {
  const AGENT = "11111111-1111-1111-1111-111111111111";

  it("sends a message to one agent and shows it pending", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    const send = vi.spyOn(apiClient, "sendAgentMessage").mockResolvedValue(makeAgentMessage());
    const list = vi.spyOn(apiClient, "listAgentMessages").mockResolvedValue([]);
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Message" }));
    list.mockResolvedValue([makeAgentMessage({ body: "stop, wrong approach" })]);
    await user.type(screen.getByLabelText("Message to builder-1"), "stop, wrong approach");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(send).toHaveBeenCalledWith(AGENT, "stop, wrong approach");
    const group = await screen.findByRole("group", { name: "Messages to builder-1" });
    expect(within(group).getByText("stop, wrong approach")).toBeInTheDocument();
    expect(within(group).getByText("pending")).toBeInTheDocument();
  });

  it("turns a pending message delivered when the board stream reports a change, without polling", async () => {
    stubSideLists();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
      const list = vi.spyOn(apiClient, "listAgentMessages").mockResolvedValue([makeAgentMessage()]);
      let boardChanged: (revision: number) => void = () => {};
      vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange) => {
        boardChanged = (revision) => onChange(revision, { revision, reload: true });
        return () => {};
      });
      await renderSignedIn(<AgentsPanel />);
      const group = await screen.findByRole("group", { name: "Messages to builder-1" });
      expect(within(group).getByText("pending")).toBeInTheDocument();

      // However long it is left, nothing but the stream refreshes it.
      await act(async () => void (await vi.advanceTimersByTimeAsync(60_000)));
      expect(list).toHaveBeenCalledTimes(1);

      list.mockResolvedValue([makeAgentMessage({ state: "delivered", delivered_at: "2026-01-01T00:05:00Z" })]);
      await act(async () => {
        boardChanged(2);
        await vi.advanceTimersByTimeAsync(LIVE_REFRESH_THROTTLE_MS);
      });

      // The group already exists from the pending message, so waiting for it
      // proves nothing about the refreshed content.
      expect(await within(screen.getByRole("group", { name: "Messages to builder-1" })).findByText(/delivered/)).toBeInTheDocument();
      expect(screen.queryByText("pending")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a delivered message as delivered with its time, and no read state", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    vi.spyOn(apiClient, "listAgentMessages").mockResolvedValue([
      makeAgentMessage({ state: "delivered", delivered_at: "2026-01-01T00:05:00Z" }),
    ]);
    await renderSignedIn(<AgentsPanel />);

    const group = await screen.findByRole("group", { name: "Messages to builder-1" });
    expect(within(group).getByText(/delivered/)).toBeInTheDocument();
    expect(within(group).queryByText(/\bread\b/i)).not.toBeInTheDocument();
    expect(within(group).queryByText("pending")).not.toBeInTheDocument();
  });

  it("shows a dropped ticket comment as not delivered, naming the ticket", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    vi.spyOn(apiClient, "listAgentMessages").mockResolvedValue([
      makeAgentMessage({ kind: "ticket_comment", ticket_id: 871, state: "dropped", dropped_at: "2026-01-01T00:05:00Z" }),
    ]);
    await renderSignedIn(<AgentsPanel />);

    const group = await screen.findByRole("group", { name: "Messages to builder-1" });
    expect(within(group).getByText("not delivered: the agent no longer held #871")).toBeInTheDocument();
    expect(within(group).queryByText("pending")).not.toBeInTheDocument();
  });

  it("shows only that agent's messages, and renders a message's text as text", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: AGENT, name: "builder-1" }),
      makeAgent({ id: "33333333-3333-3333-3333-333333333333", name: "builder-2" }),
    ]);
    vi.spyOn(apiClient, "listAgentMessages").mockResolvedValue([
      makeAgentMessage({ body: "<img src=x onerror=alert(1)>" }),
    ]);
    const { container } = await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(screen.queryByRole("group", { name: "Messages to builder-2" })).not.toBeInTheDocument();
  });

  it("keeps the newest listing when an older one answers last", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    let answerOlder: (m: AgentMessage[]) => void = () => {};
    const list = vi.spyOn(apiClient, "listAgentMessages");
    list.mockImplementationOnce(() => new Promise((resolve) => (answerOlder = resolve)));
    vi.spyOn(apiClient, "sendAgentMessage").mockResolvedValue(makeAgentMessage());
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    list.mockResolvedValue([makeAgentMessage({ body: "newer answer" })]);
    await user.click(await screen.findByRole("button", { name: "Message" }));
    await user.type(screen.getByLabelText("Message to builder-1"), "hi");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText("newer answer")).toBeInTheDocument();

    await act(async () => answerOlder([makeAgentMessage({ body: "older answer" })]));
    expect(screen.queryByText("older answer")).not.toBeInTheDocument();
    expect(screen.getByText("newer answer")).toBeInTheDocument();
  });

  it("offers no composer for a disabled agent", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: AGENT, name: "builder-1", disabled_at: "2026-01-01T00:00:00Z" }),
    ]);
    await renderSignedIn(<AgentsPanel />);

    await screen.findByText("builder-1");
    expect(screen.queryByRole("button", { name: "Message" })).not.toBeInTheDocument();
  });

  it("says why a send was refused", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: AGENT, name: "builder-1" })]);
    vi.spyOn(apiClient, "sendAgentMessage").mockRejectedValue(new ApiError(409, "that agent has too many undelivered messages"));
    const user = userEvent.setup();
    await renderSignedIn(<AgentsPanel />);

    await user.click(await screen.findByRole("button", { name: "Message" }));
    await user.type(screen.getByLabelText("Message to builder-1"), "hi");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("too many undelivered");
  });
});

describe("AgentsPanel", () => {
  it("shows a held lease's ticket and expiry, and 'idle' for an agent holding nothing", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        id: "11111111-1111-1111-1111-111111111111",
        name: "holder",
        lease: makeLease({ ticket_id: 42, expires_at: new Date(Date.now() + 5 * 60_000).toISOString() }),
      }),
      makeAgent({ id: "22222222-2222-2222-2222-222222222222", name: "idler", lease: null }),
    ]);

    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText("holder")).toBeInTheDocument();
    expect(screen.getByText(/#42/)).toBeInTheDocument();
    expect(screen.getByText(/expires in \d+m/)).toBeInTheDocument();
    expect(screen.getByText("idler")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
  });

  it("shows why a run the loop breaker let stop is stuck, and nothing for one that is not", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        id: "11111111-1111-1111-1111-111111111111",
        name: "stuck-agent",
        session: makeAgentSession({ stuck: { reason: "Stuck on #42: blocked 3 times with no progress.", at: "2026-10-04T09:00:00Z" } }),
      }),
      makeAgent({ id: "22222222-2222-2222-2222-222222222222", name: "fine-agent", session: makeAgentSession() }),
    ]);

    await renderSignedIn(<AgentsPanel />);

    const rowOf = (name: string) => screen.getByText(name).closest("li")!;
    await screen.findByText("stuck-agent");
    expect(within(rowOf("stuck-agent")).getByRole("status")).toHaveTextContent(
      /Stuck since .*: Stuck on #42: blocked 3 times with no progress\./,
    );
    expect(within(rowOf("fine-agent")).queryByRole("status")).toBeNull();
  });

  it("shows what each agent's current run says it is running, and nothing for one that never ran", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        id: "11111111-1111-1111-1111-111111111111",
        name: "pi-agent",
        session: makeAgentSession({
          harness: "pi",
          harness_version: "0.87.0",
          provider: "anthropic",
          model: "claude-sonnet-4-5",
          labels: { gpu: "a100", docker: "" },
        }),
      }),
      makeAgent({
        id: "22222222-2222-2222-2222-222222222222",
        name: "claude-agent",
        session: makeAgentSession({ harness: "claude", model: "claude-opus-5-5" }),
      }),
      makeAgent({ id: "33333333-3333-3333-3333-333333333333", name: "never-ran", session: null }),
      makeAgent({ id: "44444444-4444-4444-4444-444444444444", name: "undescribed", session: makeAgentSession() }),
    ]);

    await renderSignedIn(<AgentsPanel />);

    const rowOf = (name: string) => screen.getByText(name).closest("li")!;
    await screen.findByText("pi-agent");
    expect(
      within(rowOf("pi-agent")).getByText("pi 0.87.0 · anthropic/claude-sonnet-4-5 · docker · gpu=a100"),
    ).toBeInTheDocument();
    expect(within(rowOf("claude-agent")).getByText("claude · claude-opus-5-5")).toBeInTheDocument();
    for (const name of ["never-ran", "undescribed"]) {
      expect(rowOf(name).querySelector(".agents-panel__runs")).toBeNull();
    }
  });

  it("flags a lease whose expiry is in the past as expired", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        name: "stale-holder",
        lease: makeLease({ ticket_id: 7, expires_at: new Date(Date.now() - 60_000).toISOString() }),
      }),
    ]);

    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText(/#7/)).toBeInTheDocument();
    expect(screen.getByText("(expired)")).toBeInTheDocument();
  });

  // "Download setup script" registers the agent as well as handing over its
  // script, so a separate form that only reserves a name would be a second,
  // lesser way to do the same thing.
  it("offers no separate form for creating an agent by name", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);

    await renderSignedIn(<AgentsPanel />);
    await screen.findByText("No workers yet.");

    expect(screen.queryByRole("button", { name: "Create agent" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "New agent" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Name", { exact: true })).not.toBeInTheDocument();
  });

  it("disables an enabled agent via the toggle button", async () => {
    const user = userEvent.setup();
    stubSideLists();
    const agent = makeAgent({ name: "toggle-me", disabled_at: null });
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([agent]);
    vi.spyOn(apiClient, "setAgentDisabled").mockResolvedValue({ ...agent, disabled_at: "2026-01-02T00:00:00Z" });

    await renderSignedIn(<AgentsPanel />);
    await screen.findByText("toggle-me");

    await user.click(screen.getByRole("button", { name: "Disable" }));

    expect(apiClient.setAgentDisabled).toHaveBeenCalledWith(agent.id, true);
    expect(await screen.findByText("Disabled")).toBeInTheDocument();
  });

  describe("sessions and workers", () => {
    it("lists consoles under Your sessions and keeps them out of the worker roster", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "a-worker" }),
        makeAgent({ id: "a2", name: "a-console", kind: "console" }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("a-worker");

      const sessions = screen.getByRole("region", { name: "Your sessions" });
      expect(within(sessions).getByText("a-console")).toBeInTheDocument();
      expect(within(sessions).queryByText("a-worker")).not.toBeInTheDocument();
      expect(screen.getAllByText("a-console")).toHaveLength(1);
      expect(screen.queryByRole("switch", { name: "Console" })).not.toBeInTheDocument();
    });

    it("shows the supervisor's report on the steward's row, and only there", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({
          id: "a3",
          name: "steward",
          kind: "steward",
          observed: { phase: "failed", reason: "Codex steward not started: sandbox probe refused `network` - no netns", reported_at: "2026-10-29T09:00:00Z" },
        }),
        makeAgent({ id: "a1", name: "a-worker" }),
      ]);
      await renderSignedIn(<AgentsPanel />);

      const steward = await screen.findByRole("region", { name: "Steward" });
      expect(within(steward).getByRole("status")).toHaveTextContent(
        "Codex steward not started: sandbox probe refused `network` - no netns",
      );
      expect(screen.getAllByRole("status")).toHaveLength(1);
    });

    it("lists the steward under its own heading, not the worker roster", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "a-worker" }),
        makeAgent({ id: "a3", name: "steward", kind: "steward" }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("a-worker");

      const steward = screen.getByRole("region", { name: "Steward" });
      expect(within(steward).getByText("steward")).toBeInTheDocument();
      expect(within(steward).queryByText("a-worker")).not.toBeInTheDocument();
      expect(screen.getAllByText("steward")).toHaveLength(1);
    });

    it("says so when there are no workers or sessions", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "only-console", kind: "console" })]);

      await renderSignedIn(<AgentsPanel />);

      expect(await screen.findByText("No workers yet.")).toBeInTheDocument();
      expect(screen.getByText("only-console")).toBeInTheDocument();
      expect(screen.queryByText("No sessions yet.")).not.toBeInTheDocument();
    });

    it("says there are no sessions when none exist", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "w" })]);

      await renderSignedIn(<AgentsPanel />);

      expect(await screen.findByText("No sessions yet.")).toBeInTheDocument();
    });
  });

  describe("action outcome placement", () => {
    function rowOf(name: string): HTMLElement {
      return screen.getByText(name).closest("li") as HTMLElement;
    }

    it("shows a refusal in the row that raised it", async () => {
      const user = userEvent.setup();
      stubSideLists();
      const agents = [makeAgent({ id: "a1", name: "first" }), makeAgent({ id: "a2", name: "second" })];
      vi.spyOn(apiClient, "listAgents").mockResolvedValue(agents);
      vi.spyOn(apiClient, "setAgentDisabled").mockRejectedValue(new ApiError(409, "agent is busy"));

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("second");
      await user.click(within(rowOf("second")).getByRole("button", { name: "Disable" }));

      expect(await within(rowOf("second")).findByRole("alert")).toHaveTextContent("agent is busy");
      expect(within(rowOf("first")).queryByRole("alert")).not.toBeInTheDocument();
    });
  });

  // The defaults are what each row's override builds on, so they are read
  // first -- whether or not the roster has arrived yet.
  describe("harness config placement", () => {
    function expectBefore(first: Element, second: Element) {
      expect(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }

    it("puts the harness config ahead of the roster once both have loaded", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "ordered" })]);

      await renderSignedIn(<AgentsPanel />);
      const roster = (await screen.findByText("ordered")).closest(".agents-panel__roster");
      expect(roster).not.toBeNull();

      expectBefore(screen.getByRole("heading", { name: "Harness config" }), roster!);
    });

    it("puts the harness config ahead of the roster while the agents are still loading", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockReturnValue(new Promise(() => {}));

      await renderSignedIn(<AgentsPanel />);

      expectBefore(screen.getByRole("heading", { name: "Harness config" }), screen.getByText("Loading workers…"));
    });
  });

  describe("button descriptions", () => {
    it("explains each row action and Revoke to a keyboard user, without a delay", async () => {
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([makeGrant({ id: "g1", agent_id: "a1" })]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByRole("button", { name: "Revoke" });

      const expected: Record<string, RegExp> = {
        "New setup script": /signs out every workspace/,
        Message: /next call to slop-haus/,
        "Edit roles": /which tickets it may claim/,
        Disable: /can no longer claim tickets/,
        Revoke: /access token stops working immediately/,
      };
      for (const [name, description] of Object.entries(expected)) {
        const button = await screen.findByRole("button", { name });
        act(() => button.focus());
        expect(await screen.findByRole("tooltip")).toHaveTextContent(description);
        expect(button.getAttribute("aria-describedby")).toBe(screen.getByRole("tooltip").id);
        await user.keyboard("{Escape}");
      }
    });
  });

  describe("new setup script for an existing agent", () => {
    it("is not offered on a disabled agent", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "on", disabled_at: null }),
        makeAgent({ id: "a2", name: "off", disabled_at: "2026-01-02T00:00:00Z" }),
      ]);

      await renderSignedIn(<AgentsPanel />);

      const on = (await screen.findByText("on")).closest("li")!;
      const off = screen.getByText("off").closest("li")!;
      expect(within(on).getByRole("button", { name: "New setup script" })).toBeInTheDocument();
      expect(within(off).queryByRole("button", { name: "New setup script" })).not.toBeInTheDocument();
    });

    it("asks first, and cancelling signs nothing out", async () => {
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      const rotate = vi.spyOn(apiClient, "rotateAgentSetupScript");

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "New setup script" }));

      const confirm = screen.getByRole("group", { name: "Confirm new setup script for builder-1" });
      expect(confirm).toHaveTextContent("signs out every workspace running as builder-1");
      await user.click(within(confirm).getByRole("button", { name: "Cancel" }));

      expect(screen.queryByRole("group", { name: /Confirm new setup script/ })).not.toBeInTheDocument();
      expect(rotate).not.toHaveBeenCalled();
    });

    it("on confirming, saves the new script and refreshes the authorizations it revoked", async () => {
      const user = userEvent.setup();
      const clicked = stubDownloads();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      const listGrants = vi
        .spyOn(apiClient, "listGrants")
        .mockResolvedValueOnce([makeGrant({ id: "g1", agent_id: "a1", client_name: "Claude Code" })])
        .mockResolvedValueOnce([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      const rotate = vi.spyOn(apiClient, "rotateAgentSetupScript").mockResolvedValue({
        filename: "slop-haus-setup-builder-1.sh",
        script: "#!/usr/bin/env bash\n",
      });

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "New setup script" }));
      await user.click(screen.getByRole("button", { name: "Sign out and download" }));

      expect(rotate).toHaveBeenCalledWith("a1");
      expect(clicked.map((c) => c.filename)).toEqual(["slop-haus-setup-builder-1.sh"]);
      expect(await clicked[0].blob?.text()).toBe("#!/usr/bin/env bash\n");
      expect(await screen.findByRole("status")).toHaveTextContent("slop-haus-setup-builder-1.sh");
      await vi.waitFor(() => expect(listGrants).toHaveBeenCalledTimes(2));
      expect(await screen.findByText("No authorizations")).toBeInTheDocument();
    });

    it("surfaces a refusal and saves nothing", async () => {
      const user = userEvent.setup();
      const clicked = stubDownloads();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      const listGrants = vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      vi.spyOn(apiClient, "rotateAgentSetupScript").mockRejectedValue(new ApiError(409, "agent is disabled"));

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "New setup script" }));
      await user.click(screen.getByRole("button", { name: "Sign out and download" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("agent is disabled");
      expect(clicked).toHaveLength(0);
      // The listing is refreshed regardless: a request that failed after
      // the server revoked must not leave revoked grants on screen.
      await vi.waitFor(() => expect(listGrants).toHaveBeenCalledTimes(2));
    });
  });

  describe("hide disabled", () => {
    it("drops disabled agents from the roster while ticked", async () => {
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "on", disabled_at: null }),
        makeAgent({ id: "a2", name: "off", disabled_at: "2026-01-02T00:00:00Z" }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("off");
      const filter = screen.getByRole("checkbox", { name: "Hide disabled" });
      expect(filter).not.toBeChecked();

      await user.click(filter);
      expect(screen.queryByText("off")).not.toBeInTheDocument();
      expect(screen.getByText("on")).toBeInTheDocument();

      await user.click(filter);
      expect(screen.getByText("off")).toBeInTheDocument();
    });

    it("says so when every agent is hidden, rather than looking empty", async () => {
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a2", name: "off", disabled_at: "2026-01-02T00:00:00Z" }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("off");
      await user.click(screen.getByRole("checkbox", { name: "Hide disabled" }));

      expect(screen.getByText("Every worker is disabled.")).toBeInTheDocument();
      expect(screen.queryByText("No workers yet.")).not.toBeInTheDocument();
    });
  });

  describe("replaced agents", () => {
    it("says which agent replaced which, by name", async () => {
      const OFF = "2026-01-01T00:00:00Z";
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "old-worker", disabled_at: OFF, archived_at: OFF, replaced_by: { id: "a2", name: "new-console" } }),
        makeAgent({ id: "a2", name: "new-console", kind: "console", replaces: { id: "a1", name: "old-worker" } }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("checkbox", { name: "Show removed (1)" }));

      const archived = within(screen.getByRole("list", { name: "Removed agents" }));
      expect(archived.getByText("replaced by new-console")).toBeInTheDocument();
      expect(screen.getByText("replaces old-worker")).toBeInTheDocument();
    });
  });

  describe("archived agents", () => {
    const OFF = "2026-01-02T00:00:00Z";

    it("keeps removed agents out of the roster until Show removed is ticked", async () => {
      const user = userEvent.setup();
      stubSideLists();
      const list = vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "on" }),
        makeAgent({ id: "a2", name: "put-away", disabled_at: OFF, archived_at: OFF }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("on");
      expect(screen.queryByText("put-away")).not.toBeInTheDocument();
      expect(list).toHaveBeenCalledWith({ includeArchived: true });

      await user.click(screen.getByRole("checkbox", { name: "Show removed (1)" }));
      const removed = within(screen.getByRole("list", { name: "Removed agents" }));
      expect(removed.getByText("put-away")).toBeInTheDocument();
      expect(removed.getByText("Removed")).toBeInTheDocument();
      expect(removed.getByRole("button", { name: "Restore" })).toBeInTheDocument();
      expect(removed.queryByRole("button", { name: "Enable" })).not.toBeInTheDocument();
    });

    it("offers Remove on every live agent, enabled or not", async () => {
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "on" }),
        makeAgent({ id: "a2", name: "off", disabled_at: OFF }),
      ]);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("off");

      expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(2);
    });

    it("removes an enabled agent in one confirmed step: disable, then archive", async () => {
      const user = userEvent.setup();
      stubSideLists();
      const on = makeAgent({ id: "a2", name: "on" });
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([on]);
      const disable = vi.spyOn(apiClient, "setAgentDisabled").mockResolvedValue({ ...on, disabled_at: OFF });
      const archive = vi.spyOn(apiClient, "archiveAgent").mockResolvedValue({ ...on, disabled_at: OFF, archived_at: OFF });

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Remove" }));
      expect(disable).not.toHaveBeenCalled();
      const confirm = screen.getByRole("group", { name: "Confirm removing on" });
      expect(confirm).toHaveTextContent("Hidden and kept for history (its tickets, comments and usage stay); you can restore it");
      await user.click(within(confirm).getByRole("button", { name: "Remove" }));

      expect(disable).toHaveBeenCalledWith("a2", true);
      expect(archive).toHaveBeenCalledWith("a2");
      expect(await screen.findByRole("checkbox", { name: "Show removed (1)" })).toBeInTheDocument();
      expect(screen.queryByText("on")).not.toBeInTheDocument();
    });

    it("keeps a half-removed agent on screen under Hide disabled, with its error", async () => {
      const user = userEvent.setup();
      stubSideLists();
      const on = makeAgent({ id: "a2", name: "on" });
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([on]);
      vi.spyOn(apiClient, "setAgentDisabled").mockResolvedValue({ ...on, disabled_at: OFF });
      vi.spyOn(apiClient, "archiveAgent").mockRejectedValue(new ApiError(500, "boom"));

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("checkbox", { name: "Hide disabled" }));
      await user.click(await screen.findByRole("button", { name: "Remove" }));
      await user.click(within(screen.getByRole("group", { name: "Confirm removing on" })).getByRole("button", { name: "Remove" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("boom");
      expect(screen.getByRole("button", { name: "Retry remove" })).toBeInTheDocument();
    });

    it("shows a refused remove on the agent's row", async () => {
      const user = userEvent.setup();
      stubSideLists();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a2", name: "off", disabled_at: OFF })]);
      vi.spyOn(apiClient, "archiveAgent").mockRejectedValue(new ApiError(403, "not allowed"));

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "Remove" }));
      await user.click(within(screen.getByRole("group", { name: "Confirm removing off" })).getByRole("button", { name: "Remove" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("not allowed");
    });
  });

  describe("authorizations", () => {
    it("never lets an older listing overwrite a newer one", async () => {
      const user = userEvent.setup();
      stubDownloads();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      let resolveInitial: (grants: Grant[]) => void = () => {};
      vi.spyOn(apiClient, "listGrants")
        .mockImplementationOnce(() => new Promise((resolve) => (resolveInitial = resolve)))
        .mockResolvedValueOnce([]);
      vi.spyOn(apiClient, "rotateAgentSetupScript").mockResolvedValue({ filename: "s.sh", script: "" });

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("button", { name: "New setup script" }));
      await user.click(screen.getByRole("button", { name: "Sign out and download" }));
      expect(await screen.findByText("No authorizations")).toBeInTheDocument();

      resolveInitial([makeGrant({ id: "g1", agent_id: "a1", client_name: "Stale Client" })]);
      await new Promise((r) => setTimeout(r, 0));

      expect(screen.queryByText("Stale Client")).not.toBeInTheDocument();
      expect(screen.getByText("No authorizations")).toBeInTheDocument();
    });

    it("lists grants on the row of the agent each one acts as", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "builder-1" }),
        makeAgent({ id: "a2", name: "builder-2" }),
      ]);
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([
        makeGrant({ id: "g1", agent_id: "a1", client_name: "Claude Code", last_used_at: null }),
        makeGrant({ id: "g2", agent_id: "a1", client_name: "slop-haus workspace", last_used_at: "2026-01-02T00:00:00Z" }),
        makeGrant({ id: "g3", agent_id: "a2", client_name: "Other Client" }),
      ]);

      await renderSignedIn(<AgentsPanel />);

      await screen.findByText("Other Client");
      const rowOf = (name: string) => screen.getByText(name).closest("li.agents-panel__row")!;
      const builder1 = rowOf("builder-1");
      expect(builder1).toHaveTextContent("Claude Code");
      expect(builder1).toHaveTextContent("slop-haus workspace");
      expect(builder1).toHaveTextContent("never used");
      expect(builder1).not.toHaveTextContent("Other Client");
      expect(rowOf("builder-2")).toHaveTextContent("Other Client");
      expect(screen.queryByText("Unlinked authorizations")).not.toBeInTheDocument();
    });

    it("says an agent with no authorizations has none, an archived one included", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([
        makeAgent({ id: "a1", name: "old-worker", disabled_at: "2026-01-02T00:00:00Z", archived_at: "2026-01-03T00:00:00Z" }),
      ]);
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([]);

      await renderSignedIn(<AgentsPanel />);
      await user.click(await screen.findByRole("checkbox", { name: /Show removed/ }));

      const archived = screen.getByRole("list", { name: "Removed agents" });
      expect(within(archived).getByText("No authorizations")).toBeInTheDocument();
    });

    it("shows a grant whose agent has no row under unlinked authorizations, and only then", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([
        makeGrant({ id: "g1", agent_id: "a1", client_name: "Known Client" }),
        makeGrant({ id: "g2", agent_id: "ghost-id", agent_name: "ghost", client_name: "Lost Client" }),
        makeGrant({ id: "g3", agent_id: "c1", agent_kind: "conductor", client_name: "Conductor Client" }),
      ]);

      await renderSignedIn(<AgentsPanel />);

      const unlinked = await screen.findByRole("region", { name: "Unlinked authorizations" });
      expect(unlinked).toHaveTextContent("ghost");
      expect(unlinked).toHaveTextContent("ghost-id");
      expect(unlinked).toHaveTextContent("Lost Client");
      expect(unlinked).not.toHaveTextContent("Known Client");
      expect(screen.queryByText("Conductor Client")).not.toBeInTheDocument();
    });

    it("revokes a grant and drops it from the list", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      const listGrants = vi
        .spyOn(apiClient, "listGrants")
        .mockResolvedValueOnce([makeGrant({ id: "g1", agent_id: "a1", client_name: "Claude Code" })])
        .mockResolvedValueOnce([]);
      const revokeGrant = vi.spyOn(apiClient, "revokeGrant").mockResolvedValue(undefined);

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("Claude Code", GRANT_CLIENT);

      await user.click(screen.getByRole("button", { name: "Revoke" }));

      expect(revokeGrant).toHaveBeenCalledWith("g1");
      await vi.waitFor(() => expect(listGrants).toHaveBeenCalledTimes(2));
      expect(await screen.findByText("No authorizations")).toBeInTheDocument();
    });

    it("surfaces a revoke failure without dropping the grant", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a1", name: "builder-1" })]);
      vi.spyOn(apiClient, "listGrants").mockResolvedValue([
        makeGrant({ id: "g1", agent_id: "a1", client_name: "Claude Code" }),
      ]);
      vi.spyOn(apiClient, "revokeGrant").mockRejectedValue(new ApiError(404, "no such grant"));

      await renderSignedIn(<AgentsPanel />);
      await screen.findByText("Claude Code", GRANT_CLIENT);

      await user.click(screen.getByRole("button", { name: "Revoke" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("no such grant");
      expect(screen.getByText("Claude Code", GRANT_CLIENT)).toBeInTheDocument();
    });
  });

});

describe("harness config", () => {
  it("saves the defaults as typed and chosen, then refetches the roster", async () => {
    const user = userEvent.setup();
    stubSideLists();
    const listAgents = vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    const save = vi
      .spyOn(apiClient, "saveHarnessConfigDefaults")
      .mockResolvedValue(makeHarnessConfigScope({ revision: 2 }));

    await renderSignedIn(<AgentsPanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit defaults" }));
    const form = screen.getByRole("form", { name: "Default harness config" });
    const saveButton = within(form).getByRole("button", { name: "Save" });
    expect(saveButton).toBeDisabled();

    await user.type(within(form).getByLabelText("Main model"), "sonnet");
    await chooseFromDropdown(user, "Effort", "high");
    await user.click(saveButton);

    expect(save).toHaveBeenCalledWith({ claude: { model: "sonnet", effort: "high" } });
    await vi.waitFor(() => expect(listAgents).toHaveBeenCalledTimes(2));
  });

  it("credits defaults saved by a deleted account to 'Deleted user'", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({
        defaults: makeHarnessConfigScope({ settings: { claude: { model: "sonnet" } }, updated_by: null, updated_by_deleted: true }),
      }),
    );
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);

    await renderSignedIn(<AgentsPanel />);

    expect(await screen.findByText(/saved by/)).toHaveTextContent("saved by Deleted user");
  });

  it("clearing a field unsets it", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({
        defaults: makeHarnessConfigScope({ settings: { claude: { model: "sonnet" }, pi: { provider: "anthropic" } } }),
      }),
    );
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    const save = vi.spyOn(apiClient, "saveHarnessConfigDefaults").mockResolvedValue(makeHarnessConfigScope());

    await renderSignedIn(<AgentsPanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit defaults" }));
    const form = screen.getByRole("form", { name: "Default harness config" });
    await user.clear(within(form).getByLabelText("Main model"));
    await user.click(within(form).getByRole("button", { name: "Save" }));

    expect(save).toHaveBeenCalledWith({ pi: { provider: "anthropic" } });
  });

  it("choosing the empty option on an enum unsets it", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({
        defaults: makeHarnessConfigScope({ settings: { claude: { model: "sonnet", effort: "high" } } }),
      }),
    );
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    const save = vi.spyOn(apiClient, "saveHarnessConfigDefaults").mockResolvedValue(makeHarnessConfigScope());

    await renderSignedIn(<AgentsPanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit defaults" }));
    const form = screen.getByRole("form", { name: "Default harness config" });
    expect(selectedValueOf("Effort")).toBe("high");
    await chooseFromDropdown(user, "Effort", "not set");
    await user.click(within(form).getByRole("button", { name: "Save" }));

    expect(save).toHaveBeenCalledWith({ claude: { model: "sonnet" } });
  });

  it("refuses a model id the server would refuse, and says why", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    const save = vi.spyOn(apiClient, "saveHarnessConfigDefaults");

    await renderSignedIn(<AgentsPanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit defaults" }));
    const form = screen.getByRole("form", { name: "Default harness config" });
    const model = within(form).getByLabelText("Main model");
    await user.type(model, "--help");

    expect(model).toHaveAttribute("aria-invalid", "true");
    expect(within(form).getByText(/starting with a letter or digit/)).toBeInTheDocument();
    expect(within(form).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(save).not.toHaveBeenCalled();
  });

  it("shows the server's refusal when a save fails", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
    vi.spyOn(apiClient, "saveHarnessConfigDefaults").mockRejectedValue(
      new ApiError(422, "harness config: invalid value for claude.model"),
    );

    await renderSignedIn(<AgentsPanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit defaults" }));
    const form = screen.getByRole("form", { name: "Default harness config" });
    await user.type(within(form).getByLabelText("Main model"), "sonnet");
    await user.click(within(form).getByRole("button", { name: "Save" }));

    expect(await within(form).findByRole("alert")).toHaveTextContent("invalid value for claude.model");
  });

  it("saves an agent's override on its own row, showing the defaults it would otherwise take", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({ defaults: makeHarnessConfigScope({ settings: { claude: { model: "sonnet" } } }) }),
    );
    const agent = makeAgent({ id: "11111111-1111-1111-1111-111111111111", name: "gpu-box" });
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([agent]);
    const save = vi
      .spyOn(apiClient, "saveAgentHarnessConfig")
      .mockResolvedValue(makeHarnessConfigScope({ agent_id: agent.id }));

    await renderSignedIn(<AgentsPanel />);
    const row = (await screen.findByText("gpu-box")).closest("li")!;
    await user.click(within(row).getByRole("button", { name: "Harness config" }));

    const form = within(row).getByRole("form", { name: "Harness config for gpu-box" });
    const model = within(form).getByLabelText("Main model");
    expect(model).toHaveAttribute("placeholder", "default (sonnet)");
    await user.type(model, "opus");
    await user.click(within(form).getByRole("button", { name: "Save" }));

    expect(save).toHaveBeenCalledWith(agent.id, { claude: { model: "opus" } });
    await vi.waitFor(() => expect(within(row).queryByRole("form")).not.toBeInTheDocument());
  });

  it("lets the steward's harness config be set from its own row", async () => {
    const user = userEvent.setup();
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
    const steward = makeAgent({ id: "22222222-2222-2222-2222-222222222222", name: "the-steward", kind: "steward" });
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([steward]);
    const save = vi
      .spyOn(apiClient, "saveAgentHarnessConfig")
      .mockResolvedValue(makeHarnessConfigScope({ agent_id: steward.id }));

    await renderSignedIn(<AgentsPanel />);
    const row = (await screen.findByText("the-steward")).closest("li")!;
    expect(within(row).queryByRole("button", { name: "Edit roles" })).not.toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "Harness config" }));
    const form = within(row).getByRole("form", { name: "Harness config for the-steward" });
    await user.type(within(form).getByLabelText("Main model"), "opus");
    await user.click(within(form).getByRole("button", { name: "Save" }));

    expect(save).toHaveBeenCalledWith(steward.id, { claude: { model: "opus" } });
  });

  it("marks a run on an older config as stale, one that never said as unknown, and an override", async () => {
    stubSideLists();
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(
      makeHarnessConfig({
        overrides: [
          makeHarnessConfigScope({
            agent_id: "33333333-3333-3333-3333-333333333333",
            settings: { claude: { model: "opus" } },
          }),
        ],
      }),
    );
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({
        id: "11111111-1111-1111-1111-111111111111",
        name: "behind",
        config_revision: 5,
        session: makeAgentSession({ config_revision: 3, config_status: "stale", bundle_hash: "0000", bundle_status: "stale" }),
      }),
      makeAgent({
        id: "22222222-2222-2222-2222-222222222222",
        name: "unsaid",
        config_revision: 5,
        session: makeAgentSession({ config_status: "unknown" }),
      }),
      makeAgent({
        id: "33333333-3333-3333-3333-333333333333",
        name: "current",
        config_revision: 5,
        session: makeAgentSession({ config_revision: 5, config_status: "current", bundle_hash: "1111", bundle_status: "current" }),
      }),
    ]);

    await renderSignedIn(<AgentsPanel />);
    const rowOf = (name: string) => screen.getByText(name).closest("li")!;
    await screen.findByText("behind");

    const stale = within(rowOf("behind")).getByText("stale config");
    expect(stale).toHaveAttribute("title", expect.stringContaining("revision 3; the current one is 5"));
    expect(within(rowOf("unsaid")).getByText("config unknown")).toBeInTheDocument();
    expect(within(rowOf("current")).queryByText(/stale config|config unknown/)).not.toBeInTheDocument();
    expect(within(rowOf("behind")).getByText("stale scripts")).toHaveAttribute("title", expect.stringContaining("0000"));
    expect(within(rowOf("unsaid")).queryByText("stale scripts")).not.toBeInTheDocument();
    expect(within(rowOf("current")).queryByText("stale scripts")).not.toBeInTheDocument();
    expect(await within(rowOf("current")).findByText("config override")).toBeInTheDocument();
    expect(within(rowOf("behind")).queryByText("config override")).not.toBeInTheDocument();
  });
});

describe("AgentsPanel supervisor slots", () => {
  // Ages render from the clock, so pin it: a slow CI run must not drift 10m into 11m.
  const NOW = Date.parse("2026-01-01T12:00:00Z");
  const slot: SupervisorStatus["slots"][number] = {
    agent_id: "alpha",
    agent_name: "alpha",
    error_class: "",
    error_streak: 0,
    offline: false,
    pause_reason: "",
    reason: "unknown" as const,
    reason_detail: "",
    bundle_hash: "",
    bundle_status: "unknown" as const,
    paused_at: null,
    paused_long: false,
    phase: "running",
    phase_entered_at: new Date(NOW - 10 * 60_000).toISOString(),
    reported_at: new Date(NOW).toISOString(),
    stall_reason: "",
    stalled: false,
    ticket_id: 42,
  };
  const status = (slots: SupervisorStatus["slots"]) => ({ offline: false, claimable: 0, last_polled_at: null, slots, supervisors: [], current_bundle_hash: "", current_protocol: 0 });

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    stubSideLists();
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "builder-1" })]);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("lists each slot with phase, age, ticket and error class, highlighting only the stalled one", async () => {
    vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue(
      status([
        slot,
        { ...slot, agent_id: "beta", agent_name: "beta", phase: "backoff", ticket_id: null, error_class: "rate_limited", error_streak: 4, stalled: true, stall_reason: "repeating_error" },
      ]),
    );
    await renderSignedIn(<AgentsPanel />);
    const list = await screen.findByRole("list", { name: "Supervisor slots" });
    const [healthy, stalled] = within(list).getAllByRole("listitem");
    expect(healthy).toHaveTextContent(/alpha.*running for 10m.*#42/);
    expect(healthy).not.toHaveClass("supervisor-slots__slot--stalled");
    expect(within(healthy).queryByText(/Stalled/)).not.toBeInTheDocument();
    expect(stalled).toHaveClass("supervisor-slots__slot--stalled");
    expect(stalled).toHaveTextContent(/beta.*backoff for 10m.*error: rate_limited x4/);
    expect(stalled).toHaveTextContent("Stalled: stuck repeating the same error");
  });

  it("shows a paused slot prominently with its reason, since when, and the reason as text", async () => {
    const pausedAt = new Date(NOW - 12 * 60_000).toISOString();
    vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue(
      status([{ ...slot, phase: "paused", ticket_id: null, paused_at: pausedAt, pause_reason: "push refused <img src=x onerror=alert(1)>" }]),
    );
    await renderSignedIn(<AgentsPanel />);
    const list = await screen.findByRole("list", { name: "Supervisor slots" });
    const [item] = within(list).getAllByRole("listitem");
    expect(item).toHaveClass("supervisor-slots__slot--paused");
    expect(item).toHaveTextContent(/Paused for 12m.*push refused <img src=x onerror=alert\(1\)>/);
    expect(item.querySelector("img")).toBeNull();
  });

  it("renders nothing extra when there are no slots", async () => {
    await renderSignedIn(<AgentsPanel />);
    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByRole("list", { name: "Supervisor slots" })).not.toBeInTheDocument();
  });

  it("keeps the page working when the status cannot be fetched", async () => {
    vi.spyOn(apiClient, "getSupervisorStatus").mockRejectedValue(new ApiError(500, "boom"));
    await renderSignedIn(<AgentsPanel />);
    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByRole("list", { name: "Supervisor slots" })).not.toBeInTheDocument();
  });
});
