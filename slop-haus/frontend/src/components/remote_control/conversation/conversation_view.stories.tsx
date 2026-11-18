import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import type { HeldScopeOverlap } from "@/api";
import {
  makeConductor,
  makeConversation,
  makeConversationDetail,
  makeConversationEvent,
} from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { recentEventsOf } from "@/test/recent_events";
import "@/app.scss";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import ConversationView from "./conversation_view";

const conductor = makeConductor({ id: "c-1", name: "my-laptop" });
const SESSION = "77777777-7777-4777-8777-777777777777";

const overlaps: HeldScopeOverlap[] = Array.from({ length: 300 }, (_, i) => ({
  ticket_id: 100 + i,
  title: `Held work ${i}`,
  status: "in_progress",
  path: `pkg/file_${i}.go`,
  held: "pkg/",
}));
const changed = Array.from({ length: 200 }, (_, i) => `pkg/file_${i}.go`);
const repo = {
  branch: "ticket-9-x",
  head: "1234567890abcdef",
  dirty: true,
  dirty_count: 832,
  upstream: "origin/ticket-9-x",
  ahead: 1,
  behind: 3,
  commits: [],
  pushes: [],
  changed_paths: changed,
  changed_total: 832,
};
const history = [
  makeConversationEvent({ seq: 1, kind: "user", body: "hello" }),
  makeConversationEvent({ seq: 2, kind: "assistant", turn: 1, body: "hi" }),
  makeConversationEvent({ seq: 3, kind: "turn_end", turn: 1, body: "" }),
];

const meta = {
  title: "RemoteControl/ConversationView",
  component: ConversationView,
  args: { conductor, conversationId: SESSION },
  decorators: [
    (Story) => (
      <div
        style={{ height: "100dvh", display: "flex", flexDirection: "column" }}
      >
        <UserSettingsProvider>
          <Story />
        </UserSettingsProvider>
      </div>
    ),
  ],
  parameters: {
    layout: "fullscreen",
    harness: { initialPath: "/remote-control" },
  },
  beforeEach: () =>
    stubApi({
      getConversation: async () =>
        makeConversationDetail({
          id: SESSION,
          state: "ready",
          repo,
          scope_overlaps: overlaps,
        }),
      listConversations: async () => [
        makeConversation({ id: SESSION, state: "ready" }),
      ],
      listRecentConversationEvents: recentEventsOf(history),
      streamConversation: () => () => {},
      listMySettings: async () => [],
    }),
} satisfies Meta<typeof ConversationView>;

export default meta;
type Story = StoryObj<typeof meta>;

async function expectTranscriptKeepsMostOfTheScreen(
  canvasElement: HTMLElement,
) {
  const transcript = await within(canvasElement).findByRole("list", {
    name: "Transcript",
  });
  await waitFor(() =>
    expect(transcript.getBoundingClientRect().height).toBeGreaterThan(
      window.innerHeight * 0.3,
    ),
  );
}

// Hundreds of overlaps and a long changed-paths list, both expanded.
export const HeavyHeader: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("list", { name: "Transcript" });
    await userEvent.click(
      canvas.getByText("832 paths changed by this session"),
    );
    await userEvent.click(
      canvas.getByText(/This session changed paths that 300 agents hold/),
    );
    await expectTranscriptKeepsMostOfTheScreen(canvasElement);
    const header = canvasElement.querySelector(".conversation__header")!;
    await expect(header.scrollHeight).toBeGreaterThan(header.clientHeight);
  },
};

export const HeavyHeaderOnAPhone: Story = {
  args: { onBack: fn() },
  parameters: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    await expectTranscriptKeepsMostOfTheScreen(canvasElement);
  },
};

// The saved setting is off: messages show as typed, each with a copy button.
export const PlainMessages: Story = {
  beforeEach: () =>
    stubApi({
      listMySettings: async () => [{ name: "render_markdown", type: "bool", bool_val: false, is_default: false } as never],
    }),
  play: async ({ canvasElement }) => {
    const transcript = await within(canvasElement).findByRole("list", { name: "Transcript" });
    await waitFor(() => expect(within(transcript).getAllByRole("button", { name: "Copy" }).length).toBeGreaterThan(0));
  },
};

const replying = [
  makeConversationEvent({ seq: 1, kind: "user", body: "run the tests" }),
  makeConversationEvent({ seq: 2, kind: "assistant", body: "Running them now" }),
];

const stubEvents = (events: ReturnType<typeof makeConversationEvent>[], detail: Parameters<typeof makeConversationDetail>[0] = {}) =>
  stubApi({
    listRecentConversationEvents: recentEventsOf(events),
    getConversation: async () => makeConversationDetail({ id: SESSION, state: "ready", ...detail }),
  });

export const Replying: Story = {
  beforeEach: () => stubEvents(replying),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Stop this reply" })).toBeEnabled();
    await expect(canvas.getByText(/^Working…/)).toBeVisible();
  },
};

export const StopRequested: Story = {
  beforeEach: () => stubEvents([...replying, makeConversationEvent({ seq: 3, kind: "interrupt_requested", turn: 1 })]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Stopping…" })).toBeDisabled();
    await expect(canvas.getAllByText("Stopping…")).toHaveLength(2);
    await expect(canvas.queryByText(/^Working…/)).toBeNull();
  },
};

export const StoppedByYou: Story = {
  beforeEach: () =>
    stubEvents([
      ...replying,
      makeConversationEvent({ seq: 3, kind: "interrupt_requested", turn: 1 }),
      makeConversationEvent({ seq: 4, kind: "turn_end", turn: 1 }),
      makeConversationEvent({ seq: 5, kind: "interrupted", turn: 1 }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Stopped by you.")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: /Stop/ })).toBeNull();
  },
};

export const StopWithHostOffline: Story = {
  beforeEach: () => stubEvents(replying, { host_online: false }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Stop this reply" })).toBeDisabled();
    await expect(canvas.getAllByText("The host isn't running").length).toBeGreaterThan(0);
  },
};
