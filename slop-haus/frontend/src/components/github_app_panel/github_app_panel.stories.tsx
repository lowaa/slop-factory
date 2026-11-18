import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { apiClient, ApiError, type GitHubAppConnection, type GitHubAppStatus } from "@/api";
import { stubApi } from "@/test/story_harness";
import GitHubAppPanel from "./github_app_panel";

// Obviously fake: not a PEM, and only ever sent to the stubbed apiClient.
const FAKE_KEY = "fake-private-key-placeholder-not-a-real-key";

function makeConnection(overrides: Partial<GitHubAppConnection> = {}): GitHubAppConnection {
  return {
    app_id: 4242,
    app_slug: "fake-app-slug",
    pending: false,
    installation_id: 777,
    key_fingerprint: "0123456789abcdef".repeat(4),
    created_at: "2026-10-05T12:00:00Z",
    updated_at: "2026-10-05T12:00:00Z",
    verified_at: null,
    ...overrides,
  };
}

function status(overrides: Partial<GitHubAppStatus> = {}): GitHubAppStatus {
  return { enabled: true, can_edit: true, credential: null, ...overrides };
}

const meta = {
  title: "Settings/GitHubAppPanel",
  component: GitHubAppPanel,
} satisfies Meta<typeof GitHubAppPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NotConfigured: Story = {
  beforeEach: () => stubApi({ getGitHubApp: async () => status() }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("No GitHub App is connected.");
    await expect(canvas.getByRole("button", { name: "Create GitHub App" })).toBeEnabled();
    await expect(canvas.getByLabelText(/GitHub organisation that should own the App/)).toBeVisible();

    await userEvent.click(canvas.getByText("Connect an App that already exists, by hand"));
    await expect(canvas.getByLabelText("App id")).toBeVisible();
    await expect(canvas.getByLabelText("Installation id")).toBeVisible();
    await expect(canvas.getByLabelText("Private key (PEM)")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Connect" })).toBeEnabled();
  },
};

export const ConnectStoresTheKeyWriteOnly: Story = {
  beforeEach: () => {
    const connected = makeConnection();
    let stored = false;
    stubApi({
      getGitHubApp: async () => status({ credential: stored ? connected : null }),
      connectGitHubApp: fn(async () => {
        stored = true;
        return connected;
      }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByText("Connect an App that already exists, by hand"));
    await userEvent.type(canvas.getByLabelText("App id"), "4242");
    await userEvent.type(canvas.getByLabelText("Installation id"), "777");
    await userEvent.click(canvas.getByLabelText("Private key (PEM)"));
    await userEvent.paste(FAKE_KEY);
    await userEvent.click(canvas.getByRole("button", { name: "Connect" }));

    await canvas.findByText("Key fingerprint");
    await expect(apiClient.connectGitHubApp).toHaveBeenCalledTimes(1);
    await expect(apiClient.connectGitHubApp).toHaveBeenCalledWith({
      app_id: 4242,
      installation_id: 777,
      private_key: FAKE_KEY,
    });
    // The key is write-only: the panel shows the fingerprint, never the key.
    await expect(canvasElement.textContent).not.toContain(FAKE_KEY);
    await expect(canvas.queryByLabelText("Private key (PEM)")).toBeNull();
    await expect(canvas.getByText("0123456789abcdef")).toBeVisible();
  },
};

export const SaveError: Story = {
  beforeEach: () =>
    stubApi({
      getGitHubApp: async () => status(),
      connectGitHubApp: fn(async () => {
        throw new ApiError(400, "The private key is not a valid RSA key.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByText("Connect an App that already exists, by hand"));
    await userEvent.type(canvas.getByLabelText("App id"), "4242");
    await userEvent.type(canvas.getByLabelText("Installation id"), "777");
    await userEvent.click(canvas.getByLabelText("Private key (PEM)"));
    await userEvent.paste(FAKE_KEY);
    await userEvent.click(canvas.getByRole("button", { name: "Connect" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("The private key is not a valid RSA key.");
    await expect(apiClient.connectGitHubApp).toHaveBeenCalledTimes(1);
    // The form stays up for another attempt.
    await expect(canvas.getByRole("button", { name: "Connect" })).toBeEnabled();
  },
};

export const InvalidIdsAreNotSent: Story = {
  beforeEach: () =>
    stubApi({
      getGitHubApp: async () => status(),
      connectGitHubApp: fn(async () => makeConnection()),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByText("Connect an App that already exists, by hand"));
    await userEvent.type(canvas.getByLabelText("App id"), "not-a-number");
    await userEvent.type(canvas.getByLabelText("Installation id"), "777");
    await userEvent.click(canvas.getByRole("button", { name: "Connect" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("whole numbers");
    await expect(apiClient.connectGitHubApp).not.toHaveBeenCalled();

    await userEvent.clear(canvas.getByLabelText("App id"));
    await userEvent.type(canvas.getByLabelText("App id"), "4242");
    await userEvent.clear(canvas.getByLabelText("Installation id"));
    await userEvent.type(canvas.getByLabelText("Installation id"), "0");
    await userEvent.click(canvas.getByRole("button", { name: "Connect" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("whole numbers");
    await expect(apiClient.connectGitHubApp).not.toHaveBeenCalled();
  },
};

export const StepUpThenConnect: Story = {
  beforeEach: () => {
    stubApi({
      getGitHubApp: async () => status(),
      listSignInMethods: async () => ({
        password: { email: "person@example.com", verified: true },
        identities: [],
        password_login_enabled: true,
        reauth_required_after: "2026-01-01T00:00:00Z",
      }),
      reauthWithPassword: fn(async () => {}),
      connectGitHubApp: fn()
        .mockRejectedValueOnce(new ApiError(401, "sign in again", "reauth_required"))
        .mockResolvedValue(makeConnection()),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByText("Connect an App that already exists, by hand"));
    await userEvent.type(canvas.getByLabelText("App id"), "4242");
    await userEvent.type(canvas.getByLabelText("Installation id"), "777");
    await userEvent.click(canvas.getByLabelText("Private key (PEM)"));
    await userEvent.paste(FAKE_KEY);
    await userEvent.click(canvas.getByRole("button", { name: "Connect" }));

    await canvas.findByText("Confirm it’s you");
    await userEvent.type(canvas.getByLabelText("Current password"), "fake-password");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(apiClient.connectGitHubApp).toHaveBeenCalledTimes(2));
    await expect(apiClient.reauthWithPassword).toHaveBeenCalledWith("fake-password");
    await expect(apiClient.connectGitHubApp).toHaveBeenLastCalledWith({
      app_id: 4242,
      installation_id: 777,
      private_key: FAKE_KEY,
    });
  },
};

export const Configured: Story = {
  beforeEach: () => stubApi({ getGitHubApp: async () => status({ credential: makeConnection() }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("Key fingerprint");
    await expect(canvas.getByText("4242")).toBeVisible();
    await expect(canvas.getByText("777")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Verify" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Replace" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Remove" })).toBeEnabled();
    await expect(canvas.queryByRole("button", { name: "Install the App" })).toBeNull();

    // Removing asks first, and nothing is sent until it is confirmed.
    await userEvent.click(canvas.getByRole("button", { name: "Remove" }));
    await expect(canvas.getByRole("button", { name: "Confirm remove" })).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Keep" }));
    await expect(canvas.getByRole("button", { name: "Remove" })).toBeVisible();
  },
};

export const RemoveConnection: Story = {
  beforeEach: () => {
    let removed = false;
    stubApi({
      getGitHubApp: async () => status({ credential: removed ? null : makeConnection() }),
      removeGitHubApp: fn(async () => {
        removed = true;
      }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Remove" }));
    await expect(apiClient.removeGitHubApp).not.toHaveBeenCalled();
    await userEvent.click(canvas.getByRole("button", { name: "Confirm remove" }));
    await canvas.findByText("No GitHub App is connected.");
    await expect(apiClient.removeGitHubApp).toHaveBeenCalledTimes(1);
  },
};

export const Verified: Story = {
  beforeEach: () => {
    stubApi({
      getGitHubApp: async () => status({ credential: makeConnection() }),
      verifyGitHubApp: fn(async () => ({
        account: "fake-org",
        repos: ["fake-org/one", "fake-org/two"],
        manifest: [
          { remote: "github.com/fake-org/one", visible: true },
          { remote: "github.com/fake-org/hidden", visible: false },
        ],
      })),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Verify" }));
    await expect(await canvas.findByText("NOT visible to the installation")).toBeVisible();
    await expect(canvas.getByText("visible to the installation")).toBeVisible();
    await expect(apiClient.verifyGitHubApp).toHaveBeenCalledTimes(1);
  },
};

// The App is created on GitHub but nobody has installed it yet.
export const PendingInstall: Story = {
  beforeEach: () =>
    stubApi({
      getGitHubApp: async () =>
        status({ credential: makeConnection({ pending: true, installation_id: 0 }) }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("not installed yet");
    await expect(canvas.getByRole("button", { name: "Install the App" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Verify" })).toBeEnabled();
  },
};

export const ReplaceConfirmation: Story = {
  beforeEach: () => {
    stubApi({
      getGitHubApp: async () => status({ credential: makeConnection() }),
      // Confirming would leave the story for GitHub's page, so it is only
      // asserted that nothing is started before then.
      beginGitHubAppManifest: fn(async () => ({ action_url: "https://github.invalid/", manifest: "{}" })),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Replace" }));
    await userEvent.click(canvas.getByRole("button", { name: "Create GitHub App" }));

    const dialog = await canvas.findByRole("dialog", { name: "Replace the working connection?" });
    await expect(within(dialog).getByRole("button", { name: "Replace and create the App" })).toBeEnabled();
    await expect(apiClient.beginGitHubAppManifest).not.toHaveBeenCalled();

    await userEvent.click(within(dialog).getByRole("button", { name: "Keep the current connection" }));
    await expect(canvas.queryByRole("dialog")).toBeNull();
    await expect(apiClient.beginGitHubAppManifest).not.toHaveBeenCalled();
  },
};

export const NotAnOwner: Story = {
  beforeEach: () =>
    stubApi({ getGitHubApp: async () => status({ can_edit: false, credential: makeConnection() }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("Only the tenant’s owners can change this.");
    await expect(canvas.queryByRole("button")).toBeNull();
  },
};
