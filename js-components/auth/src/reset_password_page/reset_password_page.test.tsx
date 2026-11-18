import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider, makeFakeAuthApi } from "../test/auth_pages_fixture";
import type { AuthPagesConfig } from "../config";
import ResetPasswordPage from "./reset_password_page";

function renderResetPasswordPage(config: Partial<AuthPagesConfig> = {}) {
  render(
    <TestAuthPagesProvider config={config}>
      <ResetPasswordPage />
    </TestAuthPagesProvider>,
  );
}

describe("ResetPasswordPage", () => {
  it("shows a missing-token error when the URL has no token fragment", () => {
    window.history.replaceState({}, "", "/reset-password");
    renderResetPasswordPage({ copy: { missingResetToken: "This password reset link is missing its token." } });

    expect(screen.getByRole("alert")).toHaveTextContent("This password reset link is missing its token.");
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
  });

  it("reads the token from the URL fragment and scrubs it", () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    renderResetPasswordPage();

    expect(window.location.hash).toBe("");
    expect(screen.getByLabelText("New password")).toBeInTheDocument();
  });

  it("submits the token and new password to config.api.resetPassword", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderResetPasswordPage({ api });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));

    expect(api.resetPassword).toHaveBeenCalledWith("abc123", "new-password");
  });

  it("shows success and a sign-in link after a successful reset", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderResetPasswordPage({ api });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));

    expect(await screen.findByRole("status")).toBeInTheDocument();
    expect(screen.getByRole("link")).toBeInTheDocument();
  });

  it("disables the submit button and relabels it while the request is in flight, and shows success only once it resolves", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    let resolveReset: () => void = () => {};
    const api = makeFakeAuthApi({
      resetPassword: vi.fn().mockReturnValue(
        new Promise<void>((resolve) => {
          resolveReset = resolve;
        }),
      ),
    });
    renderResetPasswordPage({ api });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));

    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
    // A broken implementation that flipped to the success view before
    // awaiting the API call would show this notice already -- it must not
    // appear until resetPassword actually resolves.
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    resolveReset();
    expect(await screen.findByRole("status")).toBeInTheDocument();
  });

  it("navigates to login when the post-success link is clicked", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    const navigate = vi.fn();
    renderResetPasswordPage({ api, navigate });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));
    await user.click(await screen.findByRole("link"));

    expect(navigate).toHaveBeenCalledWith("/login");
  });

  it("shows the message from config.describeError on failure", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ resetPassword: vi.fn().mockRejectedValue(new Error("boom")) });
    renderResetPasswordPage({ api, describeError: () => "invalid or expired token" });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid or expired token");
  });

  it("calls onError with the reset-password action on failure", async () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    const user = userEvent.setup();
    const err = new Error("boom");
    const api = makeFakeAuthApi({ resetPassword: vi.fn().mockRejectedValue(err) });
    const onError = vi.fn();
    renderResetPasswordPage({ api, onError });

    await user.type(screen.getByLabelText("New password"), "new-password");
    await user.click(screen.getByRole("button", { name: "Set new password" }));

    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith("reset-password", err);
  });

  it("applies passwordRules min/max length to the input", () => {
    window.history.replaceState({}, "", "/reset-password#token=abc123");
    renderResetPasswordPage({ passwordRules: { minLength: 10, maxLength: 72 } });

    const input = screen.getByLabelText("New password");
    expect(input).toHaveAttribute("minlength", "10");
    expect(input).toHaveAttribute("maxlength", "72");
  });
});
