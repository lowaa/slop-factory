import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import RejectForm from "./reject_form";
import "./approvals_panel.scss";

const meta = {
  title: "Approvals/RejectForm",
  component: RejectForm,
  args: { busy: false, confirmLabel: "Reject", onConfirm: fn(), onCancel: fn() },
} satisfies Meta<typeof RejectForm>;

export default meta;
type Story = StoryObj<typeof meta>;

export const WithReason: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByRole("textbox"), "not now");
    await userEvent.click(canvas.getByRole("button", { name: "Reject" }));
    await expect(args.onConfirm).toHaveBeenCalledWith("not now");
  },
};
