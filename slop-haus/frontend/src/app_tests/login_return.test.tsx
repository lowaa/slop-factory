import { describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as api from "@/api";
import * as navigation from "@/lib/navigation";
import { apiClient, ApiError } from "@/api";
import { makeAuthConfig, makeProfile, makeTicket } from "@/test/fixtures";
import { clearPendingDevice } from "@/components/device_page";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { clickAccountLink } from "@/test/header";

// An anonymous visitor to a signed-in page is sent to /login?redirect=,
// and lands back on that page once signed in.
describe("returning to the page asked for after signing in", () => {
  const TICKET_PATH = "/tickets/7?from=blocked&tenant=tenant-a";

  function anonymous() {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "unauthorized"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig({ registration_open: true }));
  }

  function mockTicket() {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Deep" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
    vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
    vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
  }

  async function signInWithPassword() {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "login").mockResolvedValue(makeProfile({ tenant_id: "tenant-a" }));
    mockBoardShell();
    mockTicket();
    await user.type(await screen.findByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
  }

  const loginReturnPath = () => new URLSearchParams(window.location.search).get("redirect");

  it("sends an anonymous deep link to /login carrying it, and opens it once signed in", async () => {
    window.history.pushState({}, "", TICKET_PATH);
    anonymous();

    renderApp();
    await screen.findByRole("heading", { name: "Sign in" });
    expect(window.location.pathname).toBe("/login");
    expect(loginReturnPath()).toBe(TICKET_PATH);

    await signInWithPassword();

    expect(await screen.findByRole("heading", { name: "#7 Deep" })).toBeInTheDocument();
    expect(window.location.pathname + window.location.search).toBe(TICKET_PATH);
  });

  it("keeps /login plain for a visitor who asked for the board", async () => {
    window.history.pushState({}, "", "/");
    anonymous();

    renderApp();
    await screen.findByRole("heading", { name: "Sign in" });
    expect(window.location.pathname + window.location.search).toBe("/login");
  });

  it.each([
    ["another site", "//evil.example"],
    ["another site, behind a dot segment", "/.//evil.example"],
    ["an absolute URL", "https://evil.example/"],
    ["a sign-in page", "/register"],
  ])("lands on the board rather than a return path to %s", async (_name, redirect) => {
    window.history.pushState({}, "", `/login?${new URLSearchParams({ redirect })}`);
    anonymous();

    renderApp();
    await signInWithPassword();

    expect(await screen.findByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  // The pending flows are what the person was in the middle of on this
  // page, so they come before a return path.
  it("finishes a pending device visit before the return path", async () => {
    try {
      window.history.pushState({}, "", "/device?code=ABCD-EFGH");
      anonymous();
      vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);

      const { router } = renderApp();
      await screen.findByRole("heading", { name: "Sign in" });
      await act(() => router.navigate({ href: `/login?${new URLSearchParams({ redirect: "/workers" })}` }));
      expect(loginReturnPath()).toBe("/workers");

      await signInWithPassword();

      expect(await screen.findByLabelText("Device code")).toHaveValue("ABCD-EFGH");
      expect(window.location.pathname).toBe("/device");
    } finally {
      clearPendingDevice();
    }
  });

  // A session that ends mid-use returns to where it was once signed back
  // in; one ended on purpose leaves nothing for the next person.
  it("carries the page a session ended on to /login", async () => {
    window.history.pushState({}, "", TICKET_PATH);
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: "tenant-a" }));
    mockBoardShell();
    mockTicket();
    const onSignedOut = vi.spyOn(api, "onSignedOut");

    renderApp();
    await screen.findByRole("heading", { name: "#7 Deep" });
    act(() => onSignedOut.mock.calls[0][0]());

    await screen.findByRole("heading", { name: "Sign in" });
    expect(loginReturnPath()).toBe(TICKET_PATH);
  });

  it("does not carry the page to /login after a deliberate sign-out, nor into the next sign-in", async () => {
    window.history.pushState({}, "", TICKET_PATH);
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: "tenant-a" }));
    mockBoardShell();
    mockTicket();
    vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
    const user = userEvent.setup();

    renderApp();
    await screen.findByRole("heading", { name: "#7 Deep" });
    await clickAccountLink(user, "Sign out");

    await screen.findByRole("heading", { name: "Sign in" });
    expect(window.location.pathname + window.location.search).toBe("/login");

    await signInWithPassword();
    expect(await screen.findByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  // A provider sign-in leaves the page, so the return path goes to the
  // server as the round trip's next.
  it("hands the return path to a provider sign-in as its next", async () => {
    window.history.pushState({}, "", TICKET_PATH);
    anonymous();
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(
      makeAuthConfig({ providers: [{ name: "dex", display_name: "Dex" }] }),
    );
    const redirect = vi.spyOn(navigation, "redirectBrowserTo").mockImplementation(() => {});
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByRole("button", { name: "Continue with Dex" }));

    expect(redirect).toHaveBeenCalledWith(`/api/auth/login/dex?next=${encodeURIComponent(TICKET_PATH)}`);
  });

  it("starts a provider sign-in with no next for a return path to another site", async () => {
    window.history.pushState({}, "", `/login?${new URLSearchParams({ redirect: "//evil.example" })}`);
    anonymous();
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(
      makeAuthConfig({ providers: [{ name: "dex", display_name: "Dex" }] }),
    );
    const redirect = vi.spyOn(navigation, "redirectBrowserTo").mockImplementation(() => {});
    const user = userEvent.setup();

    renderApp();
    await user.click(await screen.findByRole("button", { name: "Continue with Dex" }));

    expect(redirect).toHaveBeenCalledWith("/api/auth/login/dex");
  });
});
