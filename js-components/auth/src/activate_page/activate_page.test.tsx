import { StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider, makeFakeAuthApi } from "../test/auth_pages_fixture";
import type { AuthPagesConfig } from "../config";
import ActivatePage from "./activate_page";

function renderActivatePage(config: Partial<AuthPagesConfig> = {}, strict = false) {
  const tree = (
    <TestAuthPagesProvider config={config}>
      <ActivatePage />
    </TestAuthPagesProvider>
  );
  render(strict ? <StrictMode>{tree}</StrictMode> : tree);
}

describe("ActivatePage", () => {
  it("shows a missing-token error when the URL has no token fragment", async () => {
    window.history.replaceState({}, "", "/activate");
    const api = makeFakeAuthApi();
    renderActivatePage({ api, copy: { missingActivationToken: "This activation link is missing its token." } });

    expect(await screen.findByRole("alert")).toHaveTextContent("This activation link is missing its token.");
    // Without a token there is nothing to activate -- a broken guard that
    // called activate() anyway (with null or an empty string) would still
    // show this same alert, so the API call itself is what's asserted.
    expect(api.activate).not.toHaveBeenCalled();
  });

  it("activates using the token from the URL fragment and shows success", async () => {
    window.history.replaceState({}, "", "/activate#token=abc123");
    const api = makeFakeAuthApi();
    renderActivatePage({ api });

    expect(screen.getByText("Activating…")).toBeInTheDocument();
    await waitFor(() => expect(api.activate).toHaveBeenCalledWith("abc123"));
    expect(await screen.findByRole("status")).toBeInTheDocument();
    expect(window.location.hash).toBe("");
    expect(window.location.search).toBe("");
  });

  // React's StrictMode double-invokes a mount effect in development to
  // surface effects that aren't idempotent -- activation is exactly that
  // (the token is single-use), so a naive effect would fire a second call
  // that the server correctly rejects as already-used. The ref guard in
  // activate_page.tsx exists for this.
  it("posts the activation exactly once under StrictMode's double-invoked effect", async () => {
    window.history.replaceState({}, "", "/activate#token=abc123");
    const api = makeFakeAuthApi();
    renderActivatePage({ api }, true);

    await screen.findByRole("status");
    expect(api.activate).toHaveBeenCalledTimes(1);
    expect(api.activate).toHaveBeenCalledWith("abc123");
  });

  it("shows config.describeError's message when activation fails", async () => {
    window.history.replaceState({}, "", "/activate#token=expired");
    const api = makeFakeAuthApi({ activate: vi.fn().mockRejectedValue(new Error("boom")) });
    renderActivatePage({ api, describeError: () => "invalid or expired token" });

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid or expired token");
  });

  it("calls onError with the activate action on failure", async () => {
    window.history.replaceState({}, "", "/activate#token=expired");
    const err = new Error("boom");
    const api = makeFakeAuthApi({ activate: vi.fn().mockRejectedValue(err) });
    const onError = vi.fn();
    renderActivatePage({ api, onError });

    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith("activate", err);
  });

  it("offers to sign in after a failed activation too", async () => {
    window.history.replaceState({}, "", "/activate#token=expired");
    const api = makeFakeAuthApi({ activate: vi.fn().mockRejectedValue(new Error("boom")) });
    renderActivatePage({ api });

    expect(await screen.findByRole("link")).toBeInTheDocument();
  });

  it("navigates to login when the link is clicked", async () => {
    window.history.replaceState({}, "", "/activate#token=abc123");
    const api = makeFakeAuthApi();
    const navigate = vi.fn();
    const user = userEvent.setup();
    renderActivatePage({ api, navigate });

    await user.click(await screen.findByRole("link"));

    expect(navigate).toHaveBeenCalledWith("/login");
  });
});
