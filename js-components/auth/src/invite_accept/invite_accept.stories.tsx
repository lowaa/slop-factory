import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";
import { FakeAuthError, fakeErrorCode } from "../test/fake_sign_in_methods_api";
import InviteAccept from "./invite_accept";

// Shown inside the card an app's own invite page renders it in.
const meta: Meta<typeof InviteAccept> = {
  title: "InviteAccept",
  component: InviteAccept,
  args: {
    onAcceptWithPassword: fn(async () => {}),
    intro: <AuthNotice variant="info">Ada invited you to review runs for acme.</AuthNotice>,
  },
  decorators: [
    (Story) => (
      <AuthLayout title="You’re invited" subtitle="grace@example.com">
        <Story />
      </AuthLayout>
    ),
  ],
  parameters: {
    authConfig: { providers: [{ name: "dex", displayName: "Dex" }], errorCode: fakeErrorCode, redirect: fn() },
  },
};

export default meta;
type Story = StoryObj<typeof InviteAccept>;

// Password only: the app offers no provider round trip.
export const PasswordOnly: Story = {};

export const WithProvider: Story = {
  args: { onContinueWithProvider: fn(async () => "https://dex.example.test/auth") },
  play: async ({ canvasElement, args, parameters }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Continue with Dex" }));
    expect(args.onContinueWithProvider).toHaveBeenCalledWith("dex");
    expect(parameters.authConfig.redirect).toHaveBeenCalledWith("https://dex.example.test/auth");
  },
};

// The app's own fields sit in the same form as the password, with a note by
// the provider buttons that they won't carry over if a provider is chosen
// instead.
export const WithAppFields: Story = {
  args: {
    onContinueWithProvider: fn(async () => "https://dex.example.test/auth"),
    passwordFields: (
      <label className="auth-layout__field">
        <span>Display name</span>
        <input type="text" autoComplete="name" />
      </label>
    ),
  },
};

export const ProvidersOnly: Story = {
  args: { onContinueWithProvider: fn(async () => "https://dex.example.test/auth") },
  parameters: {
    authConfig: {
      providers: [{ name: "dex", displayName: "Dex" }],
      errorCode: fakeErrorCode,
      redirect: fn(),
      passwordLogin: false,
    },
  },
};

// A real submit that the app's call refuses with a code the package knows.
export const RefusedByCode: Story = {
  args: {
    onAcceptWithPassword: fn(async () => {
      throw new FakeAuthError("password_exists");
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Choose a password"), "a-good-password");
    await userEvent.click(canvas.getByRole("button", { name: "Accept invite" }));
    expect(await canvas.findByRole("alert")).toHaveTextContent("This account already has a password.");
  },
};

// Back from Dex with an identity that is already someone else's.
export const ProviderRefused: Story = {
  args: {
    onContinueWithProvider: fn(async () => "https://dex.example.test/auth"),
    callbackError: "identity_in_use",
  },
};
