import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "@/context/auth_context";
import { apiClient, ApiError } from "@/api";
import * as navigation from "@/lib/navigation";
import {
  makeAuthConfig,
  makeInvitePreview,
  makeProfile,
  makeTenant,
} from "@/test/fixtures";
import { WithAuthPagesConfig } from "@/test/with_auth_pages";
import type { AuthPagesConfig } from "@slop-factory/auth";
import InvitePage from "./invite_page";
import { clearPendingInvite, hasPendingInviteToken } from "./pending_invite";

function renderInvitePage(
  onNavigate = vi.fn(),
  config?: Partial<AuthPagesConfig>,
) {
  render(
    <AuthProvider>
      <WithAuthPagesConfig config={config}>
        <InvitePage onNavigate={onNavigate} />
      </WithAuthPagesConfig>
    </AuthProvider>,
  );
  return { onNavigate };
}

const withDex = (
  redirect: (url: string) => void,
): Partial<AuthPagesConfig> => ({
  providers: [{ name: "dex", displayName: "Dex" }],
  redirect,
});

function anonymous() {
  vi.spyOn(apiClient, "me").mockRejectedValue(
    new ApiError(401, "unauthorized"),
  );
  vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(
    makeAuthConfig({ registration_open: true }),
  );
}

function signedInAs(email: string) {
  vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ email }));
  vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(
    makeAuthConfig({ registration_open: true }),
  );
}

