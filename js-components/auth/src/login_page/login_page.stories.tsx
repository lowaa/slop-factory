import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { makeFakeAuthApi } from "../test/auth_pages_fixture";
import LoginPage from "./login_page";

const meta: Meta<typeof LoginPage> = {
  title: "LoginPage",
  component: LoginPage,
};

export default meta;
type Story = StoryObj<typeof LoginPage>;

export const Default: Story = {};

// The notice a host shows when it dropped a person here mid-use (an
// expired or revoked session).
export const WithNotice: Story = {
  args: { notice: "Your session ended. Please sign in again." },
};

// No sign-up link at all when the deployment doesn't take open sign-ups --
// there'd be nowhere for it to lead.
export const RegistrationClosed: Story = {
  parameters: { authConfig: { registrationOpen: false } },
};

// Real typing and a real submit, driving the actual failure path -- not a
// story that just renders the error node with fixed args.
export const SignInFails: Story = {
  parameters: {
    authConfig: {
      api: makeFakeAuthApi({
        signIn: () => Promise.reject(new Error("Invalid email or password.")),
      }),
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);

    await userEvent.type(canvas.getByLabelText("Email"), "person@example.com");
    await userEvent.type(canvas.getByLabelText("Password"), "wrong-password");
    await userEvent.click(canvas.getByRole("button", { name: "Sign in" }));

    expect(await canvas.findByRole("alert")).toHaveTextContent("Invalid email or password.");
  },
};

const providers = [
  { name: "dex", displayName: "Dex" },
  { name: "github", displayName: "GitHub" },
];

// An app with login providers: a button per provider above the password
// form. Clicking one navigates to that provider's sign-in start.
export const WithProviders: Story = {
  parameters: {
    authConfig: { providers, providerSignInUrl: (name: string) => `/auth/login/${name}`, redirect: fn() },
  },
  play: async ({ canvasElement, parameters }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Continue with GitHub" }));
    expect(parameters.authConfig.redirect).toHaveBeenCalledWith("/auth/login/github");
  },
};

// Password sign-in turned off: providers only, and no forgot-password link.
export const ProvidersOnly: Story = {
  parameters: {
    authConfig: { providers: [providers[0]], providerSignInUrl: () => "/auth/login", passwordLogin: false },
  },
};

// No email here to send a reset link through.
export const NoPasswordReset: Story = {
  parameters: { authConfig: { passwordReset: false } },
};

// Back from a provider that vouched for someone with no account here.
export const ProviderSignInRefused: Story = {
  args: { callbackError: "no_account" },
  parameters: {
    authConfig: { providers: [providers[0]], providerSignInUrl: () => "/auth/login" },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(canvas.getByRole("alert")).toHaveTextContent("There’s no account here for that Dex sign-in.");
  },
};
