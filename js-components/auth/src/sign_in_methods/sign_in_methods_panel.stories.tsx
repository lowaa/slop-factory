import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import SignInMethodsPanel from "./sign_in_methods_panel";
import { stashPendingStepUp } from "./pending_step_up";
import {
  type FakeServerOptions,
  dexOnly,
  fakeDescribeError,
  fakeErrorCode,
  makeFakeSignInMethodsApi,
  passwordAndDex,
  passwordOnly,
  providers,
} from "../test/fake_sign_in_methods_api";

// Every story drives the in-memory fake server (test/
// fake_sign_in_methods_api.ts), which refuses the way both real servers do
// -- step-up first, never the last way in -- so the play functions exercise
// the panel's real handling of those refusals.
const meta: Meta<typeof SignInMethodsPanel> = {
  title: "SignInMethodsPanel",
  component: SignInMethodsPanel,
  args: {
    providers,
    returnPath: "/account",
    errorCode: fakeErrorCode,
    describeError: fakeDescribeError,
    redirect: fn(),
  },
  beforeEach: () => {
    sessionStorage.clear();
  },
};

export default meta;
type Story = StoryObj<typeof SignInMethodsPanel>;

// withServer gives a story its own fake server, made fresh each time the
// story renders (a loader, not a module-level constant), so a play that
// changes it -- a step-up, a removal -- leaves nothing behind for a re-run.
function withServer(opts: FakeServerOptions): Pick<Story, "loaders" | "render"> {
  return {
    loaders: [async () => ({ api: makeFakeSignInMethodsApi(opts) })],
    render: (args, { loaded }) => <SignInMethodsPanel {...args} api={loaded.api} />,
  };
}

export const PasswordAndProvider: Story = {
  ...withServer({ methods: passwordAndDex }),
};

export const ProviderOnly: Story = {
  ...withServer({ methods: dexOnly }),
};

// An app whose password has its own address: a password has its own address, and waits on the
// emailed link before it signs in.
export const PasswordUnconfirmed: Story = {
  ...withServer({ methods: { ...passwordAndDex, password: { email: "ada@example.com", verified: false } } }),
};

export const IdentityOfAnUnconfiguredProvider: Story = {
  ...withServer({
    methods: { ...passwordAndDex, identities: [{ provider: "gitlab", subject: "7", login: "ada", usable: false }] },
  }),
};

export const LastMethodRefused: Story = {
  ...withServer({ methods: passwordOnly }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Remove password" }));
    await userEvent.click(canvas.getByRole("button", { name: "Yes, remove" }));
    expect(await canvas.findByRole("alert")).toHaveTextContent("That’s the only way you can sign in");
  },
};

// A stale session links GitHub: asked to confirm, confirms with its
// password, and the link carries on to GitHub by itself.
export const StepUpWithPassword: Story = {
  ...withServer({ methods: passwordAndDex, stale: true, password: "the-password" }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Link GitHub" }));
    const prompt = await canvas.findByRole("region", { name: "Confirm it’s you" });
    await userEvent.type(within(prompt).getByLabelText("Current password"), "the-password");
    await userEvent.click(within(prompt).getByRole("button", { name: "Confirm" }));
    await expect(args.redirect).toHaveBeenCalledWith("https://idp.example.test/github?next=/account");
  },
};

// No password to confirm with: only the linked provider is offered.
export const StepUpWithProvider: Story = {
  ...withServer({ methods: dexOnly, stale: true }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Link GitHub" }));
    const prompt = await canvas.findByRole("region", { name: "Confirm it’s you" });
    expect(within(prompt).queryByLabelText("Current password")).not.toBeInTheDocument();
    await userEvent.click(within(prompt).getByRole("button", { name: "Continue with Dex" }));
    await expect(args.redirect).toHaveBeenCalledWith("https://idp.example.test/dex?next=/account");
  },
};

export const WrongPasswordAtStepUp: Story = {
  ...withServer({ methods: passwordAndDex, stale: true, password: "the-password" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Remove Dex sign-in" }));
    await userEvent.click(canvas.getByRole("button", { name: "Yes, remove" }));
    const prompt = await canvas.findByRole("region", { name: "Confirm it’s you" });
    await userEvent.type(within(prompt).getByLabelText("Current password"), "not-it");
    await userEvent.click(within(prompt).getByRole("button", { name: "Confirm" }));
    expect(await within(prompt).findByRole("alert")).toHaveTextContent("That password isn’t right.");
  },
};

// Back from a step-up through Dex: the change it was for is offered again.
export const ResumeAfterStepUp: Story = {
  ...withServer({ methods: passwordAndDex }),
  beforeEach: () => {
    stashPendingStepUp({ kind: "link", provider: "github" });
  },
};

// Back from linking a GitHub account that is someone else's.
export const LinkRefused: Story = {
  ...withServer({ methods: passwordAndDex }),
  args: { callbackError: "identity_in_use" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(await canvas.findByRole("alert")).toHaveTextContent("already linked to a different account here");
  },
};

export const AddPasswordWithEmail: Story = {
  ...withServer({ methods: dexOnly, addedPasswordsNeedVerifying: true }),
  args: {
    addPasswordAsksEmail: true,
    passwordRules: { minLength: 12, maxLength: 72 },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Add a password" }));
    await userEvent.type(canvas.getByLabelText("Email"), "ada@example.com");
    await userEvent.type(canvas.getByLabelText("New password"), "a-long-new-password");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    expect(await canvas.findByRole("status")).toHaveTextContent("Check ada@example.com for a link");
  },
};

export const LoadFailed: Story = {
  loaders: [
    async () => {
      const api = makeFakeSignInMethodsApi({ methods: passwordAndDex });
      api.list.mockRejectedValue(new Error("The server could not be reached."));
      return { api };
    },
  ],
  render: (args, { loaded }) => <SignInMethodsPanel {...args} api={loaded.api} />,
};
