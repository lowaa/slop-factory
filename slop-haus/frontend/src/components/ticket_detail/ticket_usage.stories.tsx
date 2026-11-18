import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeUsageSums } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TicketUsage from "./ticket_usage";

const meta = {
  title: "Ticket detail/TicketUsage",
  component: TicketUsage,
  args: { ticketId: 7 },
  parameters: { layout: "padded" },
} satisfies Meta<typeof TicketUsage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const WorkerAndGuidance: Story = {
  beforeEach: () =>
    stubApi({
      getTicketLlmUsage: async () => ({
        worker: [
          makeUsageSums({ billing_mode: "metered", input_tokens: 120000, requests: 40, cost_micros: 2_400_000 }),
          makeUsageSums({ billing_mode: "subscription", input_tokens: 800000, requests: 90, cost_micros: 5_000_000 }),
        ],
        guidance: [makeUsageSums({ input_tokens: 30000, requests: 6, cost_micros: 300_000, unpriced_records: 1, unpriced_requests: 2 })],
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("heading", { name: "Worker" })).toBeInTheDocument();
    await expect(canvas.getByRole("heading", { name: "Guidance" })).toBeInTheDocument();
  },
};

export const Unpriced: Story = {
  beforeEach: () =>
    stubApi({
      getTicketLlmUsage: async () => ({
        worker: [makeUsageSums({ requests: 4, cost_micros: null, unpriced_records: 1, unpriced_requests: 4 })],
        guidance: [],
      }),
    }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText("unpriced")).toBeInTheDocument();
  },
};
