import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import SessionTitle from "./session_title";

const meta = {
  title: "RemoteControl/SessionTitle",
  component: SessionTitle,
  args: { name: "fix the bug", onSave: async () => {} },
} satisfies Meta<typeof SessionTitle>;

export default meta;
type Story = StoryObj<typeof meta>;

const save = (outcome: "ok" | "refused" | "pending") => async () => {
  if (outcome === "pending") return new Promise<void>(() => {});
  if (outcome === "refused") throw new ApiError(422, "The name must be at most 100 characters.");
};

export const Idle: Story = {
  args: {},
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("heading", { name: "fix the bug" })).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Rename session" })).toBeVisible();
  },
};

export const Editing: Story = {
  args: {},
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Rename session" }));
    await expect(canvas.getByRole("textbox", { name: "Session name" })).toHaveFocus();
  },
};

export const Pending: Story = {
  args: { onSave: save("pending") },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Rename session" }));
    await userEvent.type(canvas.getByRole("textbox", { name: "Session name" }), "!{Enter}");
    await expect(canvas.getByRole("textbox", { name: "Session name" })).toBeDisabled();
  },
};

export const Refused: Story = {
  args: { onSave: save("refused") },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Rename session" }));
    await userEvent.type(canvas.getByRole("textbox", { name: "Session name" }), "!{Enter}");
    await expect(await canvas.findByRole("alert")).toHaveTextContent("at most 100 characters");
    await expect(canvas.getByRole("textbox", { name: "Session name" })).toBeEnabled();
  },
};
