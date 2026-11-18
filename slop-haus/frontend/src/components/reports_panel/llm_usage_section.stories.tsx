import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeLlmUsageReport, makeUsageSums } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import LlmUsageSection from "./llm_usage_section";
import { DEFAULT_USAGE_TICKETS_VALUES } from "./usage_tickets_filter";
import "./llm_usage.scss";
import "./reports_panel.scss";

const meta = {
  title: "Panels/ReportsPanel/LlmUsageSection",
  component: LlmUsageSection,
  args: { days: 7, tickets: { values: DEFAULT_USAGE_TICKETS_VALUES, onFilterChange: () => {}, viewportHeight: 600 } },
} satisfies Meta<typeof LlmUsageSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const OvercountedUsageLeftOut: Story = {
  beforeEach: () => {
    stubApi({
      getLlmUsageReport: async (query) =>
        makeLlmUsageReport({
          totals: [makeUsageSums({ transcripts: 1, requests: query.include_unreliable ? 500 : 5 })],
          unreliable_records: 3,
        }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/was over-counted and is left out/)).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("checkbox", { name: "Include over-counted usage" }));
    await expect(await canvas.findByText(/that was over-counted is included/)).toBeInTheDocument();
  },
};
