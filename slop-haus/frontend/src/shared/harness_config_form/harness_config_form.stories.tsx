import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeHarnessConfig } from "@/test/fixtures";
import { chooseOption } from "../../test/choose_option";
import HarnessConfigForm from "./harness_config_form";
import { expectPhoneDropdowns } from "../../test/phone_dropdown_checks";

const meta = {
  title: "Agents/HarnessConfigForm",
  component: HarnessConfigForm,
  args: {
    definitions: makeHarnessConfig().definitions,
    saved: undefined,
    label: "Harness config",
    onSave: fn(async () => {}),
    onCancel: fn(),
  },
  beforeEach: ({ args }) => {
    (args.onSave as ReturnType<typeof fn>).mockClear();
  },
} satisfies Meta<typeof HarnessConfigForm>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Defaults: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("group", { name: "Claude Code" })).toBeInTheDocument();
    await expect(canvas.getByRole("group", { name: "pi" })).toBeInTheDocument();
    const model = canvas.getByRole("combobox", { name: "Main model" });
    await expect(model).toHaveValue("");
    await expect(model).toHaveAttribute("placeholder", "not set");
    await expect(model).toHaveAttribute("aria-invalid", "false");
    await expect(canvas.getByRole("combobox", { name: "Effort" })).toHaveTextContent("not set");
    await expect(canvas.getByRole("button", { name: "Save" })).toBeDisabled();
  },
};

export const ValuesSet: Story = {
  args: {
    saved: { claude: { model: "opus", effort: "high" }, pi: { provider: "openrouter" } },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("combobox", { name: "Main model" })).toHaveValue("opus");
    await expect(canvas.getByRole("combobox", { name: "Effort" })).toHaveTextContent("high");
    await expect(canvas.getByRole("textbox", { name: "Provider" })).toHaveValue("openrouter");
    await expect(canvas.getByRole("combobox", { name: "Subagent model" })).toHaveValue("");
  },
};

// An agent's override shows what it inherits where it sets nothing.
export const Inherited: Story = {
  args: {
    saved: { claude: { effort: "low" } },
    inherited: { claude: { model: "sonnet", effort: "high" }, pi: { provider: "openrouter" } },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("combobox", { name: "Main model" })).toHaveAttribute(
      "placeholder",
      "default (sonnet)",
    );
    await expect(canvas.getByRole("textbox", { name: "Provider" })).toHaveAttribute(
      "placeholder",
      "default (openrouter)",
    );
    await expect(canvas.getByRole("combobox", { name: "Effort" })).toHaveTextContent("low");
    await userEvent.click(canvas.getByRole("combobox", { name: "Effort" }));
    await expect(await canvas.findByRole("option", { name: "default (high)" })).toBeInTheDocument();
  },
};

export const InvalidInput: Story = {
  args: { saved: { claude: { model: "-bad model" } } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const model = canvas.getByRole("combobox", { name: "Main model" });
    await expect(model).toHaveAttribute("aria-invalid", "true");
    await expect(canvas.getByText(/letters, digits and/)).toBeInTheDocument();
    // The error text takes the help's place.
    await expect(canvas.queryByText("The main session's model.")).not.toBeInTheDocument();
    // Another field being edited cannot save while one is invalid.
    await userEvent.type(canvas.getByRole("textbox", { name: "Provider" }), "openrouter");
    await expect(canvas.getByRole("button", { name: "Save" })).toBeDisabled();
    await expect(args.onSave).not.toHaveBeenCalled();
  },
};

export const FixingInvalidInput: Story = {
  args: { saved: { claude: { model: "-bad" } } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const model = canvas.getByRole("combobox", { name: "Main model" });
    await userEvent.clear(model);
    await userEvent.type(model, "haiku");
    await expect(model).toHaveAttribute("aria-invalid", "false");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(args.onSave).toHaveBeenCalledWith({ claude: { model: "haiku" } });
  },
};

export const SaveError: Story = {
  args: {
    onSave: fn(async () => {
      throw new ApiError(409, "Someone else changed this config.");
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await chooseOption(canvasElement, "Effort", "max");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Someone else changed this config.");
    await expect(canvas.getByRole("combobox", { name: "Effort" })).toHaveTextContent("max");
  },
};

// The enum dropdown sits in the same grid as the text inputs: as wide as
// them, finger-sized, with lists that stay on screen.
export const Phone: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const provider = canvas.getByRole("textbox", { name: "Provider" }).getBoundingClientRect().width;
    const effort = canvas.getByRole("combobox", { name: "Effort" }).getBoundingClientRect().width;
    await expect(effort).toBeCloseTo(provider, 0);
    await expectPhoneDropdowns(canvasElement, 1);
  },
};
