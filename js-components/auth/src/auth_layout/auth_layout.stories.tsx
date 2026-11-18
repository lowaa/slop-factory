import type { Meta, StoryObj } from "@storybook/react-vite";
import AuthLayout, { AuthNotice } from "./auth_layout";

const meta: Meta<typeof AuthLayout> = {
  title: "AuthLayout",
  component: AuthLayout,
  args: { title: "Sign in" },
};

export default meta;
type Story = StoryObj<typeof AuthLayout>;

export const Plain: Story = {
  args: { children: <p>Card body with no notice.</p> },
};

export const WithSubtitle: Story = {
  args: {
    subtitle: "Use the email address your invite was sent to.",
    children: <p>Card body.</p>,
  },
};

export const ErrorNotice: Story = {
  args: {
    children: <AuthNotice variant="error">Invalid email or password.</AuthNotice>,
  },
};

export const SuccessNotice: Story = {
  args: {
    children: <AuthNotice variant="success">Your account is active. You can now sign in.</AuthNotice>,
  },
};

export const InfoNotice: Story = {
  args: {
    children: <AuthNotice variant="info">Your session ended. Please sign in again.</AuthNotice>,
  },
};