describe("InvitePage", () => {
  let reloadSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // pending_invite.ts keeps the token in module scope on purpose (see its
    // doc comment) so it survives a real detour through /login -- which
    // means it also survives between test cases in this file unless
    // cleared explicitly. Every test that cares about a token sets its own
    // fresh URL fragment anyway (which readToken always prefers over a
    // stashed one), but the missing-token test does not, so a leftover
    // token from an earlier test would otherwise leak into it.
    clearPendingInvite();
    reloadSpy = vi
      .spyOn(navigation, "reloadToBoard")
      .mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows a missing-token error when the URL has no token fragment", async () => {
    window.history.replaceState({}, "", "/invite");
    anonymous();
    renderInvitePage();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "This invite link is missing its token.",
    );
  });

  // The emailed link is "#token=..." in the fragment, same reasoning as
  // @slop-factory/auth's readAndClearToken (never a query string, never in
  // a Referer header),
  // and it is scrubbed from the address bar immediately.
  it("previews using the token from the URL fragment and scrubs it from the address bar", async () => {
    window.history.replaceState({}, "", "/invite#token=abc123");
    anonymous();
    vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
      makeInvitePreview({ account_exists: false }),
    );
    renderInvitePage();

    expect(
      await screen.findByRole("button", { name: "Create account" }),
    ).toBeInTheDocument();
    expect(apiClient.previewInvite).toHaveBeenCalledWith("abc123");
    expect(window.location.hash).toBe("");
  });

  // React 18 StrictMode double-invokes lazy useState initializers in
  // development -- readToken (pending_invite.ts) is written to tolerate
  // that the same way @slop-factory/auth's own token reader is: the second
  // call sees an already-cleared hash and falls back to the value the first
  // call stashed, rather than reporting "missing token".
  it("still resolves the token under StrictMode's double-invoked initializer", async () => {
    window.history.replaceState({}, "", "/invite#token=abc123");
    anonymous();
    vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
      makeInvitePreview({ account_exists: false }),
    );

    render(
      <StrictMode>
        <AuthProvider>
          <WithAuthPagesConfig>
            <InvitePage onNavigate={vi.fn()} />
          </WithAuthPagesConfig>
        </AuthProvider>
      </StrictMode>,
    );

    expect(
      await screen.findByRole("button", { name: "Create account" }),
    ).toBeInTheDocument();
    expect(apiClient.previewInvite).toHaveBeenCalledWith("abc123");
  });

  it("shows the server's error message for an invalid or expired token", async () => {
    window.history.replaceState({}, "", "/invite#token=dead");
    anonymous();
    vi.spyOn(apiClient, "previewInvite").mockRejectedValue(
      new ApiError(400, "invalid or expired token"),
    );
    renderInvitePage();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "invalid or expired token",
    );
  });

  describe("no account yet", () => {
    it("submits the create-account form and lands on the board", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({
          account_exists: false,
          email: "new@example.com",
          invited_by: "Ada",
        }),
      );
      vi.spyOn(apiClient, "acceptInviteAsNewAccount").mockResolvedValue(
        makeProfile(),
      );
      const user = userEvent.setup();
      renderInvitePage();

      await user.type(await screen.findByLabelText("Display name"), "Bea");
      await user.type(screen.getByLabelText("Password"), "a-strong-password");
      await user.click(screen.getByRole("button", { name: "Create account" }));

      expect(apiClient.acceptInviteAsNewAccount).toHaveBeenCalledWith(
        "abc123",
        "Bea",
        "a-strong-password",
      );
      await vi.waitFor(() => expect(reloadSpy).toHaveBeenCalled());
    });

    it("shows the create-account form regardless of who else is signed in", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      signedInAs("someone-else@example.com");
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false, email: "new@example.com" }),
      );
      renderInvitePage();

      expect(
        await screen.findByRole("button", { name: "Create account" }),
      ).toBeInTheDocument();
    });

    // The display name asked for above the password is only sent by the
    // password path (see "submits the create-account form and lands on the
    // board" above); a provider names the account itself, so this checks
    // both halves of that split -- the field is offered, but never demanded,
    // when continuing with a provider instead.
    it("notes that a provider names the account itself, next to its button", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false }),
      );
      renderInvitePage(vi.fn(), withDex(vi.fn()));

      expect(await screen.findByLabelText("Display name")).toBeInTheDocument();
      expect(screen.getByText(/uses its own name/)).toBeInTheDocument();
    });

    it("starts the provider round trip with the display name left blank", async () => {
      window.history.replaceState({}, "", "/invite#token=tok-provider");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false }),
      );
      const start = vi
        .spyOn(apiClient, "startInviteWithProvider")
        .mockResolvedValue("http://dex.test/auth");
      const redirect = vi.fn();
      const user = userEvent.setup();
      renderInvitePage(vi.fn(), withDex(redirect));

      await user.click(
        await screen.findByRole("button", { name: "Continue with Dex" }),
      );

      expect(start).toHaveBeenCalledWith("dex", "tok-provider", "/");
      expect(redirect).toHaveBeenCalledWith("http://dex.test/auth");
      expect(screen.getByLabelText("Display name")).toHaveValue("");
    });
  });

  describe("account already exists", () => {
    it("offers to sign in when not signed in, and keeps the token pending", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({
          account_exists: true,
          email: "existing@example.com",
        }),
      );
      const user = userEvent.setup();
      const { onNavigate } = renderInvitePage();

      await user.click(await screen.findByRole("link", { name: "Sign in" }));

      expect(onNavigate).toHaveBeenCalledWith("/login");
      // Navigating to sign in does not spend or clear the pending token --
      // app.tsx's own screenFor is what brings the visitor back here once
      // they're signed in (see hasPendingInviteToken's doc comment).
      expect(hasPendingInviteToken()).toBe(true);
    });

    it("offers an Accept button when signed in as the invited address", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      signedInAs("existing@example.com");
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({
          account_exists: true,
          email: "existing@example.com",
          tenant_name: "Ada's workspace",
        }),
      );
      vi.spyOn(apiClient, "acceptInvite").mockResolvedValue(makeProfile());
      const user = userEvent.setup();
      renderInvitePage();

      await user.click(await screen.findByRole("button", { name: "Accept" }));

      expect(apiClient.acceptInvite).toHaveBeenCalledWith("abc123");
      await vi.waitFor(() => expect(reloadSpy).toHaveBeenCalled());
    });

    it("offers to sign out when signed in as a different address", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      signedInAs("someone-else@example.com");
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({
          account_exists: true,
          email: "existing@example.com",
        }),
      );
      vi.spyOn(apiClient, "logout").mockResolvedValue(undefined);
      const user = userEvent.setup();
      renderInvitePage();

      expect(
        await screen.findByText(/signed in as someone-else@example\.com/),
      ).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Sign out" }));

      expect(apiClient.logout).toHaveBeenCalled();
      // Still on the invite page, now offering to sign in as the right one.
      expect(
        await screen.findByRole("link", { name: "Sign in" }),
      ).toBeInTheDocument();
    });
  });

  describe("with a login provider", () => {
    it("creates the account through the provider: a POST with the token, then off to where it answers", async () => {
      window.history.replaceState({}, "", "/invite#token=tok-provider");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false }),
      );
      const start = vi
        .spyOn(apiClient, "startInviteWithProvider")
        .mockResolvedValue("http://dex.test/auth?x=1");
      const redirect = vi.fn();
      const user = userEvent.setup();
      renderInvitePage(vi.fn(), withDex(redirect));

      await user.click(
        await screen.findByRole("button", { name: "Continue with Dex" }),
      );

      expect(start).toHaveBeenCalledWith("dex", "tok-provider", "/");
      expect(redirect).toHaveBeenCalledWith("http://dex.test/auth?x=1");
    });

    it("offers the provider to an existing account that isn't signed in", async () => {
      window.history.replaceState({}, "", "/invite#token=tok-existing");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: true }),
      );
      const start = vi
        .spyOn(apiClient, "startInviteWithProvider")
        .mockResolvedValue("http://dex.test/auth");
      const user = userEvent.setup();
      renderInvitePage(vi.fn(), withDex(vi.fn()));

      await user.click(
        await screen.findByRole("button", { name: "Continue with Dex" }),
      );

      expect(start).toHaveBeenCalledWith("dex", "tok-existing", "/");
    });

    it("back from a refused provider, says why and how to try again", async () => {
      window.history.replaceState({}, "", "/invite?error=identity_in_use");
      anonymous();
      renderInvitePage(vi.fn(), withDex(vi.fn()));

      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(
        "That Dex account is already linked to a different account here.",
      );
      expect(alert).toHaveTextContent(
        "Open the link in your invitation email again",
      );
      expect(window.location.search).toBe("");
    });
  });

  describe("naming the workspace a new_tenant invite makes", () => {
    it("pre-fills a new account's workspace name from its display name and sends it", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false, kind: "new_tenant" }),
      );
      vi.spyOn(apiClient, "acceptInviteAsNewAccount").mockResolvedValue(
        makeProfile(),
      );
      const user = userEvent.setup();
      renderInvitePage();

      await user.type(await screen.findByLabelText("Display name"), "Bea");
      expect(screen.getByLabelText("Workspace name")).toHaveValue("Bea");
      await user.type(screen.getByLabelText("Password"), "a-strong-password");
      await user.click(screen.getByRole("button", { name: "Create account" }));

      expect(apiClient.acceptInviteAsNewAccount).toHaveBeenCalledWith(
        "abc123",
        "Bea",
        "a-strong-password",
        "Bea",
      );
    });

    it("sends the name the person wrote, not the display name", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false, kind: "new_tenant" }),
      );
      vi.spyOn(apiClient, "acceptInviteAsNewAccount").mockResolvedValue(
        makeProfile(),
      );
      const user = userEvent.setup();
      renderInvitePage();

      await user.type(await screen.findByLabelText("Display name"), "Bea");
      await user.clear(screen.getByLabelText("Workspace name"));
      await user.type(screen.getByLabelText("Workspace name"), "Bea's lab");
      await user.type(screen.getByLabelText("Password"), "a-strong-password");
      await user.click(screen.getByRole("button", { name: "Create account" }));

      expect(apiClient.acceptInviteAsNewAccount).toHaveBeenCalledWith(
        "abc123",
        "Bea",
        "a-strong-password",
        "Bea's lab",
      );
    });

    it("asks a join_tenant invitee for no name", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      anonymous();
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({ account_exists: false }),
      );
      renderInvitePage();

      await screen.findByLabelText("Display name");
      expect(screen.queryByLabelText("Workspace name")).not.toBeInTheDocument();
    });

    it("starts an existing account from a name that doesn't repeat one of its workspaces, and shows a refusal", async () => {
      window.history.replaceState({}, "", "/invite#token=abc123");
      vi.spyOn(apiClient, "me").mockResolvedValue(
        makeProfile({ email: "e@example.com", display_name: "Ada" }),
      );
      vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(
        makeAuthConfig({ registration_open: true }),
      );
      vi.spyOn(apiClient, "previewInvite").mockResolvedValue(
        makeInvitePreview({
          account_exists: true,
          email: "e@example.com",
          kind: "new_tenant",
        }),
      );
      vi.spyOn(apiClient, "listTenants").mockResolvedValue([
        makeTenant({ name: "ada" }),
      ]);
      vi.spyOn(apiClient, "acceptInvite").mockRejectedValue(
        new ApiError(409, "you already belong to a workspace with that name"),
      );
      const user = userEvent.setup();
      renderInvitePage();

      const field = await screen.findByLabelText("Workspace name");
      await vi.waitFor(() => expect(field).toHaveValue("Ada 2"));
      await user.click(screen.getByRole("button", { name: "Accept" }));

      expect(apiClient.acceptInvite).toHaveBeenCalledWith("abc123", "Ada 2");
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "already belong to a workspace with that name",
      );
      expect(reloadSpy).not.toHaveBeenCalled();
    });
  });
});
