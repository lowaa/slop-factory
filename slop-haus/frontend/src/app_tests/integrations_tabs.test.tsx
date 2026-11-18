import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile } from "@/test/fixtures";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { headerNav, openMoreMenu } from "@/test/header";

describe("integrations tabs", () => {
  it("opens Git at /git with the GitHub App and webhooks, and not Members' or Workspace's panels", async () => {
    window.history.pushState({}, "", "/git");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    vi.spyOn(apiClient, "getGitHubApp").mockResolvedValue({ enabled: false, can_edit: false, credential: null });
    mockBoardShell();
    const user = userEvent.setup();

    renderApp();

    expect(await screen.findByRole("heading", { name: "GitHub App" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "GitHub webhooks" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Invite someone" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Workspace", level: 2 })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Intake sources" })).not.toBeInTheDocument();
    // The tab is behind More, whose button reads as the page on show.
    const nav = await headerNav();
    expect(within(nav).getByRole("button", { name: "Git" })).toBeInTheDocument();
    await openMoreMenu(user);
    expect(screen.getByRole("link", { name: "Git" })).toHaveAttribute("aria-current", "page");
  });

  it("no longer shows the moved panels on Members", async () => {
    window.history.pushState({}, "", "/members");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();

    renderApp();

    expect(await screen.findByRole("heading", { name: "Invite someone" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub webhooks" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Intake sources" })).not.toBeInTheDocument();
  });

  it("opens Intake at /intake with the intake sources only", async () => {
    window.history.pushState({}, "", "/intake");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    vi.spyOn(apiClient, "listIntakeSources").mockResolvedValue({ enabled: true, can_edit: false, sources: [], deliveries: [] } as never);
    mockBoardShell();
    const user = userEvent.setup();

    renderApp();

    expect(await screen.findByRole("heading", { name: "Intake sources" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub webhooks" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub App" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Invite someone" })).not.toBeInTheDocument();
    const nav = await headerNav();
    expect(within(nav).getByRole("button", { name: "Intake" })).toBeInTheDocument();
    await openMoreMenu(user);
    expect(screen.getByRole("link", { name: "Intake" })).toHaveAttribute("aria-current", "page");
  });
});
