import { onTestFinished, vi } from "vitest";
import { render } from "@testing-library/react";
import { apiClient } from "@/api";
import App from "@/app";
import { createSlopHausRouter } from "@/routes";
import { makeAuthConfig } from "@/test/fixtures";
import { makeConsentStore } from "@/test/consent";
import { serveTickets } from "@/test/serve_tickets";
import { preloadRoutes } from "@/shared/lazy_route/lazy_route";

// Top-level await: resolved before any test in the importing file starts, so
// the first navigation never waits on a route chunk.
await preloadRoutes();

// The real browser-history router, as main.tsx makes it, so these tests
// drive the address bar the way the app does. It hooks window.history
// until destroyed, so each test tears its own down.
export function renderApp() {
  const router = createSlopHausRouter();
  onTestFinished(() => router.history.destroy());
  return { ...render(<App router={router} consent={makeConsentStore()} />), router };
}

// Every route needs a session now, so the board shell's mount effects
// (tickets, the agent/member name lookups, the processing switch) all hit
// the network -- mocked here so an authenticated-board test isn't also
// exercising four other components' loading states.
export function mockBoardShell() {
  serveTickets(() => []);
  vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
  vi.spyOn(apiClient, "listProposals").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMembers").mockResolvedValue([]);
  vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });
  vi.spyOn(apiClient, "listInvites").mockResolvedValue([]);
  vi.spyOn(apiClient, "listGitHubWebhooks").mockResolvedValue({ enabled: true, deliveries: [] });
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([]);
  vi.spyOn(apiClient, "listArchivedTenants").mockResolvedValue([]);
  vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
  vi.spyOn(apiClient, "getSetting").mockResolvedValue({
    name: "ticket_processing_enabled",
    type: "bool",
    string_val: null,
    int_val: null,
    float_val: null,
    bool_val: true,
    is_default: true,
    updated_at: null,
  });
}
