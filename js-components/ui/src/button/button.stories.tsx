import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import Button from "./button";

const meta: Meta<typeof Button> = {
  title: "components/Button",
  component: Button,
  args: { children: "Save invoice" },
};

export default meta;
type Story = StoryObj<typeof Button>;

export const Primary: Story = { args: { variant: "primary" } };
export const Secondary: Story = { args: { variant: "secondary" } };
export const Tonal: Story = { args: { variant: "tonal" } };
export const TonalDisabled: Story = { args: { variant: "tonal", disabled: true } };
export const TonalFocused: Story = {
  args: { variant: "tonal" },
  play: async ({ canvasElement }) => {
    within(canvasElement).getByRole("button").focus();
  },
};
export const Ghost: Story = { args: { variant: "ghost" } };
export const Danger: Story = { args: { variant: "danger", children: "Delete" } };
export const Small: Story = { args: { size: "sm" } };
export const Disabled: Story = { args: { variant: "primary", disabled: true } };

// The hierarchy only reads correctly side by side: one filled action,
// everything else quieter.
export const Hierarchy: Story = {
  render: () => (
    <div style={{ display: "flex", gap: 8 }}>
      <Button variant="primary">Save</Button>
      <Button variant="tonal">Review</Button>
      <Button>Cancel</Button>
      <Button variant="ghost">More</Button>
      <Button variant="danger">Delete</Button>
    </div>
  ),
  // A real click, in a real browser, on the actual rendered button --
  // nothing jsdom-specific about it, but it's the one story here that
  // otherwise had no assertion behind it at all.
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const save = canvas.getByRole("button", { name: "Save" });

    expect(save).toHaveClass("button--primary");
    expect(canvas.getByRole("button", { name: "Delete" })).toHaveClass("button--danger");

    let clicked = false;
    save.addEventListener("click", () => (clicked = true));
    await userEvent.click(save);

    expect(clicked).toBe(true);
  },
};
