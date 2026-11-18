import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { stubApi } from "@/test/story_harness";
import { makeBlockingGraph, makeGraphEdge, makeGraphNode, makeTicket } from "@/test/fixtures";
import BlockingGraphView from "./blocking_graph";
import { BLOCKED_FILTER_MODEL, DEFAULT_BLOCKED_VALUES } from "./blocked_filter";

const meta = {
  title: "Panels/BlockingGraph",
  component: BlockingGraphView,
  args: { values: DEFAULT_BLOCKED_VALUES, onFilterChange: () => {} },
} satisfies Meta<typeof BlockingGraphView>;

export default meta;
type Story = StoryObj<typeof meta>;

const tickets = [
  makeTicket({ id: 2, title: "Build the API", status: "blocked", unblocks: 3 }),
  makeTicket({ id: 3, title: "Build the UI", status: "blocked" }),
];

const chain = makeBlockingGraph({
  nodes: [
    makeGraphNode({ id: 1, title: "Design the schema", unblocks: 2, layer: 0 }),
    makeGraphNode({ id: 2, title: "Build the API", blocked: true, blocked_by: [1], layer: 1 }),
  ],
  edges: [makeGraphEdge({ blocker: 1, blocked: 2 })],
  layers: [[1], [2]],
});

export const Default: Story = {
  beforeEach: () => stubApi({ listBlockedPage: async () => ({ tickets }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Build the API")).toBeVisible();
    await expect(canvas.getByText("unblocks 3")).toBeVisible();
    await expect(canvas.getByRole("group", { name: "Filters" })).toBeVisible();
  },
};

export const GraphOpened: Story = {
  beforeEach: () => stubApi({ listBlockedPage: async () => ({ tickets }), getTicketBlockers: async () => chain }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Show graph for #2" }));
    await expect(await canvas.findByText("Design the schema")).toBeVisible();
  },
};

// A parked prerequisite is faded in the graph, so the ticket waiting on it
// does not look free to start.
export const HiddenBlockerMarker: Story = {
  beforeEach: () =>
    stubApi({
      listBlockedPage: async () => ({ tickets }),
      getTicketBlockers: async () =>
        makeBlockingGraph({
          nodes: [
            makeGraphNode({ id: 1, title: "Parked prerequisite", status: "parked", hidden: true, unblocks: 1, layer: 0 }),
            makeGraphNode({ id: 2, title: "Waiting on it", blocked: true, blocked_by: [1], layer: 1 }),
          ],
          edges: [makeGraphEdge({ blocker: 1, blocked: 2 })],
          layers: [[1], [2]],
        }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Show graph for #2" }));
    await expect(await canvas.findByText("hidden by filter, still blocking")).toBeVisible();
  },
};

export const MorePages: Story = {
  beforeEach: () =>
    stubApi({
      listBlockedPage: async ({ cursor }) =>
        cursor ? { tickets: [makeTicket({ id: 9, title: "Further down", status: "blocked" })] } : { tickets, nextCursor: "c2" },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Load more" }));
    await expect(await canvas.findByText("Further down")).toBeVisible();
  },
};

export const FilteredNothing: Story = {
  args: { values: BLOCKED_FILTER_MODEL.fromSearch({ q: "zebra" }) },
  beforeEach: () => stubApi({ listBlockedPage: async () => ({ tickets: [] }) }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText("No tickets match these filters.")).toBeVisible();
  },
};

export const ListFails: Story = {
  beforeEach: () =>
    stubApi({
      listBlockedPage: async () => {
        throw new Error("down");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Try again" })).toBeEnabled();
  },
};

export const GraphFails: Story = {
  beforeEach: () =>
    stubApi({
      listBlockedPage: async () => ({ tickets }),
      getTicketBlockers: async () => {
        throw new Error("down");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Show graph for #2" }));
    await expect(await canvas.findByRole("alert")).toBeVisible();
  },
};
