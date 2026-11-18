import { describe, expect, it, onTestFinished, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeAttempt, makeAuthConfig, makeProfile, makeTicket } from "@/test/fixtures";
import { makeConsentStore } from "@/test/consent";
import { serveTickets } from "@/test/serve_tickets";
import App from "./app";
import { createSlopHausRouter } from "./routes";

const TENANT = "tenant-a";

// These render the whole app, so first paint pulls in the full module graph;
// under a loaded CI runner that can pass the 1s default of findBy*/waitFor.
const APP_RENDER = { timeout: 10_000 };

function renderApp() {
  const router = createSlopHausRouter();
  onTestFinished(() => router.history.destroy());
  return render(<App router={router} consent={makeConsentStore()} />);
}

function signInTo(path: string) {
  window.history.pushState({}, "", path);
  vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: TENANT }));
  serveTickets(() => []);
  vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
  vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMembers").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([]);
  vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());
  vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
  vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
  vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
  vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
  vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([]);
  vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
  vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  vi.spyOn(apiClient, "getTicketLlmUsage").mockResolvedValue({ worker: [], guidance: [] });
  vi.spyOn(apiClient, "getTicket").mockResolvedValue(
    makeTicket({
      id: 42,
      title: "Two ways",
      attempts: [makeAttempt({ number: 1 }), makeAttempt({ number: 2, promoted: false })],
    }),
  );
}

// The test timeout (5s default) must outlast the waits above, or it cuts them short.
describe("a ticket's attempt address", { timeout: 15_000 }, () => {
  it("opens /tickets/42/attempts/2 on attempt 2's tab", async () => {
    signInTo(`/tickets/42/attempts/2?tenant=${TENANT}`);

    renderApp();

    expect(await screen.findByRole("heading", { name: "Attempt 2" }, APP_RENDER)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Attempt 2/ })).toHaveAttribute("aria-current", "page");
  });

  it("moves between the tabs by address, and Back steps between them", async () => {
    signInTo(`/tickets/42?tenant=${TENANT}`);
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByRole("link", { name: /Attempt 1/ }, APP_RENDER));

    expect(await screen.findByRole("heading", { name: "Attempt 1" }, APP_RENDER)).toBeInTheDocument();
    expect(window.location.pathname).toBe("/tickets/42/attempts/1");

    await user.click(screen.getByRole("link", { name: "Ticket" }));
    await waitFor(() => expect(window.location.pathname).toBe("/tickets/42"), APP_RENDER);
    expect(screen.queryByRole("heading", { name: "Attempt 1" })).not.toBeInTheDocument();
  });

  it.each(["two", "0", "-1", "007", "1e3"])("does not match the attempt %s", async (n) => {
    signInTo(`/tickets/42/attempts/${n}?tenant=${TENANT}`);

    renderApp();

    await waitFor(() => expect(window.location.pathname).not.toBe(`/tickets/42/attempts/${n}`), APP_RENDER);
    expect(screen.queryByRole("heading", { name: /Attempt/ })).not.toBeInTheDocument();
  });
});
