import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import ForgotPasswordPage from "./forgot_password_page";

const meta: Meta<typeof ForgotPasswordPage> = {
  title: "ForgotPasswordPage",
  component: ForgotPasswordPage,
};

export default meta;
type Story = StoryObj<typeof ForgotPasswordPage>;

export const Default: Story = {};

// Answers the same way whether or not the address has an account
// (enumeration protection) -- driven through the real form so the story
// proves the success state is actually reachable by submitting.
export const Sent: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);

    await userEvent.type(canvas.getByLabelText("Email"), "ada@example.com");
    await userEvent.click(canvas.getByRole("button", { name: "Send reset link" }));

    expect(await canvas.findByRole("heading", { name: "Check your email" })).toBeInTheDocument();
    expect(canvas.getByRole("status")).toBeInTheDocument();
  },
};
