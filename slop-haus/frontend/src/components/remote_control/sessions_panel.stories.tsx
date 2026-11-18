import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { makeConductor, makeConversation } from "@/test/fixtures";
import SessionsPanel from "./sessions_panel";
import type { Machine } from "@/lib/use_machines";

const laptop: Machine = {
  conductor: makeConductor({ id: "c-laptop", name: "laptop" }),
  online: true,
  sessions: [
    makeConversation({ id: "s-1", conductor_id: "c-laptop", state: "ready", updated_at: "2026-10-07T09:00:00Z" }),
    makeConversation({ id: "s-2", conductor_id: "c-laptop", state: "stopped", updated_at: "2026-10-06T09:00:00Z" }),
    makeConversation({ id: "s-3", conductor_id: "c-laptop", state: "failed", updated_at: "2026-10-05T09:00:00Z" }),
  ],
};
const server: Machine = {
  conductor: makeConductor({ id: "c-server", name: "build-server" }),
  online: false,
  sessions: [makeConversation({ id: "s-4", conductor_id: "c-server", state: "stopped" })],
};

const meta = {
  title: "RemoteControl/SessionsPanel",
  component: SessionsPanel,
  args: {
    state: { status: "loaded", removed: [], machines: [laptop, server] },
    open: true,
    onToggle: fn(),
    sessionId: undefined,
    newOn: undefined,
    onOpenSession: fn(),
    onNewSession: fn(),
  },
} satisfies Meta<typeof SessionsPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoSessions: Story = {
  args: { state: { status: "loaded", removed: [], machines: [{ ...laptop, sessions: [] }] } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const group = within(canvas.getByRole("region", { name: "laptop" }));
    // + New session, and the ⋯ menu.
    await expect(group.getAllByRole("button")).toHaveLength(2);
    await expect(canvas.queryByRole("button", { current: true })).not.toBeInTheDocument();
    await userEvent.click(group.getByRole("button", { name: "+ New session" }));
    await expect(args.onNewSession).toHaveBeenCalledWith("c-laptop");
  },
};

const named = (sessions: Machine["sessions"]): Story["args"] => ({ state: { status: "loaded", removed: [], machines: [{ ...laptop, sessions }] } });

export const ShortName: Story = {
  args: named([makeConversation({ id: "s-1", conductor_id: "c-laptop", first_line: "fix the bug" })]),
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("fix the bug")).toBeVisible();
  },
};

export const LongName: Story = {
  // The page gives the panel a 260px column; this is that column.
  decorators: [(Story) => (<div style={{ display: "flex", width: 260 }}><Story /></div>)],
  args: named([
    makeConversation({ id: "s-1", conductor_id: "c-laptop", first_line: "refactor the whole of the importer so that it streams rows instead of loading every one of them into memory first" }),
  ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const name = canvas.getByText(/^refactor the whole/);
    // One line cut with an ellipsis, and the panel as wide as ever.
    await expect(name.scrollWidth).toBeGreaterThan(name.clientWidth);
    await expect(canvas.getByRole("complementary").getBoundingClientRect().width).toBeLessThanOrEqual(260);
  },
};

export const NoFirstLine: Story = {
  args: named([makeConversation({ id: "s-1", conductor_id: "c-laptop" })]),
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("Session")).toBeVisible();
  },
};

export const CustomName: Story = {
  args: named([makeConversation({ id: "s-1", conductor_id: "c-laptop", title: "Importer rewrite", first_line: "fix the bug" })]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Importer rewrite")).toBeVisible();
    await expect(canvas.queryByText("fix the bug")).not.toBeInTheDocument();
  },
};

export const NoRemoteControl: Story = {
  args: { state: { status: "loaded", removed: [], machines: [] } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("You have no remote control yet. Add one above.")).toBeVisible();
    await expect(canvas.queryByRole("region")).not.toBeInTheDocument();
  },
};

export const Loading: Story = {
  args: { state: { status: "loading" } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("Loading sessions…")).toBeVisible();
  },
};

export const LoadFailed: Story = {
  args: { state: { status: "error", message: "Could not reach the server." } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("alert")).toHaveTextContent("Could not reach the server.");
  },
};

// A machine whose sessions could not be read keeps its New session button and
// says so, rather than showing an empty list that claims there are none.
export const SessionsUnreadable: Story = {
  args: { state: { status: "loaded", removed: [], machines: [{ ...laptop, online: null, sessions: null }] } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("status")).toHaveTextContent("Could not read this machine's sessions.");
    await expect(canvas.getByRole("button", { name: "+ New session" })).toBeEnabled();
    await expect(canvas.queryByText("Online")).not.toBeInTheDocument();
    await expect(canvas.queryByText("Offline")).not.toBeInTheDocument();
  },
};

