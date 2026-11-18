import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import ResetPasswordPage from "./reset_password_page";
import { withTokenInUrl } from "../test/token_in_url";

const meta: Meta<typeof ResetPasswordPage> = {
  title: "ResetPasswordPage",
  component: ResetPasswordPage,
};

export default meta;
type Story = StoryObj<typeof ResetPasswordPage>;

export const Default: Story = {
  beforeEach: withTokenInUrl("story-token"),
};

export const MissingToken: Story = {
  beforeEach: withTokenInUrl(null),
};

export const Success: Story = {
  beforeEach: withTokenInUrl("story-token"),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);

    await userEvent.type(canvas.getByLabelText("New password"), "a-new-strong-password");
    await userEvent.click(canvas.getByRole("button", { name: "Set new password" }));

    expect(await canvas.findByRole("status")).toHaveTextContent("Your password has been changed.");
  },
};
