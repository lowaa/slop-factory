import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider, makeFakeAuthApi } from "../test/auth_pages_fixture";
import type { AuthPagesConfig } from "../config";
import ForgotPasswordPage from "./forgot_password_page";

function renderForgotPasswordPage(config: Partial<AuthPagesConfig> = {}) {
  render(
    <TestAuthPagesProvider config={config}>
      <ForgotPasswordPage />
    </TestAuthPagesProvider>,
  );
}

describe("ForgotPasswordPage", () => {
  it("submits the entered email to config.api.requestPasswordReset", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderForgotPasswordPage({ api });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset link" }));

    expect(api.requestPasswordReset).toHaveBeenCalledWith("person@example.com");
  });

  it("shows the resetLinkSent notice, echoing the address, after a successful submit", async () => {
    const user = userEvent.setup();
    renderForgotPasswordPage({ copy: { resetLinkSent: (email) => `Sent to ${email}.` } });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset link" }));

    expect(await screen.findByText("Sent to person@example.com.")).toBeInTheDocument();
  });

  it("shows the message from config.describeError on failure", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ requestPasswordReset: vi.fn().mockRejectedValue(new Error("boom")) });
    renderForgotPasswordPage({ api, describeError: () => "Something went wrong." });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset link" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Something went wrong.");
  });

  it("calls onError with the request-password-reset action on failure", async () => {
    const user = userEvent.setup();
    const err = new Error("boom");
    const api = makeFakeAuthApi({ requestPasswordReset: vi.fn().mockRejectedValue(err) });
    const onError = vi.fn();
    renderForgotPasswordPage({ api, onError });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset link" }));

    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith("request-password-reset", err);
  });

  it("navigates to login when 'back to sign in' is clicked", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    renderForgotPasswordPage({ navigate });

    await user.click(screen.getByRole("link"));

    expect(navigate).toHaveBeenCalledWith("/login");
  });
});
