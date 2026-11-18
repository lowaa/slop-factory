import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import RunningHelp from "./running_help";

const meta = {
  title: "RemoteControl/RunningHelp",
  component: RunningHelp,
  parameters: { layout: "padded" },
} satisfies Meta<typeof RunningHelp>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Open: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Running a remote control", { selector: "summary" }));
    await expect(canvas.getByText(".slop-haus/run_remote_control.sh")).toBeInTheDocument();
  },
};

export const PiTab: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByText("Running a remote control", { selector: "summary" }));
    await userEvent.click(canvas.getByRole("tab", { name: "pi" }));
    await expect(canvas.getByText(".slop-haus/run_remote_control.sh --harness pi")).toBeInTheDocument();
  },
};

export const AsView: Story = {
  args: { asView: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("heading", { name: "Running a remote control" })).toBeVisible();
    await expect(canvasElement.querySelector("details")).toBeNull();
    await expect(canvas.getByText(".slop-haus/run_remote_control.sh")).toBeVisible();
  },
};
