import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import EvalSetForm from "./eval_set_form";
import "./eval_sets.scss";

const meta = {
  title: "Eval sets/Form",
  component: EvalSetForm,
  args: { busy: false, error: null, onSave: fn(), onCancel: fn() },
} satisfies Meta<typeof EvalSetForm>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NewFromFilter: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Name"), "Logins");
    await userEvent.click(canvas.getByRole("button", { name: "Create set" }));
    await expect(args.onSave).toHaveBeenCalledWith({ name: "Logins", description: "", filter: { statuses: ["done"] } });
  },
};

export const NeedsAName: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Create set" }));
    await expect(canvas.getByRole("alert")).toHaveTextContent("Give the set a name.");
    await expect(args.onSave).not.toHaveBeenCalled();
  },
};

export const Refused: Story = {
  args: { error: "that name is taken" },
};
