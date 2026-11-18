// The root routing: which screen app.tsx shows for a given auth status,
// and the two ways a signed-in session ends up back at /login (an explicit
// sign-out, and onSignedOut firing mid-use).
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as api from "@/api";
import * as navigation from "@/lib/navigation";
import { apiClient, ApiError, type Status } from "@/api";
import { makeAuthConfig, makeConductor, makeConversation, makeConversationDetail, makeHarnessConfig, makeInvitePreview, makeProfile, makeProposal, makeSetting, makeTenant, makeTicket } from "@/test/fixtures";
import { clearPendingInvite } from "@/components/invite_page";
import { clearPendingAuthorizeRequest } from "@/components/authorize_page";
import { clearPendingDevice } from "@/components/device_page";
import { serveTickets } from "@/test/serve_tickets";
import { NO_EVENTS } from "@/test/recent_events";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { clickAccountLink, headerNav, clickMoreTab, openAccountMenu, openMoreMenu } from "@/test/header";

describe("App", () => {
  // The logger is told of the consent store's changes, so a session id
  // stored before the store found analytics refused is gone without any
  // record having been emitted.
  it("clears a stored logger session id once the consent store loads with analytics refused", async () => {
    const key = "slop-factory-logging:session-id";
    onTestFinished(() => sessionStorage.removeItem(key));
    window.history.pushState({}, "", "/legal/cookie_policy");
    sessionStorage.setItem(key, "stored-id");
    renderApp();
    await waitFor(() => expect(sessionStorage.getItem(key)).toBeNull());
  });

  // The legal documents are public: a visitor reads them before choosing,
  // signed in or not, and every page carries the footer to them.
  it("serves a legal document to a signed-out visitor, with the footer", async () => {
    window.history.pushState({}, "", "/legal/cookie_policy");
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    const documents = vi.fn(async (url: string, _init?: RequestInit) =>
        Response.json(
          url.includes("/versions")
            ? { kind: "cookie_policy", locale: "en", versions: [] }
            : { kind: "cookie_policy", version: "v1", locale: "en", effective: "2026-10-03", material: true, reviewed: false, current: true, body: "# Cookie policy\n\nText." },
        ),
    );
    vi.stubGlobal("fetch", documents);
    onTestFinished(() => void vi.unstubAllGlobals());

    renderApp();

    expect(await screen.findByRole("heading", { name: "Cookie policy" })).toBeInTheDocument();
    expect(screen.getByText(/DRAFT/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cookie preferences" })).toBeInTheDocument();
    // The version in force, and the history, from the public documents route,
    // sent without credentials.
    // (Not /version.json: the build tag is read at start.)
    const documentCalls = documents.mock.calls.filter(([url]) => url !== "/version.json");
    expect(documentCalls.map(([url]) => url).sort()).toEqual([
      "/api/privacy/documents/cookie_policy",
      "/api/privacy/documents/cookie_policy/versions",
    ]);
    expect(documentCalls.every(([, init]) => init?.credentials === "omit")).toBe(true);
  });

  it("shows the footer and cookie preferences on the account page", async () => {
    window.history.pushState({}, "", "/account");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({ password: { email: "ada@example.com", verified: true }, password_login_enabled: true, identities: [], reauth_required_after: "2026-01-01T00:00:00Z" });

    renderApp();

    expect(await screen.findByRole("heading", { name: "Cookie preferences" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Legal" })).toBeInTheDocument();
  });

  it("shows the login page when there is no session", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));

    renderApp();

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  it("shows the board when there is a session", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();

    renderApp();

    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "slop-haus" }).querySelector("img.brand-logo")).not.toBeNull();
    expect(screen.getByRole("heading", { name: "slop-haus" })).toBeInTheDocument();
  });

  it("badges the Approvals tab with how many proposals wait for the person, and none when none do", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([makeProposal({ id: "a" }), makeProposal({ id: "b", ticket_id: 8 })]);

    renderApp();

    const badge = await screen.findByRole("status", { name: "2 waiting for approval" });
    expect(screen.getByRole("link", { name: /Approvals/ })).toContainElement(badge);
  });

  it("opens Remote Control at /remote-control with a prefilled new session, which sends nothing", async () => {
    window.history.pushState({}, "", "/remote-control?draft=discuss%20slop-haus%20%237");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c-1", name: "my-laptop" })], others: [] });
    vi.spyOn(apiClient, "getConductorHost").mockResolvedValue({ online: true });
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
    const start = vi.spyOn(apiClient, "startConversation");

    renderApp();

    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
    expect(screen.getByRole("link", { name: "Remote Control" })).toHaveAttribute("aria-current", "page");
    expect(start).not.toHaveBeenCalled();
  });

  it("opens Remote Control on the connected session, and on a new one with ?new=1", async () => {
    window.history.pushState({}, "", "/approvals");
    window.history.pushState({}, "", "/remote-control");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c-1", name: "my-laptop" })], others: [] });
    vi.spyOn(apiClient, "getConductorHost").mockResolvedValue({ online: true });
    const live = makeConversation({ id: "88888888-8888-4888-8888-888888888888", conductor_id: "c-1", state: "ready" });
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([live]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail({ id: live.id }));
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    vi.spyOn(apiClient, "streamConversation").mockReturnValue(() => {});

    renderApp();

    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
    expect(window.location.search).toContain(`session=${live.id}`);
    expect(window.location.search).not.toContain("new");
    // The page chose the session itself, so Back leaves the page rather than
    // landing on the bare address, which would bounce forward again.
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/approvals"));
  });

  it("opens Remote Control on a new session at ?new=1 even with a connected one", async () => {
    window.history.pushState({}, "", "/remote-control?new=1");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c-1", name: "my-laptop" })], others: [] });
    vi.spyOn(apiClient, "getConductorHost").mockResolvedValue({ online: true });
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([makeConversation({ conductor_id: "c-1", state: "ready" })]);
    vi.spyOn(apiClient, "getHarnessConfig").mockResolvedValue(makeHarnessConfig());

    renderApp();

    expect(await screen.findByRole("region", { name: "New session" })).toBeInTheDocument();
    expect(window.location.search).toBe("?new=1");
  });

  it("opens the Approvals tab at /approvals", async () => {
    window.history.pushState({}, "", "/approvals");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    const pending = makeProposal({ id: "a", ticket_title: "Pick a database" });
    vi.spyOn(apiClient, "listProposals").mockResolvedValue([pending]);
    vi.spyOn(apiClient, "listPendingProposalsPage").mockResolvedValue({ proposals: [pending] });

    renderApp();

    expect(await screen.findByText(/Pick a database/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Approvals/ })).toHaveAttribute("aria-current", "page");
  });

  it("shows no badge when nothing is pending", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();

    renderApp();

    await screen.findByText("Ada Lovelace");
    await vi.waitFor(() => expect(apiClient.listProposals).toHaveBeenCalled());
    expect(screen.getByRole("link", { name: "Approvals" })).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: /waiting for approval/ })).toBeNull();
  });

  // A signed-in person landing on a public path (a bookmarked /login, say)
  // sees the board straight away rather than the login form for a tick --
  // see app.tsx's screenFor.
  it("shows the board, not the login form, when already signed in on a public path", async () => {
    window.history.pushState({}, "", "/login");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();

    renderApp();

    expect(await screen.findByRole("heading", { name: "slop-haus" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });

  // AuthProvider registers one callback for the whole app's lifetime with
  // api/index.ts's onSignedOut -- captured here via the spy rather than
  // driven through a real fetch/401 sequence, which api/index.ts's own
  // session.test.ts already covers in detail.
  it("returns to the login page with a notice when the session ends mid-use", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const onSignedOut = vi.spyOn(api, "onSignedOut");

    renderApp();
    await screen.findByRole("heading", { name: "slop-haus" });

    const listener = onSignedOut.mock.calls[0][0];
    act(() => listener());

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByText(/session has ended/i)).toBeInTheDocument();
  });

  // The invite page sends an anonymous visitor who already has an account
  // to /login (invite_page.tsx), and app.tsx's own screenFor is what has
  // to bring them back to /invite afterwards -- the URL itself says
  // nothing about the invite by the time they're on /login. Negative-run
  // by dropping the `hasPendingInviteToken()` check from screenFor: this
  // test then lands on the board instead, where it would otherwise pass.
  it("returns to a pending invite after signing in via the /login detour", async () => {
    // pending_invite.ts's token is module state (see its own doc comment
    // on why), and would otherwise leak into whichever test in this file
    // runs next -- the try/finally guarantees the cleanup runs even if an
    // assertion below fails, not just on the happy path.
    try {
      window.history.pushState({}, "", "/invite#token=abc123");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ email: "person@example.com", account_exists: true }),
      );
      const user = userEvent.setup();

      renderApp();
      // "You're invited" is the title in both the loading and ready states
      // (invite_page.tsx), so this alone doesn't prove the preview has
      // resolved -- the link clicked next is what does.
      await screen.findByRole("heading", { name: "You're invited" });

      await user.click(await screen.findByRole("link", { name: "Sign in" }));
      await screen.findByRole("heading", { name: "Sign in" });

      vi.spyOn(apiClient, "login").mockResolvedValue(makeProfile({ email: "person@example.com" }));
      mockBoardShell();
      await user.type(screen.getByLabelText("Email"), "person@example.com");
      await user.type(screen.getByLabelText("Password"), "hunter2pass");
      await user.click(screen.getByRole("button", { name: "Sign in" }));

      expect(await screen.findByRole("heading", { name: "You're invited" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "slop-haus" })).not.toBeInTheDocument();
    } finally {
      clearPendingInvite();
    }
  });

  // Mirrors the invite test above, for the consent page: an anonymous
  // visitor who follows an authorization URL is sent to /login, and
  // app.tsx's screenFor has to bring them back to /authorize -- with the
  // request id gone from the URL by then -- once they're signed in.
  // Negative-run by dropping the `hasPendingAuthorizeRequest()` check from
  // screenFor: this test then lands on the board instead.
  it("returns to the consent page after signing in via the /login detour", async () => {
    try {
      window.history.pushState({}, "", "/authorize?request=req-1");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
      vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue({
        client_name: "Claude Code",
        redirect_host: "127.0.0.1:9876",
      });
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
      const user = userEvent.setup();

      renderApp();
      await screen.findByRole("heading", { name: "Sign in" });

      vi.spyOn(apiClient, "login").mockResolvedValue(makeProfile());
      await user.type(screen.getByLabelText("Email"), "person@example.com");
      await user.type(screen.getByLabelText("Password"), "hunter2pass");
      await user.click(screen.getByRole("button", { name: "Sign in" }));

      expect(await screen.findByText(/wants to act as one of your agents/)).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "slop-haus" })).not.toBeInTheDocument();
    } finally {
      clearPendingAuthorizeRequest();
    }
  });

  // An expired consent request is a dead end, not a trap: once it has been
  // shown, leaving the page must reach the board rather than routing back.
  it("lets a signed-in user leave an expired consent request", async () => {
    try {
      window.history.pushState({}, "", "/authorize?request=req-gone");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
      mockBoardShell();
      vi.spyOn(apiClient, "getAuthRequest").mockRejectedValue(new ApiError(404, "expired"));

      renderApp();
      await screen.findByRole("alert");

      act(() => {
        window.history.pushState({}, "", "/");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      expect(await screen.findByRole("heading", { name: "slop-haus" })).toBeInTheDocument();
    } finally {
      clearPendingAuthorizeRequest();
    }
  });

  // Same shape for the device page, which needs no request id in the URL
  // at all -- a bare /device visit while signed out still has to come back
  // to /device, not the board, once signed in.
  it("returns to the device page after signing in via the /login detour", async () => {
    try {
      window.history.pushState({}, "", "/device");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);
      const user = userEvent.setup();

      renderApp();
      await screen.findByRole("heading", { name: "Sign in" });

      vi.spyOn(apiClient, "login").mockResolvedValue(makeProfile());
      await user.type(screen.getByLabelText("Email"), "person@example.com");
      await user.type(screen.getByLabelText("Password"), "hunter2pass");
      await user.click(screen.getByRole("button", { name: "Sign in" }));

      expect(await screen.findByLabelText("Device code")).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "slop-haus" })).not.toBeInTheDocument();
    } finally {
      clearPendingDevice();
    }
  });

  // Negative-run this one (drop the `profile.is_admin &&` guard in
  // app.tsx) to confirm it actually distinguishes the two profiles rather
  // than always rendering the link.
  it("shows the Admin link only for an admin profile", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ is_admin: true }));
    mockBoardShell();
    const user = userEvent.setup();

    renderApp();
    await screen.findByRole("heading", { name: "slop-haus" });
    await openAccountMenu(user);

    expect(await screen.findByRole("link", { name: "Admin" })).toHaveAttribute("href", "/admin/");
  });

  it("hides the Admin link for a non-admin profile", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ is_admin: false }));
    mockBoardShell();
    const user = userEvent.setup();

    renderApp();

    await screen.findByRole("heading", { name: "slop-haus" });
    await openAccountMenu(user);
    // The menu is open, so its other links are on show and Admin's absence means something.
    expect(screen.getByRole("link", { name: "Account" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Admin" })).not.toBeInTheDocument();
  });

  describe("the desktop header's menus", () => {
    const MORE_TABS = ["Git", "Intake", "Reports", "Compare", "Eval sets", "Members", "Tenant settings"];
    const BAR_TABS = ["Board", "Approvals", "Steward", "Blocked", "Unreviewed", "Remote Control", "Workers", "Workspace"];

    function signIn(path = "/") {
      window.history.pushState({}, "", path);
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
      mockBoardShell();
    }

    it("keeps Integrations and Settings behind More, and the other tabs on show", async () => {
      signIn();
      const user = userEvent.setup();

      renderApp();
      await screen.findByRole("heading", { name: "slop-haus" });

      for (const name of BAR_TABS) expect(screen.getByRole("link", { name })).toBeVisible();
      for (const name of MORE_TABS) expect(screen.queryByRole("link", { name })).not.toBeInTheDocument();

      await openMoreMenu(user);
      for (const name of MORE_TABS) expect(screen.getByRole("link", { name })).toBeVisible();
      for (const name of BAR_TABS) expect(screen.getByRole("link", { name })).toBeVisible();
    });

    it("reads the open More tab's label on the More button", async () => {
      signIn("/git");
      vi.spyOn(apiClient, "getGitHubApp").mockResolvedValue({ enabled: false, can_edit: false, credential: null });

      renderApp();

      const nav = await headerNav();
      const button = await within(nav).findByRole("button", { name: "Git" });
      expect(button).toHaveClass("header-menu__button--active");
      expect(within(nav).queryByRole("button", { name: "More" })).not.toBeInTheDocument();
    });

    it("holds Help, Account, New tenant and Sign out in the account menu", async () => {
      signIn();
      const user = userEvent.setup();

      renderApp();
      await screen.findByRole("heading", { name: "slop-haus" });

      const account = [
        () => screen.queryByRole("link", { name: "Help" }),
        () => screen.queryByRole("link", { name: "Account" }),
        () => screen.queryByRole("link", { name: "New tenant" }),
        () => screen.queryByRole("button", { name: "Sign out" }),
      ];
      for (const find of account) expect(find()).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Ada Lovelace" }));
      for (const find of account) expect(find()).toBeVisible();
    });
  });

  it("has a Members nav item alongside Agents", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const user = userEvent.setup();

    renderApp();
    await screen.findByRole("heading", { name: "slop-haus" });
    await clickMoreTab(user, "Members");

    expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
  });

  it("opens Tenant settings at /settings from the Settings group", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    vi.spyOn(apiClient, "listSettings").mockResolvedValue([makeSetting()]);
    vi.spyOn(apiClient, "listMyTenantSettings").mockResolvedValue([]);
    const user = userEvent.setup();

    renderApp();
    await screen.findByRole("heading", { name: "slop-haus" });
    await clickMoreTab(user, "Tenant settings");

    expect(await screen.findByRole("heading", { name: "This tenant" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings");
    // The menu closed on navigating, and its button now reads as the page on show.
    expect(within(await headerNav()).getByRole("button", { name: "Tenant settings" })).toHaveClass("header-menu__button--active");
    await openMoreMenu(user);
    expect(screen.getByRole("link", { name: "Tenant settings" })).toHaveAttribute("aria-current", "page");
  });

  // The header's Board tab (not only the detail view's own Back
  // button) must bring back a board that reflects what changed in the
  // detail view -- here, a Preparing ticket opened.
  it("shows a ticket's new status on the board after leaving its detail view by the Board tab", async () => {
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    let status: Status = "preparing";
    const ticket = () => makeTicket({ id: 3, title: "Shape me", status, can_ready: status === "preparing" });
    serveTickets(() => [ticket()]);
    vi.spyOn(apiClient, "getTicket").mockImplementation(() => Promise.resolve(ticket()));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
    vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
    vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    vi.spyOn(apiClient, "markTicketReady").mockImplementation(() => {
      status = "open";
      return Promise.resolve(ticket());
    });
    const user = userEvent.setup();
    const findColumn = async (name: string) =>
      (await screen.findByRole("heading", { name })).closest(".board__column") as HTMLElement;

    renderApp();
    await user.click(within(await findColumn("Preparing")).getByText("Shape me"));
    await user.click(await screen.findByRole("button", { name: "Open" }));
    await waitFor(() => expect(apiClient.markTicketReady).toHaveBeenCalled());
    await user.click(screen.getByRole("link", { name: "Board" }));

    expect(within(await findColumn("Open")).getByText("Shape me")).toBeInTheDocument();
    expect(within(await findColumn("Preparing")).queryByText("Shape me")).not.toBeInTheDocument();
  });

  it("signs out via the header button and returns to the login page", async () => {
    const user = userEvent.setup();
    window.history.pushState({}, "", "/");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ display_name: "Ada Lovelace" }));
    mockBoardShell();
    const logout = vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);

    renderApp();
    await screen.findByText("Ada Lovelace");

    await clickAccountLink(user, "Sign out");

    expect(logout).toHaveBeenCalled();
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  describe("login providers and the account page", () => {
    const dex = makeAuthConfig({ providers: [{ name: "dex", display_name: "Dex" }] });

    it("offers a button per configured provider on the sign-in page, which starts that provider's sign-in", async () => {
      window.history.pushState({}, "", "/login");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(dex);
      const redirect = vi.spyOn(navigation, "redirectBrowserTo").mockImplementation(() => {});
      const user = userEvent.setup();

      renderApp();

      await user.click(await screen.findByRole("button", { name: "Continue with Dex" }));
      expect(redirect).toHaveBeenCalledWith("/api/auth/login/dex");
      expect(screen.getByLabelText("Password")).toBeInTheDocument();
    });

    // Negative-run by rendering the page with no providers regardless of
    // /api/auth/config: the button's absence must fail the test above,
    // and this one must still pass.
    it("offers no provider button when none is configured", async () => {
      window.history.pushState({}, "", "/login");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());

      renderApp();

      await screen.findByRole("heading", { name: "Sign in" });
      expect(screen.queryByRole("button", { name: /^Continue with/ })).not.toBeInTheDocument();
    });

    it("hides the password form when the server turns password sign-in off", async () => {
      window.history.pushState({}, "", "/login");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue({ ...dex, password_login: false });

      renderApp();

      expect(await screen.findByRole("button", { name: "Continue with Dex" })).toBeInTheDocument();
      expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    });

    it("explains a refused provider sign-in on the sign-in page", async () => {
      window.history.pushState({}, "", "/login?error=no_account");
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(dex);

      renderApp();

      expect(await screen.findByRole("alert")).toHaveTextContent("There’s no account here for that Dex sign-in.");
      expect(window.location.search).toBe("");
    });

    it("opens the account page from the header, and a tab takes the person back", async () => {
      window.history.pushState({}, "", "/");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ email: "ada@example.com" }));
      mockBoardShell();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
        password: { email: "ada@example.com", verified: true },
        identities: [],
        password_login_enabled: true,
        reauth_required_after: "2026-01-01T00:00:00Z",
      });
      const user = userEvent.setup();

      renderApp();

      await screen.findByRole("heading", { name: "slop-haus" });
      await clickAccountLink(user, "Account");
      expect(window.location.pathname).toBe("/account");
      expect(await screen.findByRole("heading", { name: "Sign-in methods" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Change password" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "Board" }));
      expect(window.location.pathname).toBe("/");
      expect(await screen.findByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
    });

    it("lands a refused link back on the account page with the reason", async () => {
      window.history.pushState({}, "", "/account?error=identity_in_use");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(dex);
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue({
        password: { email: "ada@example.com", verified: true },
        identities: [],
        password_login_enabled: true,
        reauth_required_after: "2026-01-01T00:00:00Z",
      });

      renderApp();

      expect(await screen.findByRole("alert")).toHaveTextContent("That Dex account is already linked to a different account here.");
      expect(window.location.pathname).toBe("/account");
    });
  });

  // The route guards' URL corrections (routes.tsx), which the component
  // tests above mostly see only as what ends up on screen.
  describe("routing", () => {
    const signInMethods = {
      password: { email: "ada@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    };

    it("replaces, rather than pushes, the entry an anonymous visitor is redirected away from", async () => {
      window.history.pushState({}, "", "/account");
      const lengthBefore = window.history.length;
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));

      renderApp();

      await screen.findByRole("heading", { name: "Sign in" });
      expect(window.location.pathname).toBe("/login");
      expect(window.history.length).toBe(lengthBefore);
    });

    it("corrects a signed-in visitor's /login to /", async () => {
      window.history.pushState({}, "", "/login");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();

      renderApp();

      await screen.findByRole("heading", { name: "File a ticket" });
      expect(window.location.pathname).toBe("/");
    });

    it.each([
      ["signed in", "/"],
      ["signed out", "/login"],
    ])("sends an unknown path to %s's landing page", async (who, landing) => {
      window.history.pushState({}, "", "/no-such-page");
      if (who === "signed in") {
        vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
        mockBoardShell();
      } else {
        vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      }

      renderApp();

      await waitFor(() => expect(window.location.pathname).toBe(landing));
    });

    // Signed out, every sign-in page is reachable; signed in, only the two
    // an emailed link can land on mid-token are.
    it.each([
      ["/register", "Create an account"],
      ["/forgot-password", "Reset your password"],
      ["/activate", "Activate your account"],
      ["/reset-password", "Set a new password"],
    ])("shows %s to a signed-out visitor", async (path, title) => {
      window.history.pushState({}, "", path);
      vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));

      renderApp();

      expect(await screen.findByRole("heading", { name: title })).toBeInTheDocument();
      expect(window.location.pathname).toBe(path);
    });

    it.each([
      ["/activate", "Activate your account"],
      ["/reset-password", "Set a new password"],
    ])("still shows %s to a signed-in visitor", async (path, title) => {
      window.history.pushState({}, "", path);
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();

      renderApp();

      expect(await screen.findByRole("heading", { name: title })).toBeInTheDocument();
      expect(window.location.pathname).toBe(path);
    });

    it.each(["/register", "/forgot-password"])("sends a signed-in visitor on %s to the board", async (path) => {
      window.history.pushState({}, "", path);
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();

      renderApp();

      await screen.findByRole("heading", { name: "File a ticket" });
      expect(window.location.pathname).toBe("/");
    });

    // With no request id and nothing pending, the consent page is still
    // the page to show -- it explains what is missing -- rather than the
    // board. Negative-run by sending /authorize with nothing pending on to
    // the board.
    it("shows a signed-in visitor /authorize even with no request id", async () => {
      window.history.pushState({}, "", "/authorize");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();

      renderApp();

      expect(await screen.findByText("This authorization link is missing its request id.")).toBeInTheDocument();
      expect(window.location.pathname).toBe("/authorize");
    });

    // A pending consent request is finished before a device sign-in, as
    // screenFor ordered them. Negative-run by dropping /authorize from the
    // flows ahead of /device.
    it("finishes a pending consent request before showing /device", async () => {
      try {
        window.history.pushState({}, "", "/authorize?request=req-1");
        vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
        vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());
        renderApp();
        await screen.findByRole("heading", { name: "Sign in" });
        cleanup();

        window.history.pushState({}, "", "/device");
        vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
        mockBoardShell();
        vi.spyOn(apiClient, "getAuthRequest").mockResolvedValue({
          client_name: "Claude Code",
          redirect_host: "127.0.0.1:9876",
        });
        renderApp();

        expect(await screen.findByRole("heading", { name: "Authorize agent access" })).toBeInTheDocument();
        expect(window.location.pathname).toBe("/authorize");
      } finally {
        clearPendingAuthorizeRequest();
        clearPendingDevice();
      }
    });

    // AppShell renders once more with no profile before the guard re-runs.
    // A crash there would be caught by the router's error boundary and then
    // covered by the redirect, so the console is what shows it. Negative-run
    // by dropping AppShell's profile check.
    it("returns to the login page when the session ends on the account page", async () => {
      window.history.pushState({}, "", "/account");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue(signInMethods);
      const onSignedOut = vi.spyOn(api, "onSignedOut");

      const consoleError = vi.spyOn(console, "error");

      renderApp();
      await screen.findByRole("heading", { name: "Sign-in methods" });

      act(() => onSignedOut.mock.calls[0][0]());

      expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
      expect(window.location.pathname).toBe("/login");
      expect(consoleError).not.toHaveBeenCalled();
    });

    // The whole tab is replaced, so nothing under it (a board with approve
    // buttons) can act in the tenant the session has since moved to.
    it("replaces the tab with a notice when the session moves to another tenant", async () => {
      window.history.pushState({}, "", "/account");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue(signInMethods);
      vi.spyOn(apiClient, "listTenants").mockResolvedValue([
        makeTenant({ id: "tenant-a", name: "Alpha", current: false }),
        makeTenant({ id: "tenant-b", name: "Beta", current: true }),
      ]);
      const onTenantChanged = vi.spyOn(api, "onTenantChanged");

      renderApp();
      await screen.findByRole("heading", { name: "Sign-in methods" });
      act(() => onTenantChanged.mock.calls[0][0]("tenant-a"));

      expect(await screen.findByRole("button", { name: "Reload into Beta" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Sign-in methods" })).not.toBeInTheDocument();
    });

    it("replaces the tab with a notice when the server says its tenant was archived", async () => {
      window.history.pushState({}, "", "/account");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue(signInMethods);
      vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ id: "tenant-b", name: "Beta", current: false })]);
      const onApiFailure = vi.spyOn(api, "onApiFailure");

      renderApp();
      await screen.findByRole("heading", { name: "Sign-in methods" });
      act(() => {
        for (const [listener] of onApiFailure.mock.calls) listener({ method: "GET", route: "/api/tickets", status: 403, code: "tenant_archived" });
      });

      expect(await screen.findByRole("button", { name: "Switch to Beta" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Sign-in methods" })).not.toBeInTheDocument();
    });

    // Each tab is an address of its own, so Back from the account page
    // returns to the tab that was open, not always the board.
    it("keeps the open tab across a visit to the account page and Back", async () => {
      window.history.pushState({}, "", "/");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      vi.spyOn(apiClient, "listSignInMethods").mockResolvedValue(signInMethods);
      const user = userEvent.setup();

      renderApp();
      await user.click(await screen.findByRole("link", { name: "Workers" }));
      await screen.findByText("No workers yet.");
      expect(window.location.pathname).toBe("/workers");
      await clickAccountLink(user, "Account");
      await screen.findByRole("heading", { name: "Sign-in methods" });

      act(() => window.history.back());

      expect(await screen.findByText("No workers yet.")).toBeInTheDocument();
      await waitFor(() => expect(window.location.pathname).toBe("/workers"));
    });

    it("moves between tabs with the browser's Back and Forward", async () => {
      window.history.pushState({}, "", "/");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();
      const user = userEvent.setup();

      renderApp();
      await user.click(await screen.findByRole("link", { name: "Workers" }));
      await screen.findByText("No workers yet.");
      await clickMoreTab(user, "Members");
      await screen.findByText("No outstanding invites.");

      act(() => window.history.back());
      expect(await screen.findByText("No workers yet.")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Workers" })).toHaveAttribute("aria-current", "page");

      act(() => window.history.forward());
      expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
      expect(window.location.pathname).toBe("/members");
    });

    it("opens the tab its address names on a fresh load", async () => {
      window.history.pushState({}, "", "/members");
      vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
      mockBoardShell();

      renderApp();

      expect(await screen.findByText("No outstanding invites.")).toBeInTheDocument();
      expect(within(await headerNav()).getByRole("button", { name: "Members" })).toBeInTheDocument();
      await openMoreMenu(userEvent.setup());
      expect(screen.getByRole("link", { name: "Members" })).toHaveAttribute("aria-current", "page");
      // Only that tab: "/" is a prefix of every path, so Board must not match fuzzily.
      expect(screen.getByRole("link", { name: "Board" })).not.toHaveAttribute("aria-current");
    });
  });
});
