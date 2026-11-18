import { describe, expect, it, onTestFinished, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import App from "@/app";
import { createSlopHausRouter } from "@/routes";
import { makeAuthConfig, makeProfile } from "@/test/fixtures";
import { makeConsentStore } from "@/test/consent";
import { openAccountMenu } from "@/test/header";
import { serveTickets } from "@/test/serve_tickets";

const APP_RENDER = { timeout: 10_000 };

function openSignedIn(path: string) {
  window.history.pushState({}, "", path);
  vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
  serveTickets(() => []);
  vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
  vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMembers").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([]);
  vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());
  const router = createSlopHausRouter();
  onTestFinished(() => router.history.destroy());
  render(<App router={router} consent={makeConsentStore()} />);
}

describe("the Help routes in the app", () => {
  it("opens the index, with the guide listed", async () => {
    openSignedIn("/help");
    expect(await screen.findByRole("link", { name: /Running workers/ }, APP_RENDER)).toHaveAttribute("href", "/help/supervisor");
  });

  it("opens a guide page", async () => {
    openSignedIn("/help/supervisor");
    expect(await screen.findByRole("heading", { name: /Running workers/ }, APP_RENDER)).toBeInTheDocument();
  });

  it("has a Help link in the header", async () => {
    openSignedIn("/help/supervisor");
    await screen.findByRole("heading", { name: "slop-haus", level: 1 }, APP_RENDER);
    await openAccountMenu(userEvent.setup());
    expect(await screen.findByRole("link", { name: "Help" }, APP_RENDER)).toHaveAttribute("href", "/help");
  });
});
