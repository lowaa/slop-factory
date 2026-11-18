import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { apiClient, ApiError, type BlockerCandidate, type Ticket } from "@/api";
import {
  makeBlockerCandidate,
  makeBlockerList,
  makeTicket,
} from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import Blockers from "./blockers";
import { useBlockers } from "./use_blockers";
import type { LoadedState, TicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 19;

// The real useBlockers over a store that holds just the ticket, so the
// stories exercise the component's actual API calls rather than hand-made
// actions. The store's other members are never reached by blockers.
function BlockersHarness({ initial }: { initial: Ticket }) {
  const [loaded, setLoaded] = useState<LoadedState>({
    status: "loaded",
    ticket: initial,
    comments: [],
    contexts: [],
    revisions: [],
  });
  const store: Pick<
    TicketStore,
    | "state"
    | "updateLoaded"
    | "beginFieldWrite"
    | "isCurrent"
    | "refreshComments"
  > = {
    state: loaded,
    updateLoaded: (update) => setLoaded(update),
    beginFieldWrite: () => 0,
    isCurrent: () => true,
    refreshComments: async () => {},
  };
  const blockers = useBlockers(store as TicketStore, TICKET_ID);
  return <Blockers ticket={loaded.ticket} blockers={blockers} />;
}

const CANDIDATES: BlockerCandidate[] = [
  makeBlockerCandidate({ id: 3, title: "Design the schema" }),
  makeBlockerCandidate({
    id: 7,
    title: "Stand up the API",
    status: "in_progress",
  }),
];

const BLOCKERS = [
  { id: 4, title: "Pick a queue", status: "open" as const },
  { id: 5, title: "Wait for the vendor", status: "parked" as const },
];

const meta = {
  title: "Ticket detail/Blockers",
  component: BlockersHarness,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof BlockersHarness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoBlockers: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () =>
    stubApi({ getBlockerCandidates: fn(async () => CANDIDATES) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText("Nothing is blocking this ticket."),
    ).toBeInTheDocument();
    await expect(
      await canvas.findByRole("combobox", { name: "Add blocker" }),
    ).toBeInTheDocument();
    await expect(apiClient.getBlockerCandidates).toHaveBeenCalledWith(
      TICKET_ID,
    );
    await expect(canvas.queryByRole("button", { name: "Remove" })).toBeNull();
  },
};

export const WithBlockers: Story = {
  args: {
    initial: makeTicket({
      id: TICKET_ID,
      status: "blocked",
      blockers: BLOCKERS,
    }),
  },
  beforeEach: () => stubApi({ getBlockerCandidates: async () => CANDIDATES }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText(/#4 Pick a queue/)).toBeInTheDocument();
    await expect(
      canvas.getByText(/#5 Wait for the vendor/),
    ).toBeInTheDocument();
    await expect(canvas.getByText(", parked")).toBeInTheDocument();
    await expect(
      canvas.getAllByRole("button", { name: "Remove" }),
    ).toHaveLength(2);
  },
};

export const RemoveBlocker: Story = {
  args: {
    initial: makeTicket({
      id: TICKET_ID,
      status: "blocked",
      blockers: BLOCKERS,
    }),
  },
  beforeEach: () =>
    stubApi({
      getBlockerCandidates: fn(async () => CANDIDATES),
      // The reply carries a blocker added elsewhere meanwhile: the list shown is the server's.
      removeBlocker: fn(async () =>
        makeBlockerList(BLOCKERS[1], {
          id: 6,
          title: "Added elsewhere",
          status: "open",
        }),
      ),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getAllByRole("button", { name: "Remove" })[0]);

    await expect(apiClient.removeBlocker).toHaveBeenCalledWith(TICKET_ID, 4);
    await waitFor(() =>
      expect(canvas.queryByText(/#4 Pick a queue/)).toBeNull(),
    );
    await expect(
      canvas.getByText(/#5 Wait for the vendor/),
    ).toBeInTheDocument();
    await expect(canvas.getByText(/#6 Added elsewhere/)).toBeInTheDocument();
    // The candidate list is refetched: the removed ticket may be offered again.
    await waitFor(() =>
      expect(apiClient.getBlockerCandidates).toHaveBeenCalledTimes(2),
    );
  },
};

export const AddBlocker: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () =>
    stubApi({
      getBlockerCandidates: async () => CANDIDATES,
      addBlocker: fn(async () =>
        makeBlockerList({
          id: 7,
          title: "Stand up the API",
          status: "in_progress",
        }),
      ),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByRole("combobox", { name: "Add blocker" }),
      "api",
    );
    // The panel is portalled out of the canvas, so look in the whole document.
    await userEvent.click(
      await within(document.body).findByRole("option", {
        name: /#7 Stand up the API/,
      }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Add" }));

    await expect(apiClient.addBlocker).toHaveBeenCalledWith(TICKET_ID, 7);
    await expect(
      await canvas.findByText(/#7 Stand up the API/),
    ).toBeInTheDocument();
    await expect(
      canvas.queryByText("Nothing is blocking this ticket."),
    ).toBeNull();
  },
};

export const AddWithoutChoosing: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () =>
    stubApi({
      getBlockerCandidates: async () => CANDIDATES,
      addBlocker: fn(async () => makeBlockerList()),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("combobox", { name: "Add blocker" });
    await userEvent.click(canvas.getByRole("button", { name: "Add" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "Choose a ticket.",
    );
    await expect(apiClient.addBlocker).not.toHaveBeenCalled();
  },
};

export const CycleRefused: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () =>
    stubApi({
      getBlockerCandidates: async () => CANDIDATES,
      addBlocker: async () => {
        throw new ApiError(
          422,
          "dependency would create a cycle: #3 already depends on #19",
        );
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByRole("combobox", { name: "Add blocker" }),
      "schema",
    );
    await userEvent.click(
      await within(document.body).findByRole("option", {
        name: /#3 Design the schema/,
      }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Add" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "dependency would create a cycle",
    );
    await expect(
      canvas.getByText("Nothing is blocking this ticket."),
    ).toBeInTheDocument();
  },
};

export const NothingToBlockOn: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () => stubApi({ getBlockerCandidates: async () => [] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(
        "No other ticket can block this one without creating a cycle.",
      ),
    ).toBeInTheDocument();
    await expect(canvas.queryByRole("combobox")).toBeNull();
  },
};

export const LoadingCandidates: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () =>
    stubApi({
      getBlockerCandidates: () => new Promise<BlockerCandidate[]>(() => {}),
    }),
  play: async ({ canvasElement }) => {
    await expect(
      await within(canvasElement).findByText("Loading tickets…"),
    ).toBeInTheDocument();
  },
};

export const LoadErrorRetry: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  beforeEach: () => {
    // Fails the first load only, so the retry is what recovers it.
    const getBlockerCandidates = fn(async () => CANDIDATES);
    getBlockerCandidates.mockRejectedValueOnce(new ApiError(500, "boom"));
    stubApi({ getBlockerCandidates });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(
        /Couldn't load the tickets this one can block on/,
      ),
    ).toBeInTheDocument();
    await expect(canvas.queryByRole("combobox")).toBeNull();

    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));

    await expect(
      await canvas.findByRole("combobox", { name: "Add blocker" }),
    ).toBeInTheDocument();
    await expect(apiClient.getBlockerCandidates).toHaveBeenCalledTimes(2);
    await expect(canvas.queryByText(/Couldn't load/)).toBeNull();
  },
};
