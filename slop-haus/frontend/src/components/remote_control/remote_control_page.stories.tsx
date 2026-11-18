import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import type { ConversationEvent } from "@/api";
import { makeConductor, makeConductorDirectoryEntry, makeTenant, makeConversation, makeConversationDetail, makeConversationEvent, makeHarnessConfig } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import "@/app.scss";
import RemoteControlPage from "./remote_control_page";
import { recentEventsOf } from "@/test/recent_events";

const laptop = makeConductor({ id: "c-laptop", name: "my-laptop" });
const SESSION = "55555555-5555-4555-8555-555555555555";
const OLDER = "66666666-6666-4666-8666-666666666666";

// A long chat: more than a phone screen holds.
const LONG_CHAT: ConversationEvent[] = Array.from({ length: 12 }, (_, i) => [
  makeConversationEvent({ seq: i * 3 + 1, kind: "user", body: `Question ${i + 1}: what does this change do to the sessions list?` }),
  makeConversationEvent({ seq: i * 3 + 2, kind: "assistant", turn: i + 1, body: `Answer ${i + 1}: ${"a long reply ".repeat(10)}` }),
  makeConversationEvent({ seq: i * 3 + 3, kind: "turn_end", turn: i + 1, body: "" }),
]).flat();

const meta = {
  title: "RemoteControl/RemoteControlPage",
  component: RemoteControlPage,
  args: { onSelect: fn() },
  // The app shell's height chain (index.scss's #root, app.scss's __main): the
  // page gets the window's height, and what does not fit scrolls in it.
  decorators: [
    (Story) => (
      <div style={{ height: "100dvh", display: "flex", flexDirection: "column" }}>
        <div className="app">
          <div className="app__header">slop-haus</div>
          {/* A div: the page has its own main. */}
          <div className="app__main">
            <Story />
          </div>
        </div>
      </div>
    ),
  ],
  parameters: { layout: "fullscreen", harness: { initialPath: "/remote-control" } },
  beforeEach: () =>
    stubApi({
      listConductors: async () => ({ own: [laptop], others: [] }),
      listOwnRemoteControlSupervisors: async () => ({ provisionFromBoard: false, supervisors: [] }),
      listGrants: async () => [],
      conductorDirectory: async () => [makeConductorDirectoryEntry({ id: laptop.id, name: laptop.name })],
      listTenants: async () => [makeTenant()],
      getConductorHost: async () => ({ online: true }),
      listConversations: async () => [
        makeConversation({ id: SESSION, conductor_id: laptop.id, state: "ready", updated_at: "2026-10-07T09:00:00Z" }),
        makeConversation({ id: OLDER, conductor_id: laptop.id, state: "stopped", updated_at: "2026-10-06T09:00:00Z" }),
      ],
      getHarnessConfig: async () => makeHarnessConfig(),
      getConversation: async (_c, id) => makeConversationDetail({ id, state: "ready" }),
      listRecentConversationEvents: recentEventsOf(LONG_CHAT),
      streamConversation: () => () => {},
      listMySettings: async () => [],
    }),
} satisfies Meta<typeof RemoteControlPage>;

export default meta;
type Story = StoryObj<typeof meta>;

const phone = { viewport: { value: "mobile1", isRotated: false } };

// The desktop layout: the list beside the chat, with the setup and help links
// at the top of the list.
export const Desktop: Story = {
  args: { conductorId: laptop.id, sessionId: SESSION },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("list", { name: "Transcript" });
    await expect(canvas.getByRole("region", { name: "my-laptop" })).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "Session" })).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "‹ Sessions" })).not.toBeInTheDocument();
    await expect(canvas.getByRole("textbox", { name: "Message" })).toHaveAttribute("rows", "3");
    await expect(canvas.getByRole("button", { name: "Add a remote control" })).not.toHaveAttribute("aria-current");
  },
};

export const SetupView: Story = {
  args: { view: "setup" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "Add a remote control" });
    await expect(canvas.getByRole("button", { name: "Add a remote control" })).toHaveAttribute("aria-current", "page");
    await expect(canvas.queryByRole("textbox", { name: "Message" })).not.toBeInTheDocument();
  },
};

export const RunningView: Story = {
  args: { view: "running" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "Running a remote control" });
    await expect(canvas.getByRole("button", { name: "Running a remote control" })).toHaveAttribute("aria-current", "page");
  },
};

export const TenantView: Story = {
  args: { view: "tenant" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "Remote controls in this tenant" });
    await expect(canvas.getByRole("button", { name: "Remote controls in this tenant" })).toHaveAttribute("aria-current", "page");
  },
};

export const NoRemoteControlLinks: Story = {
  beforeEach: () =>
    stubApi({
      listConductors: async () => ({ own: [], others: [] }),
      listGrants: async () => [],
      listMySettings: async () => [],
      listOwnRemoteControlSupervisors: async () => ({ provisionFromBoard: false, supervisors: [] }),
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "how to run one" }));
    await expect(args.onSelect).toHaveBeenCalledWith({ view: "running" });
    await expect(canvas.getByRole("button", { name: "add a remote control" })).toBeVisible();
  },
};

export const PhoneSessionList: Story = {
  globals: phone,
  args: { conductorId: laptop.id, isNew: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const group = await canvas.findByRole("region", { name: "my-laptop" });
    await expect(within(group).getByRole("button", { name: "+ New session" })).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Hide sessions" })).not.toBeInTheDocument();
    await expect(canvas.queryByRole("region", { name: "Session" })).not.toBeInTheDocument();
    await expect(canvas.getByText("Add a remote control", { selector: "summary" })).toBeVisible();
  },
};

export const PhoneChat: Story = {
  globals: phone,
  args: { conductorId: laptop.id, sessionId: SESSION },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const transcript = await canvas.findByRole("list", { name: "Transcript" });
    await expect(canvas.getByRole("button", { name: "‹ Sessions" })).toBeVisible();
    await expect(canvas.queryByText("Add a remote control", { selector: "summary" })).not.toBeInTheDocument();
    await expect(canvas.queryByRole("region", { name: "my-laptop" })).not.toBeInTheDocument();
    // The transcript has the most of the screen, and is what scrolls: the
    // composer's Attach row takes a line of it on a small phone.
    await expect(transcript.getBoundingClientRect().height).toBeGreaterThan(window.innerHeight * 0.4);
    await expect(transcript.scrollHeight).toBeGreaterThan(transcript.clientHeight);
    await expect(canvas.getByRole("textbox", { name: "Message" }).getBoundingClientRect().bottom).toBeLessThanOrEqual(window.innerHeight);
    const root = document.documentElement;
    await expect(root.scrollHeight).toBeLessThanOrEqual(root.clientHeight);
  },
};

export const PhoneChatMenuOpen: Story = {
  globals: phone,
  args: { conductorId: laptop.id, sessionId: SESSION },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("list", { name: "Transcript" });
    await userEvent.click(canvas.getByRole("button", { name: "Session menu" }));
    await expect(canvas.getByRole("button", { name: "Disconnect" })).toBeVisible();
    await expect(canvas.getByText("Host")).toBeVisible();
  },
};
