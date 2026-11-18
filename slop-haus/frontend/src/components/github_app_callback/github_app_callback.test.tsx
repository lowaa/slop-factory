import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import { renderSignedIn } from "@/test/render_signed_in";
import GitHubAppCallback from "./github_app_callback";

afterEach(() => vi.restoreAllMocks());

const credential = {
  app_id: 1,
  app_slug: "slop-haus-abc",
  installation_id: 0,
  pending: true,
  key_fingerprint: "f".repeat(64),
  created_at: "2026-10-05T12:00:00Z",
  updated_at: "2026-10-05T12:00:00Z",
  verified_at: null,
};

describe("GitHubAppCallback", () => {
  it("hands GitHub's code and state to the server once, then goes on to the install page", async () => {
    const complete = vi
      .spyOn(apiClient, "completeGitHubAppManifest")
      .mockResolvedValue({
        credential,
        install_url:
          "https://github.com/apps/slop-haus-abc/installations/new?state=i",
      });
    const redirect = vi
      .spyOn(navigation, "redirectBrowserTo")
      .mockImplementation(() => undefined);

    await renderSignedIn(
      <StrictMode>
        <GitHubAppCallback
          step="created"
          search={{ code: "abc123", state: "st" }}
        />
      </StrictMode>,
    );

    await vi.waitFor(() =>
      expect(redirect).toHaveBeenCalledWith(
        "https://github.com/apps/slop-haus-abc/installations/new?state=i",
      ),
    );
    expect(complete).toHaveBeenCalledTimes(1);
    expect(complete).toHaveBeenCalledWith("abc123", "st");
  });

  it("hands the installation to the server, verifies, and returns to the workspace", async () => {
    const complete = vi
      .spyOn(apiClient, "completeGitHubAppInstall")
      .mockResolvedValue({
        credential: { ...credential, installation_id: 777, pending: false },
        requested: false,
      });
    const verify = vi
      .spyOn(apiClient, "verifyGitHubApp")
      .mockResolvedValue({ account: "acme", repos: [], manifest: [] });

    const { router } = await renderSignedIn(
      <GitHubAppCallback
        step="installed"
        search={{
          installation_id: "777",
          setup_action: "install",
          state: "st",
        }}
      />,
    );

    await vi.waitFor(() => expect(verify).toHaveBeenCalled());
    expect(complete).toHaveBeenCalledWith({
      installation_id: 777,
      setup_action: "install",
      state: "st",
    });
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/git"),
    );
  });

  it("says it is waiting for an org owner when the install was only requested", async () => {
    vi.spyOn(apiClient, "completeGitHubAppInstall").mockResolvedValue({
      credential,
      requested: true,
    });
    const verify = vi.spyOn(apiClient, "verifyGitHubApp");

    await renderSignedIn(
      <GitHubAppCallback
        step="installed"
        search={{ setup_action: "request", state: "st" }}
      />,
    );

    expect(
      await screen.findByText(/Waiting for an org owner to approve/),
    ).toBeInTheDocument();
    expect(verify).not.toHaveBeenCalled();
  });

  it("goes back to Git from the waiting-for-approval notice", async () => {
    vi.spyOn(apiClient, "completeGitHubAppInstall").mockResolvedValue({
      credential,
      requested: true,
    });

    const { router } = await renderSignedIn(
      <GitHubAppCallback
        step="installed"
        search={{ setup_action: "request", state: "st" }}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Back to Git" }));

    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/git"));
  });

  it("shows a refusal and does not go on", async () => {
    vi.spyOn(apiClient, "completeGitHubAppManifest").mockRejectedValue(
      new ApiError(400, "the setup link is invalid"),
    );
    const redirect = vi
      .spyOn(navigation, "redirectBrowserTo")
      .mockImplementation(() => undefined);

    await renderSignedIn(
      <GitHubAppCallback
        step="created"
        search={{ code: "abc123", state: "old" }}
      />,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "the setup link is invalid",
    );
    expect(redirect).not.toHaveBeenCalled();
  });

  it("goes back to Git from a refusal", async () => {
    vi.spyOn(apiClient, "completeGitHubAppManifest").mockRejectedValue(
      new ApiError(400, "the setup link is invalid"),
    );

    const { router } = await renderSignedIn(
      <GitHubAppCallback step="created" search={{ code: "abc123", state: "old" }} />,
    );
    await screen.findByRole("alert");
    await userEvent.click(screen.getByRole("button", { name: "Back to Git" }));

    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/git"));
  });

  it("treats an installation id that is not a whole number as none", async () => {
    const complete = vi
      .spyOn(apiClient, "completeGitHubAppInstall")
      .mockRejectedValue(new ApiError(422, "no installation id"));
    await renderSignedIn(
      <GitHubAppCallback
        step="installed"
        search={{ installation_id: "7; drop", state: "st" }}
      />,
    );
    await screen.findByRole("alert");
    expect(complete).toHaveBeenCalledWith({
      installation_id: 0,
      setup_action: "",
      state: "st",
    });
  });
});
