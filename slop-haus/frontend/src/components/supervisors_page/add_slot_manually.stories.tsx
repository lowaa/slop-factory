import type { Meta, StoryObj } from "@storybook/react-vite";
import { stubApi } from "@/test/story_harness";
import { expect, userEvent, within } from "storybook/test";
import AddSlotManually from "./add_slot_manually";

const meta = {
  title: "Supervisors/AddSlotManually",
  component: AddSlotManually,
} satisfies Meta<typeof AddSlotManually>;

export default meta;
type Story = StoryObj<typeof meta>;

// The controls only render once the <details> is open; the default-style
// check and the a11y check need them in the DOM.
const open = (canvasElement: HTMLElement) => {
  const all = canvasElement.querySelectorAll("details");
  if (all.length === 0) throw new Error("no <details> rendered");
  all.forEach((d) => (d.open = true));
};

export const ExistingRoute: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await expect(
      canvas.getByRole("tab", { name: "Into a repo you already have" }),
    ).toHaveAttribute("aria-selected", "true");
    await expect(
      canvas.getByRole("tab", { name: "Start a fresh workspace" }),
    ).toHaveAttribute("aria-selected", "false");
    await expect(
      canvas.getByText("Give it a checkout of its own"),
    ).toBeVisible();
    await expect(
      canvas.queryByText("Make somewhere for it to work"),
    ).toBeNull();
  },
};

export const FreshRoute: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("tab", { name: "Start a fresh workspace" }),
    );
    await expect(
      canvas.getByRole("tab", { name: "Start a fresh workspace" }),
    ).toHaveAttribute("aria-selected", "true");
    await expect(
      canvas.getByText("Make somewhere for it to work"),
    ).toBeVisible();
    await expect(
      canvas.queryByText("Give it a checkout of its own"),
    ).toBeNull();
    await expect(canvasElement.querySelector(".add-slot-manually__steps pre")).toHaveTextContent(
      "mkdir -p ~/agents/builder-1",
    );
  },
};

export const NameInput: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Worker name" }),
      "  my worker ",
    );
    await userEvent.click(
      canvas.getByRole("tab", { name: "Start a fresh workspace" }),
    );
    // The name lands in a shell command, so it is trimmed and quoted.
    await expect(canvasElement.querySelector(".add-slot-manually__steps pre")).toHaveTextContent(
      "mkdir -p ~/agents/'my worker'",
    );
  },
};

export const NameRequired: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Download setup script" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "Give the worker a name first.",
    );
  },
};

export const Downloaded: Story = {
  beforeEach: () =>
    stubApi({
      createAgentSetupScript: async (name) => ({
        filename: `slop-haus-setup-${name}.sh`,
        script: "#!/bin/sh\n",
      }),
    }),
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Worker name" }),
      "builder-9",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Download setup script" }),
    );
    await expect(await canvas.findByRole("status")).toHaveTextContent(
      "slop-haus-setup-builder-9.sh",
    );
    await expect(
      canvas.getByText("bash slop-haus-setup-builder-9.sh"),
    ).toBeVisible();
  },
};

export const AdoptStep: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Let a supervisor start it")).toBeVisible();
    await expect(canvas.getByText("slop-haus slot add NAME --setup-script FILE")).toBeVisible();
    await expect(canvas.queryByText(/run_worker/)).toBeNull();
  },
};

export const SlotAddCommands: Story = {
  play: async ({ canvasElement }) => {
    open(canvasElement);
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Copy the add slot command" })).toBeVisible();
    await expect(canvas.getByText("slop-haus slot add --count 3")).toBeVisible();
  },
};
