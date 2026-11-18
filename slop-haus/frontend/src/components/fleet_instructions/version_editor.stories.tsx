import { useState, type ComponentProps } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import VersionEditor, { type VersionDraft } from "./version_editor";

// The editor is controlled; the story holds the draft so typing works.
function Stateful(args: ComponentProps<typeof VersionEditor>) {
  const [draft, setDraft] = useState<VersionDraft>(args.draft);
  return <VersionEditor {...args} draft={draft} onChange={setDraft} />;
}

const meta = {
  title: "Fleet instructions/VersionEditor",
  component: VersionEditor,
  args: {
    draft: { name: "", body: "", makeDefault: false },
    maxBodyBytes: 20,
    busy: false,
    onChange: () => {},
    onSave: () => {},
  },
  parameters: { layout: "padded" },
  render: (args) => <Stateful {...args} />,
} satisfies Meta<typeof VersionEditor>;

export default meta;
type Story = StoryObj<typeof meta>;

const SAVE = "Save as a new version";

export const Empty: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: SAVE })).toBeDisabled();
    await expect(canvas.getByText("0 / 20 bytes")).toBeVisible();
  },
};

export const OverTheLimit: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Name"), "house");
    await userEvent.type(canvas.getByLabelText("Instructions"), "héllo, this is far too long");
    await expect(canvas.getByRole("button", { name: SAVE })).toBeDisabled();
  },
};

export const Ready: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Name"), "house");
    await userEvent.type(canvas.getByLabelText("Instructions"), "Be brief.");
    await expect(canvas.getByRole("button", { name: SAVE })).toBeEnabled();
  },
};
