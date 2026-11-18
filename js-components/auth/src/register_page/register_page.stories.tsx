import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import RegisterPage from "./register_page";

const meta: Meta<typeof RegisterPage> = {
  title: "RegisterPage",
  component: RegisterPage,
};

export default meta;
type Story = StoryObj<typeof RegisterPage>;

export const Default: Story = {};

// Showing the form and letting every submit fail would be a worse account
// of "sign-up is closed" than saying so up front -- the whole form is
// swapped for a notice.
export const ByInvitationOnly: Story = {
  parameters: { authConfig: { registrationOpen: false } },
};

// Register always answers the same way whether or not the address is
// already registered (enumeration protection) -- driven through the real
// form rather than asserted on fixed args, so the story proves the
// success state is actually reachable by submitting.
export const Submitted: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);

    await userEvent.type(canvas.getByLabelText("Display name"), "Ada Lovelace");
    await userEvent.type(canvas.getByLabelText("Email"), "ada@example.com");
    await userEvent.type(canvas.getByLabelText("Password"), "a-strong-password");
    await userEvent.click(canvas.getByRole("button", { name: "Create account" }));

    expect(await canvas.findByRole("heading", { name: "Check your email" })).toBeInTheDocument();
    expect(canvas.getByRole("status")).toHaveTextContent("ada@example.com");
  },
};