// A session still recorded as ready on an offline machine has no process: it
// reads Offline, not Connected.
export const LiveSessionOnOfflineMachine: Story = {
  args: {
    state: {
      status: "loaded",
      removed: [],
      machines: [{ ...server, sessions: [makeConversation({ id: "s-5", conductor_id: "c-server", state: "ready" })] }],
    },
  },
  play: async ({ canvasElement }) => {
    const group = within(within(canvasElement).getByRole("region", { name: "build-server" }));
    await expect(group.getByRole("button", { name: /Offline/ })).toBeVisible();
    await expect(group.queryByText("Connected")).not.toBeInTheDocument();
  },
};

export const SeveralWithOneCurrent: Story = {
  args: { sessionId: "s-2" },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const group = within(canvas.getByRole("region", { name: "laptop" }));
    await expect(group.getAllByRole("button")).toHaveLength(5);
    const current = group.getByRole("button", { current: true });
    await expect(current).toHaveTextContent("Disconnected");
    await expect(canvas.getAllByRole("button", { current: true })).toHaveLength(1);
    await expect(group.getByRole("button", { name: /^Failed/ })).toBeInTheDocument();
    await expect(group.getByText("Online")).toBeInTheDocument();
    await expect(within(canvas.getByRole("region", { name: "build-server" })).getByText("Offline")).toBeInTheDocument();
    await userEvent.click(group.getByRole("button", { name: /Connected/ }));
    await expect(args.onOpenSession).toHaveBeenCalledWith("c-laptop", "s-1");
  },
};

export const AgentMenuOpen: Story = {
  play: async ({ canvasElement }) => {
    const group = within(within(canvasElement).getByRole("region", { name: "laptop" }));
    await userEvent.click(group.getByRole("button", { name: "Manage laptop" }));
    for (const name of ["Edit config", "New setup script", "Disable", "Remove"]) {
      await expect(group.getByRole("button", { name })).toBeVisible();
    }
    await userEvent.click(group.getByRole("button", { name: "Remove" }));
    await expect(group.getByRole("group", { name: "Confirm removing laptop" })).toBeVisible();
  },
};

// A disabled one keeps its heading to be enabled again, with no sessions; a
// removed one is behind the toggle.
export const DisabledAndRemoved: Story = {
  args: {
    state: {
      status: "loaded",
      machines: [{ conductor: makeConductor({ id: "c-off", name: "spare", disabled_at: "2026-01-02T00:00:00Z" }), online: null, sessions: [] }],
      removed: [makeConductor({ id: "c-gone", name: "old-box", disabled_at: "2026-01-02T00:00:00Z", archived_at: "2026-01-03T00:00:00Z" })],
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const group = within(canvas.getByRole("region", { name: "spare" }));
    await expect(group.getByText("Disabled")).toBeVisible();
    await expect(group.queryByRole("button", { name: "+ New session" })).not.toBeInTheDocument();
    await expect(canvas.queryByText("old-box")).not.toBeInTheDocument();
    await userEvent.click(canvas.getByRole("checkbox", { name: "Show removed (1)" }));
    await expect(canvas.getByRole("button", { name: "Restore" })).toBeVisible();
  },
};

export const NewSessionOpen: Story = {
  args: { newOn: "c-server" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const open = canvas.getByRole("button", { current: true });
    await expect(open).toHaveTextContent("+ New session");
    await expect(within(canvas.getByRole("region", { name: "build-server" })).getByRole("button", { current: true })).toBe(open);
  },
};

export const Collapsed: Story = {
  args: { open: false },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const toggle = canvas.getByRole("button", { name: "Show sessions" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(canvas.queryByRole("region")).not.toBeInTheDocument();
    await userEvent.click(toggle);
    await expect(args.onToggle).toHaveBeenCalledWith(true);
  },
};

export const PhoneWidth: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  args: { sessionId: "s-1" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    for (const b of canvas.getAllByRole("button", { name: /^(\+ New session|Connected|Disconnected|Failed)/ })) {
      await expect(b.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    }
  },
};

// The setup and running entries sit under the toggle on desktop; the open one
// is marked current.
export const WithViewEntries: Story = {
  args: { view: "running", onOpenView: fn() },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const nav = canvas.getByRole("navigation", { name: "Remote control help" });
    await expect(within(nav).getByRole("button", { name: "Running a remote control" })).toHaveAttribute("aria-current", "page");
    await expect(within(nav).getByRole("button", { name: "Add a remote control" })).not.toHaveAttribute("aria-current");
    await userEvent.click(within(nav).getByRole("button", { name: "Add a remote control" }));
    await expect(args.onOpenView).toHaveBeenCalledWith("setup");
  },
};

export const ViewEntriesHiddenWhenCollapsed: Story = {
  args: { open: false, onOpenView: fn() },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByRole("navigation", { name: "Remote control help" })).not.toBeInTheDocument();
  },
};
