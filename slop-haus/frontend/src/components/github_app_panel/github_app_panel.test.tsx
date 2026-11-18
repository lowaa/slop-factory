import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  apiClient,
  ApiError,
  type GitHubAppConnection,
  type GitHubAppStatus,
} from "@/api";
import * as navigation from "@/lib/navigation";
import { renderSignedIn } from "@/test/render_signed_in";
import GitHubAppPanel from "./github_app_panel";

afterEach(() => vi.restoreAllMocks());

const FINGERPRINT = "abcdef0123456789".repeat(4);

function connection(
  overrides: Partial<GitHubAppConnection> = {},
): GitHubAppConnection {
  return {
    app_id: 4242,
    app_slug: "",
    pending: false,
    installation_id: 777,
    key_fingerprint: FINGERPRINT,
    created_at: "2026-10-05T12:00:00Z",
    updated_at: "2026-10-05T12:00:00Z",
    verified_at: null,
    ...overrides,
  };
}

function stubStatus(overrides: Partial<GitHubAppStatus> = {}) {
  return vi
    .spyOn(apiClient, "getGitHubApp")
    .mockResolvedValue({
      enabled: true,
      can_edit: true,
      credential: null,
      ...overrides,
    });
}

const PEM =
  "-----BEGIN RSA PRIVATE KEY-----\nnot-a-real-key\n-----END RSA PRIVATE KEY-----";

async function fillForm(
  user: ReturnType<typeof userEvent.setup>,
  appId = "4242",
  installationId = "777",
) {
  await user.type(screen.getByLabelText("App id"), appId);
  await user.type(screen.getByLabelText("Installation id"), installationId);
  await user.click(screen.getByLabelText("Private key (PEM)"));
  await user.paste(PEM);
}

