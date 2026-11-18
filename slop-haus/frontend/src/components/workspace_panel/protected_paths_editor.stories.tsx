import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { ManifestRepo } from "@/api";
import ProtectedPathsEditor from "./protected_paths_editor";
import "./manifest_section.scss";

type Rules = NonNullable<ManifestRepo["protected_paths"]>;

function Harness({ initial }: { initial: Rules }) {
  const [rules, setRules] = useState<Rules>(initial);
  return <ProtectedPathsEditor repoIndex={0} rules={rules} onChange={setRules} />;
}

const meta = {
  title: "Workspace/ProtectedPathsEditor",
  component: Harness,
  args: { initial: [] },
} satisfies Meta<typeof Harness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const EmptyWarnsNothingIsProtected: Story = {
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText(/nothing is protected/)).toBeVisible();
  },
};

export const PreviewNamesTheRule: Story = {
  args: { initial: [{ path: "infrastructure/" }] },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Preview a path of repo 1"), "infrastructure/a.yaml");
    await expect(canvas.getByLabelText("Preview result of repo 1")).toHaveTextContent("Protected by infrastructure/.");
  },
};
