import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import StewardDiscussionsSection from "./steward_discussions";
import "./steward_digest.scss";
import { discussions, EDITED_DRAFT, NO_DISCUSSIONS } from "./fixtures";

const meta = {
  title: "Steward/DiscussionAgreement",
  component: StewardDiscussionsSection,
} satisfies Meta<typeof StewardDiscussionsSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoDecisions: Story = {
  args: { discussions: NO_DISCUSSIONS },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByText("No decided discussions in this period."),
    ).toBeVisible();
  },
};

export const Mixed: Story = {
  args: { discussions: discussions() },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const labels = within(canvas.getByRole("table", { name: "Label" }));
    await expect(labels.getByRole("row", { name: /merge_continue/ })).toHaveTextContent("75%");
    await expect(labels.getByRole("row", { name: /yours/ })).toHaveTextContent("Warning");
  },
};

export const WithEditedDraft: Story = {
  args: {
    discussions: discussions({
      label_disagreements: [
        {
          category: "merge_continue",
          ticket_id: 9,
          attempt: 1,
          round: 1,
          label: "routine",
          how: "answered elsewhere",
          overridden: true,
          at: "2026-01-01T10:00:00Z",
        },
      ],
      answer_disagreements: [EDITED_DRAFT],
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText(/routine, answered another way/),
    ).toBeVisible();
    await expect(
      canvas.getByText("Draft: Continue with the merge."),
    ).toBeVisible();
    await expect(
      canvas.getByText("Posted: Continue, but wait for the migration first."),
    ).toBeVisible();
  },
};
