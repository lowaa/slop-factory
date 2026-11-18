import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { stubApi } from "@/test/story_harness";
import { makeCompareResult } from "./compare_fixtures";
import ComparePage from "./compare_page";
import CompareResults from "./compare_results";

const variants = [{ id: "v1", name: "Careful", version: 1, harness: "claude" as const }];

const meta = {
  title: "Panels/ComparePage",
  component: ComparePage,
  beforeEach: () => stubApi({ listVariantOptions: async () => variants }),
} satisfies Meta<typeof ComparePage>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Enter a replay batch id.")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Compare" })).toBeDisabled();
  },
};

export const Loading: Story = {
  beforeEach: () =>
    stubApi({ listVariantOptions: async () => variants, compareVariants: () => new Promise(() => {}) }),
  play: async ({ canvasElement, userEvent }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("checkbox", { name: "Careful" }));
    await userEvent.type(canvas.getByLabelText("Replay batch id"), "0b8a2d6e-3f4c-4b0e-9a55-2f1d1d6f4c11");
    await userEvent.click(canvas.getByRole("button", { name: "Compare" }));
    await expect(await canvas.findByText("Comparing…")).toBeVisible();
  },
};

export const Result: Story = {
  render: () => <CompareResults result={makeCompareResult(8)} />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("table", { name: "Cost" })).toBeVisible();
    await expect(canvas.queryByTestId("small-n")).toBeNull();
  },
};

export const SmallN: Story = {
  render: () => <CompareResults result={makeCompareResult(3)} />,
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByTestId("small-n")).toHaveTextContent("3 tickets: too few to trust a difference");
  },
};
