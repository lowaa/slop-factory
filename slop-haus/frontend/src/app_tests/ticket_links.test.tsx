import { beforeAll, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as navigation from "@/lib/navigation";
import { apiClient } from "@/api";
import { preloadTicketRoutes } from "@/components/ticket_detail";
import { makeAgentContext, makeConductor, makeHarnessConfig, makeProfile, makeTenant, makeTicket } from "@/test/fixtures";
import { serveTickets } from "@/test/serve_tickets";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { openMoreMenu } from "@/test/header";

describe("ticket links", () => {
  const SESSION_TENANT = "tenant-a";

  // The ticket page is a lazy route. Importing it here, under the longer hook
  // timeout, keeps the first test's cold transform+import (slow on a loaded CI
  // runner) from eating the findBy* timeout after its click.
  beforeAll(async () => {
    await preloadTicketRoutes();
  });

  function mockTicket(id: number, title: string) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id, title }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
    vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
    vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  }

  function signIn() {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: SESSION_TENANT }));
    mockBoardShell();
  }

  it("opens a ticket from its card at an address of its own, and Back returns to the board", async () => {
    window.history.pushState({}, "", "/");
    signIn();
    serveTickets(() => [makeTicket({ id: 7, title: "Linkable" })]);
    mockTicket(7, "Linkable");
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByText("Linkable"));

    expect(await screen.findByRole("heading", { name: "#7 Linkable" })).toBeInTheDocument();
    expect(window.location.pathname + window.location.search).toBe(`/tickets/7?from=board&tenant=${SESSION_TENANT}`);
    expect(screen.getByRole("link", { name: "Board" })).toHaveAttribute("aria-current", "page");

    await user.click(screen.getByRole("link", { name: "← Back to board" }));
    expect(await screen.findByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  it("opens a deep link to a ticket, rolled up under the tab it names", async () => {
    window.history.pushState({}, "", `/tickets/7?from=blocked&tenant=${SESSION_TENANT}`);
    signIn();
    mockTicket(7, "Deep");

    renderApp();

    expect(await screen.findByRole("heading", { name: "#7 Deep" })).toBeInTheDocument();
    expect(apiClient.getTicket).toHaveBeenCalledWith(7);
    expect(screen.getByRole("link", { name: "← Back to blocked" })).toHaveAttribute("href", "/blocked");
    expect(screen.getByRole("link", { name: "Blocked" })).toHaveAttribute("aria-current", "page");
  });

  it("groups the header tabs", async () => {
    window.history.pushState({}, "", "/");
    signIn();

    const user = userEvent.setup();

    const { container } = renderApp();

    const links = (name: string) =>
      within(screen.getByRole("group", { name })).getAllByRole("link").map((link) => link.textContent);
    await screen.findByRole("group", { name: "Tickets" });
    expect(links("Tickets")).toEqual(["Board", "Approvals", "Steward", "Blocked", "Unreviewed"]);
    expect(links("Agents")).toEqual(["Remote Control", "Workers", "Workspace"]);
    await openMoreMenu(user);
    expect(links("Settings")).toEqual(["Reports", "Compare", "Eval sets", "Members", "Tenant settings"]);
    expect(links("Integrations")).toEqual(["Git", "Intake"]);
    // Nothing outside the groups: the nav's links are exactly those fifteen.
    expect(within(container.querySelector(".app__nav") as HTMLElement).getAllByRole("link")).toHaveLength(15);
  });

  it("keeps a ?from=agents ticket link from before the tab was renamed, rolled up under Workers", async () => {
    window.history.pushState({}, "", `/tickets/7?from=agents&tenant=${SESSION_TENANT}`);
    signIn();
    mockTicket(7, "Old link");

    renderApp();

    expect(await screen.findByRole("heading", { name: "#7 Old link" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "← Back to workers" })).toHaveAttribute("href", "/workers");
    expect(screen.getByRole("link", { name: "Workers" })).toHaveAttribute("aria-current", "page");
  });

  it("redirects the old /agents path to /workers", async () => {
    window.history.pushState({}, "", "/agents");
    signIn();

    renderApp();

    expect(await screen.findByText("No workers yet.")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/workers");
  });

  it("opens a ticket link without a tenant in the session's tenant, going back to the board", async () => {
    window.history.pushState({}, "", "/tickets/7");
    signIn();
    mockTicket(7, "Plain");

    renderApp();

    expect(await screen.findByRole("heading", { name: "#7 Plain" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "← Back to board" })).toHaveAttribute("href", "/");
  });

  it("discusses a ticket in Remote Control: the shortcut opens a new session with the prompt in its message box, unsent", async () => {
    window.history.pushState({}, "", `/tickets/7?tenant=${SESSION_TENANT}`);
    signIn();
    mockTicket(7, "Talk");
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c-1", name: "my-laptop" })], others: [] });
    vi.spyOn(apiClient, "getConductorHost").mockResolvedValue({ online: true });
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
    const start = vi.spyOn(apiClient, "startConversation");
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByRole("button", { name: "Discuss in Remote Control" }));

    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
    expect(window.location.pathname).toBe("/remote-control");
    expect(start).not.toHaveBeenCalled();
  });

  // The ticket's own tab is in its address, so a reload or a pasted link
  // lands on it, and Back steps between the tabs before leaving the ticket.
  it("keeps the ticket's tab in its address", async () => {
    const MAIN = `?from=blocked&tenant=${SESSION_TENANT}`;
    const CONTEXT = `${MAIN}&view=agent-context`;
    window.history.pushState({}, "", `/tickets/7${MAIN}`);
    signIn();
    mockTicket(7, "Tabbed");
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([makeAgentContext({ id: 1, body: "where I got to" })]);
    const user = userEvent.setup();
    const contextTab = () => screen.getByRole("tab", { name: "Agent context history (1)" });

    renderApp();

    expect(await screen.findByRole("tab", { name: "Main" })).toHaveAttribute("aria-selected", "true");

    await user.click(contextTab());
    await waitFor(() => expect(window.location.search).toBe(CONTEXT));
    expect(contextTab()).toHaveAttribute("aria-selected", "true");
    expect(screen.getByLabelText("Add a comment")).not.toBeVisible();

    // Choosing the tab already on show adds nothing for Back to undo (the
    // router does not push a navigation to where it already is).
    await user.click(contextTab());

    act(() => window.history.back());
    await waitFor(() => expect(screen.getByLabelText("Add a comment")).toBeVisible());
    // The main tab is the default, so it leaves no trace in the address.
    expect(window.location.search).toBe(MAIN);

    act(() => window.history.forward());
    await waitFor(() => expect(contextTab()).toHaveAttribute("aria-selected", "true"));
    expect(window.location.search).toBe(CONTEXT);
  });

  it("opens a ticket on the tab its address names", async () => {
    window.history.pushState({}, "", `/tickets/7?tenant=${SESSION_TENANT}&view=agent-context`);
    signIn();
    mockTicket(7, "Deep tab");

    renderApp();

    expect(await screen.findByRole("tab", { name: "Agent context history" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("opens a ticket on its main tab when the address names no tab it has", async () => {
    window.history.pushState({}, "", `/tickets/7?tenant=${SESSION_TENANT}&view=nonsense`);
    signIn();
    mockTicket(7, "Unknown tab");

    renderApp();

    expect(await screen.findByRole("tab", { name: "Main" })).toHaveAttribute("aria-selected", "true");
  });

  it("sends an address that is not a ticket number to the board", async () => {
    window.history.pushState({}, "", "/tickets/seven");
    signIn();

    renderApp();

    expect(await screen.findByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  // The same number is a different ticket in another tenant, so a link
  // naming one is never opened here without asking.
  it("asks before switching to the tenant a ticket link names, and never shows this tenant's ticket of that number", async () => {
    window.history.pushState({}, "", "/tickets/7?from=board&tenant=tenant-b");
    signIn();
    mockTicket(7, "Tenant A's seven");
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: SESSION_TENANT, name: "Alpha", current: true }),
      makeTenant({ id: "tenant-b", name: "Beta", current: false }),
    ]);
    vi.spyOn(apiClient, "switchTenant").mockResolvedValue(makeProfile({ tenant_id: "tenant-b" }));
    const reloadPage = vi.spyOn(navigation, "reloadPage").mockImplementation(() => {});
    const user = userEvent.setup();

    renderApp();

    expect(await screen.findByRole("heading", { name: "Ticket #7 is in another tenant" })).toBeInTheDocument();
    const prompt = screen.getByRole("heading", { name: "Ticket #7 is in another tenant" }).parentElement as HTMLElement;
    expect(await within(prompt).findByText("Beta")).toBeInTheDocument();
    expect(within(prompt).getByText("Alpha")).toBeInTheDocument();
    expect(apiClient.getTicket).not.toHaveBeenCalled();
    expect(apiClient.switchTenant).not.toHaveBeenCalled();
    expect(screen.queryByText("Tenant A's seven")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Switch to Beta" }));

    expect(apiClient.switchTenant).toHaveBeenCalledExactlyOnceWith("tenant-b");
    await waitFor(() => expect(reloadPage).toHaveBeenCalledOnce());
    expect(window.location.pathname + window.location.search).toBe("/tickets/7?from=board&tenant=tenant-b");
  });

  it("explains, and offers no switch, when a ticket link names a tenant the user is not in", async () => {
    window.history.pushState({}, "", "/tickets/7?tenant=tenant-z");
    signIn();
    mockTicket(7, "Tenant A's seven");
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([
      makeTenant({ id: SESSION_TENANT, name: "Alpha", current: true }),
    ]);

    renderApp();

    expect(await screen.findByText(/tenant you are not a member of/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Switch to/ })).not.toBeInTheDocument();
    expect(apiClient.getTicket).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "← Back to board" })).toHaveAttribute("href", "/");
  });
});
