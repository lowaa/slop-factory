import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, type ManifestRepo } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import BatchingStatus from "./batching_status";

function stubRepos(repos: ManifestRepo[] | null) {
  vi.spyOn(apiClient, "getWorkspace").mockResolvedValue({
    manifest: repos && { repos, revision: 1, updated_at: "2026-10-31T00:00:00Z" },
    can_edit: true,
    proposal: null,
    agents: [],
    divergences: [],
  });
}

const REPO = { path: "mono", remote: "github.com/acme/mono" };

describe("BatchingStatus", () => {
  it("says batching is not active when no repo turned the switch on, and links to the manifest", async () => {
    stubRepos([{ ...REPO, independent_apps: ["a/"], skip_retest_across_apps: true }]);
    await renderSignedIn(<BatchingStatus batchSize={2} />);

    expect(await screen.findByText("not active")).toBeInTheDocument();
    expect(screen.getByText(/Batch merges within an app/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "workspace manifest" })).toHaveAttribute("href", "/workspace");
  });

  it("says it is not active when the switch is on but no app is declared", async () => {
    stubRepos([{ ...REPO, batch_within_apps: true }]);
    await renderSignedIn(<BatchingStatus batchSize={2} />);

    expect(await screen.findByText("not active")).toBeInTheDocument();
  });

  it("says it is not active with no manifest", async () => {
    stubRepos(null);
    await renderSignedIn(<BatchingStatus batchSize={3} />);

    expect(await screen.findByText("not active")).toBeInTheDocument();
  });

  it("says nothing batches when two repos turned it on", async () => {
    stubRepos([
      { ...REPO, independent_apps: ["a/"], batch_within_apps: true },
      { path: "other", remote: "github.com/acme/other", independent_apps: ["b/"], batch_within_apps: true },
    ]);
    await renderSignedIn(<BatchingStatus batchSize={2} />);

    expect(await screen.findByText("not active")).toBeInTheDocument();
    expect(screen.getByText(/more than one repo/)).toBeInTheDocument();
  });

  it("counts the apps when active", async () => {
    stubRepos([{ ...REPO, independent_apps: ["a/", "b/"], batch_within_apps: true }]);
    await renderSignedIn(<BatchingStatus batchSize={2} />);

    expect(await screen.findByText("active")).toBeInTheDocument();
    expect(screen.getByText(/for 2 apps/)).toBeInTheDocument();
  });

  it("is plain at batch size 1", async () => {
    stubRepos([{ ...REPO, independent_apps: ["a/"], batch_within_apps: true }]);
    await renderSignedIn(<BatchingStatus batchSize={1} />);

    expect(await screen.findByText(/every change lands alone/)).toBeInTheDocument();
  });
});
