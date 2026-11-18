import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import LegalMarkdown from "./legal_markdown";
import { cookieTable, dataFlow, retention, subProcessors } from "./legal_table_fixtures";

const source = ["## Cookies", cookieTable, "## Sub-processors", subProcessors, "## Retention", retention, "## Data", dataFlow].join(
  "\n\n",
);

const meta: Meta<typeof LegalMarkdown> = {
  title: "privacy/LegalTable",
  component: LegalMarkdown,
  args: { source },
  // The tables are the generated ones, so what they look like is what users read.
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(canvas.getAllByRole("region")).toHaveLength(4);
  },
};

export default meta;
type Story = StoryObj<typeof LegalMarkdown>;

export const Desktop: Story = { globals: { viewport: { value: "desktop" } } };

export const Phone: Story = {
  decorators: [
    (Story) => (
      <div style={{ width: 375, padding: 16 }}>
        <Story />
      </div>
    ),
  ],
};

// Desktop viewport, narrow container: the layout follows the container (as in
// the consent screens), not the viewport.
export const NarrowContainer: Story = {
  decorators: [
    (Story) => (
      <div style={{ width: "30rem", padding: 16 }}>
        <Story />
      </div>
    ),
  ],
};

// Wide enough to show the table, too narrow for every column: the columns
// share what the container has, and what each can shrink to is set by where
// its words may break. With "overflow-wrap: anywhere" on the cells this
// screenshot changes, which is what the visual diff guards.
export const MidContainer: Story = {
  decorators: [
    (Story) => (
      <div style={{ width: "62rem", padding: 16 }}>
        <Story />
      </div>
    ),
  ],
};
