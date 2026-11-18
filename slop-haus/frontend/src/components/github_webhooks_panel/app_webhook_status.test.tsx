import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type GitHubAppWebhook } from "@/api";
import { makeAppWebhook } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import GitHubWebhooksPanel from "./github_webhooks_panel";

function stubWebhook(overrides: Partial<GitHubAppWebhook> = {}) {
  vi.spyOn(apiClient, "listGitHubWebhooks").mockResolvedValue({ enabled: true, deliveries: [] });
  return vi.spyOn(apiClient, "getGitHubAppWebhook").mockResolvedValue(makeAppWebhook(overrides));
}

async function appSection() {
  return within(await screen.findByRole("region", { name: "GitHub App webhook" }));
}

describe("App webhook status", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("says receiving only once a pull request event has arrived", async () => {
    stubWebhook({
      state: "receiving",
      last_delivery: { event: "pull_request_review", action: "submitted", outcome: "applied", received_at: "2026-10-02T10:00:00Z" },
      last_event: { event: "pull_request_review", action: "submitted", outcome: "applied", received_at: "2026-10-02T10:00:00Z" },
    });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(await app.findByText(/^Receiving\./)).toBeInTheDocument();
    // Expected and set-on-GitHub, which agree.
    expect(app.getAllByText("https://hooks.example/hooks/github-app", { selector: "dd", exact: false })).toHaveLength(2);
  });

  it("tells the owner exactly which events to tick while none has arrived", async () => {
    stubWebhook({ state: "awaiting_events" });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(await app.findByText(/^Not done\. The App's webhook is set/)).toBeInTheDocument();
    expect(app.getByText("Pull request")).toBeInTheDocument();
    expect(app.getByText("Pull request review")).toBeInTheDocument();
    expect(app.getByText("Active")).toBeInTheDocument();
    expect(app.queryByText(/^Receiving\./)).toBeNull();
  });

  it.each([
    ["url_differs", /^Not done\. The URL set on GitHub is not this environment's/],
    ["not_applied", /^Not done\. slop-haus has not set/],
    ["shared_installation", /Another workspace on this server already receives/],
  ] as const)("shows the %s state", async (state, text) => {
    stubWebhook({ state });
    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await (await appSection()).findByText(text)).toBeInTheDocument();
  });

  it("offers no actions where the server has no App door or the App is not connected", async () => {
    stubWebhook({ state: "unavailable", expected_url: "" });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(await app.findByText(/does not receive GitHub App webhooks/)).toBeInTheDocument();
    expect(app.queryByRole("button")).toBeNull();
  });

  it("tells an owner on a server with a door to connect an App, and shows no facts or actions", async () => {
    stubWebhook({ state: "unavailable", configured_url: "" });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(await app.findByText(/Connect and install a GitHub App above/)).toBeInTheDocument();
    expect(app.queryByText("Expected URL")).toBeNull();
    expect(app.queryByRole("button")).toBeNull();
  });

  it("hides the actions from a member and says why", async () => {
    stubWebhook({ can_edit: false });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(await app.findByText(/Only an owner can check or rotate/)).toBeInTheDocument();
    expect(app.queryByRole("button", { name: "Check" })).toBeNull();
  });

  it("does not offer actions for an installation another workspace owns", async () => {
    stubWebhook({ state: "shared_installation", can_edit: true });
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    await app.findByText(/Another workspace/);
    expect(app.queryByRole("button", { name: "Check" })).toBeNull();
  });

  it("Check shows what the server answered", async () => {
    stubWebhook({ state: "url_differs" });
    const check = vi.spyOn(apiClient, "checkGitHubAppWebhook").mockResolvedValue({
      outcome: "applied",
      webhook: makeAppWebhook({ state: "awaiting_events" }),
    });
    const user = userEvent.setup();
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    await user.click(await app.findByRole("button", { name: "Check" }));

    expect(check).toHaveBeenCalledWith(false);
    expect(await app.findByText(/^Not done\. The App's webhook is set/)).toBeInTheDocument();
    expect(app.getByText("GitHub's webhook was set to this environment's URL.")).toBeInTheDocument();
  });

  it("offers to take the webhook over when it points at another host, and asks for it explicitly", async () => {
    stubWebhook({ state: "not_applied" });
    const check = vi
      .spyOn(apiClient, "checkGitHubAppWebhook")
      .mockRejectedValueOnce(new ApiError(409, "the App's webhook URL points elsewhere", "github_app_webhook_foreign"))
      .mockResolvedValueOnce({ outcome: "applied", webhook: makeAppWebhook({ state: "awaiting_events" }) });
    const user = userEvent.setup();
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    expect(app.queryByRole("button", { name: "Take over the webhook" })).toBeNull();
    await user.click(await app.findByRole("button", { name: "Check" }));
    expect(await app.findByRole("alert")).toHaveTextContent("points elsewhere");

    await user.click(app.getByRole("button", { name: "Take over the webhook" }));

    expect(check).toHaveBeenLastCalledWith(true);
    expect(await app.findByText(/^Not done\. The App's webhook is set/)).toBeInTheDocument();
  });

  it("Rotate asks for the password again on reauth_required and then rotates", async () => {
    stubWebhook();
    const rotate = vi
      .spyOn(apiClient, "rotateGitHubAppWebhookSecret")
      .mockRejectedValueOnce(new ApiError(403, "confirm it's you", "reauth_required"))
      .mockResolvedValueOnce(makeAppWebhook());
    vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
      password: { email: "person@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    });
    const reauth = vi.spyOn(apiClient, "reauthWithPassword").mockResolvedValue(undefined);
    const user = userEvent.setup();
    await renderSignedIn(<GitHubWebhooksPanel />);

    const app = await appSection();
    await user.click(await app.findByRole("button", { name: "Rotate secret" }));
    await user.type(await screen.findByLabelText("Current password"), "correct-password");
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    await app.findByText("The webhook secret was replaced.");
    expect(reauth).toHaveBeenCalledWith("correct-password");

    expect(await app.findByText("The webhook secret was replaced.")).toBeInTheDocument();
    expect(rotate).toHaveBeenCalledTimes(2);
  });

  it("shows a load failure", async () => {
    vi.spyOn(apiClient, "listGitHubWebhooks").mockResolvedValue({ enabled: true, deliveries: [] });
    vi.spyOn(apiClient, "getGitHubAppWebhook").mockRejectedValue(new ApiError(500, "database is down"));
    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await (await appSection()).findByRole("alert")).toHaveTextContent("database is down");
  });
});
