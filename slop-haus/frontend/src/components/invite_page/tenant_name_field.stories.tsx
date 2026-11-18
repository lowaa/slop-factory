import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
// The app loads this through auth_pages_config.tsx, which stories do not
// import.
import "@/auth_pages_config.scss";
import TenantNameField from "./tenant_name_field";

function Controlled({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial);
  return (
    <div className="slop-haus-auth auth-layout__form">
      <TenantNameField value={value} onChange={setValue} hint="How you tell this workspace from your others." />
    </div>
  );
}

const meta = {
  title: "Auth/TenantNameField",
  component: Controlled,
} satisfies Meta<typeof Controlled>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Prefilled: Story = {
  args: { initial: "Ada" },
  play: async ({ canvasElement }) => {
    const field = await within(canvasElement).findByLabelText("Workspace name");
    await expect(field).toHaveValue("Ada");
    await userEvent.type(field, " 2");
    await expect(field).toHaveValue("Ada 2");
  },
};