describe("GitHubAppPanel", () => {
  it("says nothing is connected and offers the form to an owner", async () => {
    stubStatus();
    await renderSignedIn(<GitHubAppPanel />);

    expect(
      await screen.findByText("No GitHub App is connected."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeInTheDocument();
  });

  it("sends the key once, then clears it from the page and shows only the fingerprint", async () => {
    const user = userEvent.setup();
    const status = stubStatus();
    const connect = vi
      .spyOn(apiClient, "connectGitHubApp")
      .mockResolvedValue(connection());
    await renderSignedIn(<GitHubAppPanel />);
    await screen.findByText("No GitHub App is connected.");

    await fillForm(user);
    status.mockResolvedValue({
      enabled: true,
      can_edit: true,
      credential: connection(),
    });
    await user.click(screen.getByRole("button", { name: "Connect" }));

    expect(connect).toHaveBeenCalledWith({
      app_id: 4242,
      installation_id: 777,
      private_key: PEM,
    });
    expect(await screen.findByText("4242")).toBeInTheDocument();
    expect(screen.getByText(FINGERPRINT.slice(0, 16))).toBeInTheDocument();
    expect(
      screen.queryByLabelText("Private key (PEM)"),
    ).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("not-a-real-key");
  });

  it("does not keep a replaced key in the form for the next time it opens", async () => {
    const user = userEvent.setup();
    stubStatus({ credential: connection() });
    vi.spyOn(apiClient, "connectGitHubApp").mockResolvedValue(connection());
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Replace" }));
    await fillForm(user);
    await user.click(screen.getByRole("button", { name: "Replace" }));
    await user.click(await screen.findByRole("button", { name: "Replace" }));

    expect(screen.getByLabelText("Private key (PEM)")).toHaveValue("");
    expect(screen.getByLabelText("App id")).toHaveValue("");
  });

  it("refuses ids that are not whole numbers without calling the server", async () => {
    const user = userEvent.setup();
    stubStatus();
    const connect = vi.spyOn(apiClient, "connectGitHubApp");
    await renderSignedIn(<GitHubAppPanel />);
    await screen.findByText("No GitHub App is connected.");

    await fillForm(user, "abc", "777");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("whole numbers");
    expect(connect).not.toHaveBeenCalled();
  });

  it("keeps what was typed when the server refuses the key", async () => {
    const user = userEvent.setup();
    stubStatus();
    vi.spyOn(apiClient, "connectGitHubApp").mockRejectedValue(
      new ApiError(
        422,
        "githubapp: not a usable RSA private key: no PEM block",
      ),
    );
    await renderSignedIn(<GitHubAppPanel />);
    await screen.findByText("No GitHub App is connected.");

    await fillForm(user);
    await user.click(screen.getByRole("button", { name: "Connect" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "not a usable RSA private key",
    );
    expect(screen.getByLabelText("App id")).toHaveValue("4242");
  });

  it("shows the connection and verifies it against the manifest", async () => {
    const user = userEvent.setup();
    stubStatus({ credential: connection() });
    const verify = vi.spyOn(apiClient, "verifyGitHubApp").mockResolvedValue({
      account: "acme",
      repos: ["acme/app", "acme/docs"],
      manifest: [
        { remote: "github.com/acme/app", visible: true },
        { remote: "github.com/acme/missing", visible: false },
      ],
    });
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Verify" }));

    expect(verify).toHaveBeenCalled();
    expect(
      await screen.findByText(/which can see 2 repositories/),
    ).toBeInTheDocument();
    expect(screen.getByText("visible to the installation")).toBeInTheDocument();
    expect(
      screen.getByText("NOT visible to the installation"),
    ).toBeInTheDocument();
  });

  it("renders a verification with no repositories and no manifest", async () => {
    const user = userEvent.setup();
    stubStatus({ credential: connection() });
    vi.spyOn(apiClient, "verifyGitHubApp").mockResolvedValue({
      account: "acme",
      repos: [],
      manifest: [],
    });
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Verify" }));

    expect(
      await screen.findByText(/which can see 0 repositories/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows a refused verification as an error, not as a verification", async () => {
    const user = userEvent.setup();
    stubStatus({ credential: connection() });
    vi.spyOn(apiClient, "verifyGitHubApp").mockRejectedValue(
      new ApiError(502, "github API answered 401: Bad credentials"),
    );
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Verify" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Bad credentials",
    );
    expect(screen.queryByText(/which can see/)).not.toBeInTheDocument();
  });

  it("removes only after a confirmation", async () => {
    const user = userEvent.setup();
    const status = stubStatus({ credential: connection() });
    const remove = vi
      .spyOn(apiClient, "removeGitHubApp")
      .mockResolvedValue(undefined);
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Remove" }));
    expect(remove).not.toHaveBeenCalled();

    status.mockResolvedValue({
      enabled: true,
      can_edit: true,
      credential: null,
    });
    await user.click(screen.getByRole("button", { name: "Confirm remove" }));

    expect(remove).toHaveBeenCalledTimes(1);
    expect(
      await screen.findByText("No GitHub App is connected."),
    ).toBeInTheDocument();
  });

  it("offers a member no controls", async () => {
    stubStatus({ can_edit: false, credential: connection() });
    await renderSignedIn(<GitHubAppPanel />);

    expect(await screen.findByText("4242")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Verify|Replace|Remove|Connect/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/Only the tenant/)).toBeInTheDocument();
  });

  it("says when the server keeps no credentials", async () => {
    stubStatus({ enabled: false });
    await renderSignedIn(<GitHubAppPanel />);

    expect(
      await screen.findByText(/no CREDENTIAL_ENCRYPTION_KEY/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Connect" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Create GitHub App" }),
    ).not.toBeInTheDocument();
  });

  it("asks for a fresh sign-in when the server wants one, then sends the connect again", async () => {
    const user = userEvent.setup();
    stubStatus();
    vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
      password: { email: "person@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    });
    vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);
    const connect = vi
      .spyOn(apiClient, "connectGitHubApp")
      .mockRejectedValueOnce(
        new ApiError(401, "sign in again", "reauth_required"),
      )
      .mockResolvedValue(connection());
    await renderSignedIn(<GitHubAppPanel />);
    await screen.findByText("No GitHub App is connected.");

    await fillForm(user);
    await user.click(screen.getByRole("button", { name: "Connect" }));
    await screen.findByText("Confirm it’s you");
    await user.type(screen.getByLabelText("Current password"), "correct-password");
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await vi.waitFor(() => expect(connect).toHaveBeenCalledTimes(2));
    expect(connect).toHaveBeenLastCalledWith({
      app_id: 4242,
      installation_id: 777,
      private_key: PEM,
    });
  });

  it("renders every button as the shared Button", async () => {
    stubStatus({ credential: connection() });
    const user = userEvent.setup();
    await renderSignedIn(<GitHubAppPanel />);

    await user.click(await screen.findByRole("button", { name: "Remove" }));
    const confirming = screen.getAllByRole("button");
    expect(confirming).toHaveLength(4);
    expect(screen.getByRole("button", { name: "Confirm remove" })).toHaveClass(
      "button--danger",
    );
    await user.click(screen.getByRole("button", { name: "Keep" }));
    await user.click(screen.getByRole("button", { name: "Replace" }));
    const form = screen.getAllByRole("button");
    expect(screen.getByRole("button", { name: "Replace" })).toHaveClass(
      "button--primary",
    );

    // Cancel is not a submit: it leaves replace mode without posting the form.
    const connect = vi.spyOn(apiClient, "connectGitHubApp");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(connect).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("App id")).not.toBeInTheDocument();

    for (const button of [...confirming, ...form])
      expect(button).toHaveClass("button");
  });

  describe("creating the App from a manifest", () => {
    it("asks the server for the manifest and posts it to the GitHub URL the server built", async () => {
      const user = userEvent.setup();
      stubStatus();
      const begin = vi
        .spyOn(apiClient, "beginGitHubAppManifest")
        .mockResolvedValue({
          action_url:
            "https://github.com/organizations/acme/settings/apps/new?state=s",
          manifest: '{"name":"slop-haus-abc"}',
        });
      const post = vi
        .spyOn(navigation, "postFormTo")
        .mockImplementation(() => undefined);
      await renderSignedIn(<GitHubAppPanel />);

      await user.type(
        await screen.findByLabelText(/GitHub organisation that should own/),
        "acme",
      );
      await user.click(
        screen.getByRole("button", { name: "Create GitHub App" }),
      );

      expect(begin).toHaveBeenCalledWith("acme");
      expect(post).toHaveBeenCalledWith(
        "https://github.com/organizations/acme/settings/apps/new?state=s",
        {
          manifest: '{"name":"slop-haus-abc"}',
        },
      );
    });

    describe("when a working connection already exists", () => {
      async function openReplace() {
        const user = userEvent.setup();
        stubStatus({ credential: connection() });
        const begin = vi
          .spyOn(apiClient, "beginGitHubAppManifest")
          .mockResolvedValue({
            action_url: "https://github.com/settings/apps/new?state=s",
            manifest: "{}",
          });
        const post = vi
          .spyOn(navigation, "postFormTo")
          .mockImplementation(() => undefined);
        await renderSignedIn(<GitHubAppPanel />);
        await user.click(await screen.findByRole("button", { name: "Replace" }));
        return { user, begin, post };
      }

      it("warns beside the Create button that the connection stops until the new App is installed", async () => {
        await openReplace();
        expect(screen.getByRole("note")).toHaveTextContent(
          /stops working as soon as GitHub creates the new App.*until the new App is installed.*owner to approve.*delete it there by hand/,
        );
      });

      it("asks for confirmation before anything is sent to GitHub, and Keep sends nothing", async () => {
        const { user, begin, post } = await openReplace();

        await user.click(
          screen.getByRole("button", { name: "Create GitHub App" }),
        );
        const dialog = screen.getByRole("dialog");
        expect(dialog).toHaveTextContent(/stops working as soon as GitHub/);
        expect(begin).not.toHaveBeenCalled();

        await user.click(
          screen.getByRole("button", { name: "Keep the current connection" }),
        );
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        expect(begin).not.toHaveBeenCalled();
        expect(post).not.toHaveBeenCalled();
      });

      it("goes to GitHub only after the dialog is confirmed", async () => {
        const { user, begin, post } = await openReplace();

        await user.click(
          screen.getByRole("button", { name: "Create GitHub App" }),
        );
        await user.click(
          screen.getByRole("button", { name: "Replace and create the App" }),
        );

        await vi.waitFor(() => expect(post).toHaveBeenCalled());
        expect(begin).toHaveBeenCalledTimes(1);
      });
    });

    it("needs no confirmation or warning when nothing is connected", async () => {
      const user = userEvent.setup();
      stubStatus();
      const begin = vi
        .spyOn(apiClient, "beginGitHubAppManifest")
        .mockResolvedValue({ action_url: "https://github.com/x", manifest: "{}" });
      vi.spyOn(navigation, "postFormTo").mockImplementation(() => undefined);
      await renderSignedIn(<GitHubAppPanel />);
      await user.click(
        await screen.findByRole("button", { name: "Create GitHub App" }),
      );
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.queryByRole("note")).not.toBeInTheDocument();
      expect(begin).toHaveBeenCalled();
    });

    it("asks for a recent sign-in and then retries the create", async () => {
      const user = userEvent.setup();
      stubStatus();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
        password: { email: "person@example.com", verified: true },
        identities: [],
        password_login_enabled: true,
        reauth_required_after: "2026-01-01T00:00:00Z",
      });
      vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);
      const begin = vi
        .spyOn(apiClient, "beginGitHubAppManifest")
        .mockRejectedValueOnce(
          new ApiError(401, "sign in again", "reauth_required"),
        )
        .mockResolvedValue({ action_url: "https://github.com/x", manifest: "{}" });
      vi.spyOn(navigation, "postFormTo").mockImplementation(() => undefined);
      await renderSignedIn(<GitHubAppPanel />);

      await user.click(
        await screen.findByRole("button", { name: "Create GitHub App" }),
      );
      await screen.findByText("Confirm it’s you");
      await user.type(screen.getByLabelText("Current password"), "correct-password");
      await user.click(screen.getByRole("button", { name: "Confirm" }));

      await vi.waitFor(() => expect(begin).toHaveBeenCalledTimes(2));
    });

    it("says plainly that an org owner will be asked to approve the install", async () => {
      stubStatus();
      await renderSignedIn(<GitHubAppPanel />);
      expect(
        await screen.findByText(
          /an owner of the organisation will be asked to approve/,
        ),
      ).toBeInTheDocument();
    });

    it("shows what the server refused, and sends nothing to GitHub", async () => {
      const user = userEvent.setup();
      stubStatus();
      vi.spyOn(apiClient, "beginGitHubAppManifest").mockRejectedValue(
        new ApiError(422, "that is not a GitHub account name"),
      );
      const post = vi
        .spyOn(navigation, "postFormTo")
        .mockImplementation(() => undefined);
      await renderSignedIn(<GitHubAppPanel />);

      await user.type(
        await screen.findByLabelText(/GitHub organisation that should own/),
        "a/b",
      );
      await user.click(
        screen.getByRole("button", { name: "Create GitHub App" }),
      );

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "not a GitHub account name",
      );
      expect(post).not.toHaveBeenCalled();
    });

    it("offers no create button to a member", async () => {
      stubStatus({ can_edit: false });
      await renderSignedIn(<GitHubAppPanel />);
      await screen.findByText(/Only the tenant/);
      expect(
        screen.queryByRole("button", { name: "Create GitHub App" }),
      ).not.toBeInTheDocument();
    });

    it("shows an App that is not installed yet, and sends the owner to the install page", async () => {
      const user = userEvent.setup();
      stubStatus({
        credential: connection({
          pending: true,
          installation_id: 0,
          app_slug: "slop-haus-abc",
        }),
      });
      vi.spyOn(apiClient, "getGitHubAppInstallURL").mockResolvedValue(
        "https://github.com/apps/slop-haus-abc/installations/new?state=s",
      );
      const redirect = vi
        .spyOn(navigation, "redirectBrowserTo")
        .mockImplementation(() => undefined);
      await renderSignedIn(<GitHubAppPanel />);

      expect(await screen.findByText("not installed yet")).toBeInTheDocument();
      expect(screen.getByText(/waiting for them/)).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Install the App" }));

      expect(redirect).toHaveBeenCalledWith(
        "https://github.com/apps/slop-haus-abc/installations/new?state=s",
      );
    });
  });
});
