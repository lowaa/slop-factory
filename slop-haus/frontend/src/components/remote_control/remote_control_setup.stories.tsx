import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError } from "@/api";
import { stubApi } from "@/test/story_harness";
import RemoteControlSetup from "./remote_control_setup";

// The section is closed until asked for, so every story opens it first.
async function open(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(canvas.getByText("Add a remote control", { selector: "summary" }));
  return canvas;
}

const meta = {
  title: "RemoteControl/RemoteControlSetup",
  component: RemoteControlSetup,
  parameters: { layout: "padded" },
  // No supervisor of the person's by default: running one by hand is the only way.
  beforeEach: () => stubApi({ listOwnRemoteControlSupervisors: async () => ({ provisionFromBoard: false, supervisors: [] }) }),
} satisfies Meta<typeof RemoteControlSetup>;

export default meta;
type Story = StoryObj<typeof meta>;

export const RegisterRemoteControl: Story = {
  beforeEach: () =>
    stubApi({
      registerConductor: fn(async () => ({ filename: "fake-setup-script.sh", script: "#!/bin/sh\n" })),
    }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await userEvent.type(canvas.getByLabelText("Remote control name"), "  fake-new-box ");
    await userEvent.click(canvas.getByRole("button", { name: "Register remote control" }));

    await expect(await canvas.findByText("bash fake-setup-script.sh")).toBeVisible();
    await expect(apiClient.registerConductor).toHaveBeenCalledTimes(1);
    await expect(apiClient.registerConductor).toHaveBeenCalledWith("fake-new-box");
    await expect(canvas.getByLabelText("Remote control name")).toHaveValue("");
  },
};

export const EmptyNameError: Story = {
  beforeEach: () => stubApi({ registerConductor: fn() }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await userEvent.type(canvas.getByLabelText("Remote control name"), "   ");
    await userEvent.click(canvas.getByRole("button", { name: "Register remote control" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("Give the remote control a name first.");
    await expect(apiClient.registerConductor).not.toHaveBeenCalled();
  },
};

export const RegisterError: Story = {
  beforeEach: () =>
    stubApi({
      registerConductor: fn(async () => {
        throw new ApiError(409, "A remote control with that name already exists.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await userEvent.type(canvas.getByLabelText("Remote control name"), "fake-laptop");
    await userEvent.click(canvas.getByRole("button", { name: "Register remote control" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("A remote control with that name already exists.");
    await expect(apiClient.registerConductor).toHaveBeenCalledWith("fake-laptop");
    // The name is kept so it can be corrected.
    await expect(canvas.getByLabelText("Remote control name")).toHaveValue("fake-laptop");
    await expect(canvas.getByRole("button", { name: "Register remote control" })).toBeEnabled();
  },
};

export const AsView: Story = {
  args: { asView: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("heading", { name: "Add a remote control" })).toBeVisible();
    // A view is not itself a closed section; only the by-hand fallback inside it is.
    await expect(canvasElement.querySelector("details.remote-control-setup")).toBeNull();
  },
};
