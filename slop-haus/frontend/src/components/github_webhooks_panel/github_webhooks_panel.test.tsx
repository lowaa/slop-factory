import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, ApiError, type GitHubWebhooks } from "@/api";
import { makeAppWebhook, makeGitHubDelivery } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import GitHubWebhooksPanel from "./github_webhooks_panel";

function stubWebhooks(overrides: Partial<GitHubWebhooks> = {}) {
  return vi.spyOn(apiClient, "listGitHubWebhooks").mockResolvedValue({
    enabled: true,
    deliveries: [],
    ...overrides,
  });
}

describe("GitHubWebhooksPanel", () => {
  // The App webhook section has its own tests (app_webhook_status.test.tsx);
  // here it is a server that does not receive App webhooks.
  beforeEach(() => {
    vi.spyOn(apiClient, "getGitHubAppWebhook").mockResolvedValue(makeAppWebhook({ state: "unavailable", expected_url: "" }));
  });

  it("lists deliveries", async () => {
    stubWebhooks({
      deliveries: [
        makeGitHubDelivery({
          id: "d1",
          event: "pull_request",
          action: "opened",
          outcome: "applied",
          detail: "moved ticket #7 to review",
        }),
      ],
    });

    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await screen.findByText("pull_request.opened")).toBeInTheDocument();
    expect(screen.getByText("applied")).toBeInTheDocument();
    expect(screen.getByText("moved ticket #7 to review")).toBeInTheDocument();
  });

  it("says so when there are no deliveries yet", async () => {
    stubWebhooks();

    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await screen.findByText("No deliveries yet.")).toBeInTheDocument();
  });

  // enabled: false means the server has no WEBHOOK_PUBLIC_BASE_URL (or no
  // credential key) -- nothing can be delivered.
  it("shows a notice when webhooks aren't configured on the server", async () => {
    stubWebhooks({ enabled: false });

    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await screen.findByText("GitHub webhooks are not configured on this server.")).toBeInTheDocument();
  });

  it("shows the server's error when the list cannot be loaded", async () => {
    vi.spyOn(apiClient, "listGitHubWebhooks").mockRejectedValue(new ApiError(500, "database is down"));

    await renderSignedIn(<GitHubWebhooksPanel />);

    expect(await screen.findByText("database is down")).toBeInTheDocument();
  });

  it("renders a ticket link for a delivery that names a ticket", async () => {
    stubWebhooks({ deliveries: [makeGitHubDelivery({ id: "d1", ticket_id: 42 })] });

    await renderSignedIn(<GitHubWebhooksPanel />);

    const link = await screen.findByRole("link", { name: "#42" });
    expect(link).toHaveAttribute("href", expect.stringContaining("/tickets/42"));
    expect(link).toHaveAttribute("href", expect.stringContaining("from=git"));
  });

  it("does not render a ticket link when a delivery has no ticket", async () => {
    stubWebhooks({ deliveries: [makeGitHubDelivery({ id: "d1", ticket_id: null })] });

    await renderSignedIn(<GitHubWebhooksPanel />);

    await screen.findByText("pull_request.opened");
    // The explanation paragraph always links to /account -- only a ticket
    // link (named "#<id>") is what this delivery must not have.
    expect(screen.queryByRole("link", { name: /^#\d+$/ })).not.toBeInTheDocument();
  });
});
