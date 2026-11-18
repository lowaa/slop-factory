import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import NewSourceForm from "./new_source_form";
import { INITIAL_FORM, type FormValues } from "./intake_form_values";
import { FIELD_DOCS } from "./intake_field_docs";
import { chooseOption } from "../../test/choose_option";

// Holds the form's values the way the panel does, so the controls respond.
function Harness(props: Pick<React.ComponentProps<typeof NewSourceForm>, "enabled" | "hmac" | "busy" | "onSubmit">) {
  const [form, setForm] = useState<FormValues>(INITIAL_FORM);
  return (
    <NewSourceForm
      {...props}
      form={form}
      onChange={(key, value) => setForm((current) => ({ ...current, [key]: value }))}
    />
  );
}

const meta = {
  title: "Panels/NewIntakeSourceForm",
  component: Harness,
  args: { enabled: true, hmac: true, busy: false, onSubmit: fn() },
} satisfies Meta<typeof Harness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    for (const title of ["Sender", "Authentication", "Mapping", "Where tickets land"]) {
      await expect(canvas.getByRole("group", { name: title })).toBeInTheDocument();
    }
    await expect(canvas.getByLabelText("Default status")).toBeDisabled();
  },
};

export const TooltipOnHover: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.hover(canvas.getByLabelText("Max priority"));
    await expect(await canvas.findByRole("tooltip")).toHaveTextContent(FIELD_DOCS.max_priority.tooltip);
  },
};

export const TrustedWithRoles: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await chooseOption(canvasElement, "Trust", "trusted");
    await expect(canvas.getByLabelText("Default status")).toBeEnabled();
    await userEvent.type(canvas.getByLabelText("Roles"), "ops");
    await chooseOption(canvasElement, "Role match", "all");
    await expect(canvas.getByLabelText("Role match")).toHaveTextContent("all");
  },
};

export const NoHmacKey: Story = {
  args: { hmac: false },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("combobox", { name: "Credential type" }));
    await expect(canvas.getByRole("option", { name: "hmac" })).toHaveAttribute("aria-disabled", "true");
    await expect(canvas.getByText("HMAC needs an INTAKE_KEY on the server.")).toBeInTheDocument();
  },
};

export const DoorNotConfigured: Story = {
  args: { enabled: false },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("button", { name: "Create source" })).toBeDisabled();
  },
};

export const Creating: Story = {
  args: { busy: true },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("button", { name: "Creating…" })).toBeDisabled();
  },
};

// 8 dropdowns on a phone: each trigger is a finger's size, and the last
// one's list opens inside the screen rather than being cut off.
export const Phone: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  args: { hmac: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    await chooseOption(canvasElement, "Trust", "trusted");
    await userEvent.type(canvas.getByLabelText("Roles"), "ops");

    const dropdowns = canvas.getAllByRole("combobox");
    await expect(dropdowns).toHaveLength(9);
    // As wide as the text inputs beside them, not shrunk to their content.
    const name = canvas.getByLabelText("Name").getBoundingClientRect().width;
    for (const dropdown of dropdowns) {
      await expect(dropdown.getBoundingClientRect().width).toBeCloseTo(name, 0);
    }
    for (const dropdown of dropdowns) {
      await expect(dropdown.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    }

    for (const dropdown of dropdowns) {
      dropdown.scrollIntoView({ block: "center" });
      await userEvent.click(dropdown);
      const list = await canvas.findByRole("listbox");
      const rect = list.getBoundingClientRect();
      await expect(rect.top).toBeGreaterThanOrEqual(0);
      await expect(rect.bottom).toBeLessThanOrEqual(window.innerHeight);
      await expect(rect.left).toBeGreaterThanOrEqual(0);
      await expect(rect.right).toBeLessThanOrEqual(window.innerWidth);
      // Not hidden behind, or clipped by, an ancestor.
      const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + 4);
      await expect(list.contains(top)).toBe(true);
      await userEvent.keyboard("{Escape}");
    }
  },
};
