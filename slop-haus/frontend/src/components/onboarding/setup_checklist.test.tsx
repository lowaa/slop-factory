import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { apiClient, type OnboardingStatus } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import SetupChecklist from "./setup_checklist";

const repos = { key: "repos", title: "Declare a repo", detail: "Workers clone it.", path: "/workspace", done: false };
const codeHost = { key: "code_host", title: "Connect a code host", detail: "PRs go through it.", path: "/git", done: false };

const serve = (status: OnboardingStatus) => vi.spyOn(apiClient, "getOnboardingStatus").mockResolvedValue(status);

describe("SetupChecklist", () => {
  it("lists each missing step with a link to where it is configured, and says no worker can do code work", async () => {
    serve({ steps: [repos, codeHost], code_work_ready: false });

    await renderSignedIn(<SetupChecklist refreshToken={0} />);

    const notice = await screen.findByRole("status", { name: "Setup" });
    expect(notice).toHaveTextContent("No worker can do code work");
    expect(screen.getByRole("link", { name: "Declare a repo" })).toHaveAttribute("href", "/workspace");
    expect(screen.getByRole("link", { name: "Connect a code host" })).toHaveAttribute("href", "/git");
  });

  it("keeps a step that is done as plain text and drops the code-work warning once something is set up", async () => {
    serve({ steps: [{ ...repos, done: true }, codeHost], code_work_ready: true });

    await renderSignedIn(<SetupChecklist refreshToken={0} />);

    await screen.findByRole("link", { name: "Connect a code host" });
    expect(screen.queryByRole("link", { name: "Declare a repo" })).toBeNull();
    expect(screen.getByText(/Declare a repo \(done\)/)).toBeInTheDocument();
    expect(screen.queryByText(/No worker can do code work/)).toBeNull();
  });

  it("shows nothing once every step is done", async () => {
    const spy = serve({ steps: [{ ...repos, done: true }, { ...codeHost, done: true }], code_work_ready: true });

    await renderSignedIn(<SetupChecklist refreshToken={0} />);

    await waitFor(() => expect(spy).toHaveBeenCalled());
    expect(screen.queryByRole("status", { name: "Setup" })).toBeNull();
  });

  it("shows nothing when the status cannot be read", async () => {
    const spy = vi.spyOn(apiClient, "getOnboardingStatus").mockRejectedValue(new Error("down"));

    await renderSignedIn(<SetupChecklist refreshToken={0} />);

    await waitFor(() => expect(spy).toHaveBeenCalled());
    expect(screen.queryByRole("status", { name: "Setup" })).toBeNull();
  });
});
