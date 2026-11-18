import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError, TICKET_PROCESSING_ENABLED, type Setting } from "@/api";
import { stubApi } from "@/test/story_harness";
import ProcessingToggle from "./processing_toggle";

function makeSetting(enabled: boolean): Setting {
  return {
    name: TICKET_PROCESSING_ENABLED,
    type: "bool",
    string_val: null,
    int_val: null,
    float_val: null,
    bool_val: enabled,
    is_default: false,
    updated_at: null,
  };
}

const meta = {
  title: "Layout/ProcessingToggle",
  component: ProcessingToggle,
} satisfies Meta<typeof ProcessingToggle>;

export default meta;
type Story = StoryObj<typeof meta>;

export const On: Story = {
  beforeEach: () => stubApi({ getSetting: fn(async () => makeSetting(true)) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const toggle = await canvas.findByRole("switch", { name: "Ticket processing" });
    await expect(apiClient.getSetting).toHaveBeenCalledWith(TICKET_PROCESSING_ENABLED);
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect(toggle).toHaveTextContent("Processing");
    await expect(toggle).toBeEnabled();
    await expect(canvas.queryByText(/agents finish current work/)).toBeNull();
  },
};

export const Off: Story = {
  beforeEach: () => stubApi({ getSetting: async () => makeSetting(false) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const toggle = await canvas.findByRole("switch", { name: "Ticket processing" });
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    await expect(toggle).toHaveTextContent("Paused");
    await expect(canvas.getByText("agents finish current work, then stop")).toBeInTheDocument();
  },
};

export const TurnOff: Story = {
  beforeEach: () =>
    stubApi({
      getSetting: async () => makeSetting(true),
      updateSetting: fn(async () => makeSetting(false)),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Ticket processing" }));

    await expect(apiClient.updateSetting).toHaveBeenCalledTimes(1);
    await expect(apiClient.updateSetting).toHaveBeenCalledWith(TICKET_PROCESSING_ENABLED, {
      type: "bool",
      bool_val: false,
    });
    await expect(await canvas.findByText("Paused")).toBeVisible();
    await expect(canvas.getByRole("switch")).toHaveAttribute("aria-checked", "false");
  },
};

export const TurnOn: Story = {
  beforeEach: () =>
    stubApi({
      getSetting: async () => makeSetting(false),
      updateSetting: fn(async () => makeSetting(true)),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Ticket processing" }));

    await expect(apiClient.updateSetting).toHaveBeenCalledWith(TICKET_PROCESSING_ENABLED, {
      type: "bool",
      bool_val: true,
    });
    await expect(await canvas.findByText("Processing")).toBeVisible();
    await expect(canvas.getByRole("switch")).toHaveAttribute("aria-checked", "true");
    await expect(canvas.queryByText(/agents finish current work/)).toBeNull();
  },
};

// The write never settles, so the busy state can be asserted and looked at.
export const Switching: Story = {
  beforeEach: () =>
    stubApi({
      getSetting: async () => makeSetting(true),
      updateSetting: fn(() => new Promise<Setting>(() => {})),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Ticket processing" }));

    const toggle = canvas.getByRole("switch");
    await expect(toggle).toBeDisabled();
    // Keeps showing the value it is moving away from until the write lands.
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect(apiClient.updateSetting).toHaveBeenCalledTimes(1);
  },
};

export const FailedSwitch: Story = {
  beforeEach: () =>
    stubApi({
      getSetting: async () => makeSetting(true),
      updateSetting: fn(async () => {
        throw new ApiError(500, "Could not save the processing switch.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Ticket processing" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("Could not save the processing switch.");
    const toggle = canvas.getByRole("switch");
    // Back on the value it came from, and usable for a retry.
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect(toggle).toBeEnabled();
  },
};

export const LoadError: Story = {
  beforeEach: () =>
    stubApi({
      getSetting: async () => {
        throw new ApiError(500, "Processing switch is unavailable.");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Processing switch is unavailable.");
    await expect(canvas.queryByRole("switch")).toBeNull();
  },
};
