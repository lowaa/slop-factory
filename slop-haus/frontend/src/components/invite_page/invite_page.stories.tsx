import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import type { AuthPagesConfig } from "@slop-factory/auth";
import { apiClient, ApiError, type InvitePreview } from "@/api";
import { makeInvitePreview, makeProfile } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { WithAuthPagesConfig } from "@/test/with_auth_pages";
// The app loads this through auth_pages_config.tsx, which stories do not
// import; without it the card is flagged as default-styled.
import "@/auth_pages_config.scss";
import InvitePage from "./invite_page";
import { clearPendingInvite } from "./pending_invite";

// Obviously fake: the page only ever sends this to the stubbed apiClient.
const FAKE_TOKEN = "fake-invite-token-not-real";

// The page reads its token from the emailed link's fragment. The token is
// module state once read, so it is cleared with the story or it would leak
// into the next one.
function openInviteLink(preview: () => Promise<InvitePreview>): () => void {
  window.history.replaceState(null, "", `/invite#token=${FAKE_TOKEN}`);
  stubApi({ previewInvite: preview });
  return clearPendingInvite;
}

// layoutClassName is what the app's own config gives the card, and what
// auth_pages_config.scss scopes its rules under.
const redirect = fn();
const signOut = fn(async () => {});

const APP_LIKE_CONFIG: Partial<AuthPagesConfig> = {
  layoutClassName: "slop-haus-auth",
  providers: [{ name: "dex", displayName: "Dex" }],
  // The default would leave the story for the provider's page.
  redirect,
  describeError: (err) =>
    err instanceof Error ? err.message : "Something went wrong.",
};

const SIGNED_OUT = { status: "anonymous", profile: null } as const;

const meta = {
  title: "Auth/InvitePage",
  component: InvitePage,
  args: { onNavigate: () => {} },
  decorators: [
    (Story) => (
      <WithAuthPagesConfig config={APP_LIKE_CONFIG}>
        <Story />
      </WithAuthPagesConfig>
    ),
  ],
} satisfies Meta<typeof InvitePage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NewAccountSignedOut: Story = {
  parameters: { harness: { auth: SIGNED_OUT } },
  beforeEach: () => {
    const cleanup = openInviteLink(async () =>
      makeInvitePreview({ account_exists: false }),
    );
    // A refused accept keeps the page up: a success would navigate away.
    stubApi({
      acceptInviteAsNewAccount: fn(async () => {
        throw new ApiError(410, "This invite has already been used.");
      }),
    });
    return cleanup;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("button", { name: "Create account" });
    await expect(canvas.getByLabelText("Display name")).toBeVisible();
    await expect(canvas.getByLabelText("Password")).toBeVisible();
    await expect(canvas.getByRole("button", { name: /Dex/ })).toBeVisible();

    await userEvent.type(canvas.getByLabelText("Display name"), "Ada");
    await userEvent.type(
      canvas.getByLabelText("Password"),
      "correct horse battery",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Create account" }),
    );

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "This invite has already been used.",
    );
    await expect(apiClient.acceptInviteAsNewAccount).toHaveBeenCalledTimes(1);
    await expect(apiClient.acceptInviteAsNewAccount).toHaveBeenCalledWith(
      FAKE_TOKEN,
      "Ada",
      "correct horse battery",
    );
  },
};

export const ExistingAccountSignedOut: Story = {
  parameters: { harness: { auth: SIGNED_OUT } },
  beforeEach: () => {
    redirect.mockClear();
    stubApi({
      startInviteWithProvider: fn(
        async () => "https://provider.example/authorize",
      ),
    });
    return openInviteLink(async () =>
      makeInvitePreview({ account_exists: true }),
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("link", { name: "Sign in" })).toBeVisible();
    await userEvent.click(await canvas.findByRole("button", { name: /Dex/ }));
    await expect(apiClient.startInviteWithProvider).toHaveBeenCalledTimes(1);
    await expect(apiClient.startInviteWithProvider).toHaveBeenCalledWith(
      "dex",
      FAKE_TOKEN,
      "/",
    );
    await expect(redirect).toHaveBeenCalledTimes(1);
    await expect(redirect).toHaveBeenCalledWith(
      "https://provider.example/authorize",
    );
  },
};

export const SignedInAccept: Story = {
  parameters: {
    harness: { profile: makeProfile({ email: "invitee@example.com" }) },
  },
  beforeEach: () => {
    const cleanup = openInviteLink(async () =>
      makeInvitePreview({ account_exists: true }),
    );
    stubApi({
      acceptInvite: fn(async () => {
        throw new ApiError(410, "This invite has expired.");
      }),
    });
    return cleanup;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Accept" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "This invite has expired.",
    );
    await expect(apiClient.acceptInvite).toHaveBeenCalledTimes(1);
    await expect(apiClient.acceptInvite).toHaveBeenCalledWith(FAKE_TOKEN);
    await expect(canvas.getByRole("button", { name: "Accept" })).toBeEnabled();
  },
};

export const SignedInAsSomeoneElse: Story = {
  parameters: {
    harness: {
      profile: makeProfile({ email: "someone.else@example.com" }),
      auth: { signOut },
    },
  },
  beforeEach: () =>
    openInviteLink(async () => makeInvitePreview({ account_exists: true })),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "someone.else@example.com",
    );
    await expect(
      canvas.getByRole("button", { name: "Sign out" }),
    ).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Accept" })).toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: "Sign out" }));
    await expect(signOut).toHaveBeenCalledOnce();
  },
};

export const ExpiredInvite: Story = {
  parameters: { harness: { auth: SIGNED_OUT } },
  beforeEach: () =>
    openInviteLink(async () => {
      throw new ApiError(410, "This invite has expired.");
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "This invite has expired.",
    );
    await expect(canvas.getByRole("link", { name: "Sign in" })).toBeVisible();
  },
};
