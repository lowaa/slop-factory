import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider } from "../test/auth_pages_fixture";
import { FakeAuthError, fakeErrorCode } from "../test/fake_sign_in_methods_api";
import type { AuthPagesConfig } from "../config";
import { agreeExtras } from "../test/sign_up_extras_fixture";
import InviteAccept, { type InviteAcceptProps } from "./invite_accept";

const dex: Partial<AuthPagesConfig> = {
  providers: [{ name: "dex", displayName: "Dex" }],
  errorCode: fakeErrorCode,
};

function renderInvite(props: Partial<InviteAcceptProps> = {}, config: Partial<AuthPagesConfig> = dex) {
  const onAcceptWithPassword = vi.fn().mockResolvedValue(undefined);
  const redirect = vi.fn();
  render(
    <TestAuthPagesProvider config={{ redirect, ...config }}>
      <InviteAccept onAcceptWithPassword={onAcceptWithPassword} {...props} />
    </TestAuthPagesProvider>,
  );
  return { onAcceptWithPassword, redirect };
}

describe("InviteAccept", () => {
  it("accepts with the password typed", async () => {
    const user = userEvent.setup();
    const { onAcceptWithPassword } = renderInvite();

    await user.type(screen.getByLabelText("Choose a password"), "a-good-password");
    await user.click(screen.getByRole("button", { name: "Accept invite" }));

    expect(onAcceptWithPassword).toHaveBeenCalledWith("a-good-password");
  });

  it("renders the app's password-only fields inside the same form", async () => {
    const user = userEvent.setup();
    renderInvite({
      passwordFields: (
        <label>
          Display name <input />
        </label>
      ),
    });

    await user.type(screen.getByLabelText("Display name"), "Ada");
    expect(screen.getByLabelText("Display name").closest("form")).toBe(screen.getByLabelText("Choose a password").closest("form"));
  });

  // The provider path doesn't read passwordFields at all -- onContinueWithProvider
  // only ever gets the provider's name -- so a person who typed into one and
  // then chose a provider must be told it went nowhere, not left to wonder.
  it("notes that a provider ignores the password-only fields, when there is one to ignore", () => {
    renderInvite({
      onContinueWithProvider: vi.fn(),
      passwordFields: (
        <label>
          Display name <input />
        </label>
      ),
    });

    expect(screen.getByText(/uses its own name/)).toBeInTheDocument();
  });

  it("says nothing about a provider ignoring fields when there are none to ignore", () => {
    renderInvite({ onContinueWithProvider: vi.fn() });

    expect(screen.queryByText(/uses its own name/)).not.toBeInTheDocument();
  });

  it("starts the provider round trip without needing the password-only fields filled in", async () => {
    const user = userEvent.setup();
    const onContinueWithProvider = vi.fn().mockResolvedValue("https://dex.example.test/auth");
    const { redirect } = renderInvite({
      onContinueWithProvider,
      passwordFields: (
        <label>
          Display name <input required />
        </label>
      ),
    });

    await user.click(screen.getByRole("button", { name: "Continue with Dex" }));

    expect(onContinueWithProvider).toHaveBeenCalledWith("dex");
    expect(redirect).toHaveBeenCalledWith("https://dex.example.test/auth");
  });

  it("offers no provider when the app gives no way to start one", () => {
    renderInvite();

    expect(screen.queryByRole("button", { name: "Continue with Dex" })).not.toBeInTheDocument();
  });

  it("continues with a provider by sending the browser where the app's start answered", async () => {
    const user = userEvent.setup();
    const onContinueWithProvider = vi.fn().mockResolvedValue("https://dex.example.test/auth");
    const { redirect, onAcceptWithPassword } = renderInvite({ onContinueWithProvider });

    await user.click(screen.getByRole("button", { name: "Continue with Dex" }));

    expect(onContinueWithProvider).toHaveBeenCalledWith("dex");
    expect(redirect).toHaveBeenCalledWith("https://dex.example.test/auth");
    expect(onAcceptWithPassword).not.toHaveBeenCalled();
  });

  it("explains a refusal by its code", async () => {
    const user = userEvent.setup();
    renderInvite({
      onAcceptWithPassword: vi.fn().mockRejectedValue(new FakeAuthError("password_exists")),
    });

    await user.type(screen.getByLabelText("Choose a password"), "a-good-password");
    await user.click(screen.getByRole("button", { name: "Accept invite" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("This account already has a password.");
    expect(screen.getByRole("button", { name: "Accept invite" })).not.toBeDisabled();
  });

  it("shows a refused provider round trip's code", () => {
    renderInvite({ callbackError: "identity_in_use", onContinueWithProvider: vi.fn() });

    expect(screen.getByRole("alert")).toHaveTextContent("That Dex account is already linked to a different account here.");
  });

  it("offers only providers when the app turns password sign-in off", () => {
    renderInvite({ onContinueWithProvider: vi.fn() }, { ...dex, passwordLogin: false });

    expect(screen.getByRole("button", { name: "Continue with Dex" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Choose a password")).not.toBeInTheDocument();
  });

  describe("with sign-up extras", () => {
    it("holds both ways in back until complete, and hands the values to either", async () => {
      const user = userEvent.setup();
      const onContinueWithProvider = vi.fn().mockResolvedValue("https://idp.example/auth");
      const { onAcceptWithPassword, redirect } = renderInvite(
        { onContinueWithProvider },
        { ...dex, signUpExtras: agreeExtras },
      );

      expect(screen.getByRole("button", { name: "Continue with Dex" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Accept invite" })).toBeDisabled();

      await user.click(screen.getByLabelText("I agree"));
      await user.click(screen.getByRole("button", { name: "Continue with Dex" }));
      expect(onContinueWithProvider).toHaveBeenCalledWith("dex", { agreed: true });
      expect(redirect).toHaveBeenCalledWith("https://idp.example/auth");

      await user.type(screen.getByLabelText("Choose a password"), "a-good-password");
      await user.click(screen.getByRole("button", { name: "Accept invite" }));
      expect(onAcceptWithPassword).toHaveBeenCalledWith("a-good-password", { agreed: true });
    });

    it("does not accept with incomplete extras when the form is submitted without the button", async () => {
      const user = userEvent.setup();
      const { onAcceptWithPassword } = renderInvite({}, { ...dex, signUpExtras: agreeExtras });
      await user.type(screen.getByLabelText("Choose a password"), "a-good-password");

      fireEvent.submit(screen.getByLabelText("Choose a password").closest("form")!);

      expect(onAcceptWithPassword).not.toHaveBeenCalled();
    });

    it("shows the extras on a provider-only invite too", () => {
      renderInvite({ onContinueWithProvider: vi.fn() }, { ...dex, passwordLogin: false, signUpExtras: agreeExtras });
      expect(screen.getByLabelText("I agree")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Continue with Dex" })).toBeDisabled();
    });
  });

  it("shows the shared fields with the password form and with the provider buttons", async () => {
    const user = userEvent.setup();
    renderInvite({
      sharedFields: <input aria-label="Workspace name" />,
      onContinueWithProvider: vi.fn().mockResolvedValue("https://dex.test/auth"),
    });
    expect(screen.getByLabelText("Workspace name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Dex/ })).toBeInTheDocument();
    await user.type(screen.getByLabelText("Choose a password"), "a-good-password");
    expect(screen.getByLabelText("Workspace name")).toBeInTheDocument();
  });

  it("shows the shared fields when passwords are off", () => {
    renderInvite({ sharedFields: <input aria-label="Workspace name" /> }, { ...dex, passwordLogin: false });
    expect(screen.getByLabelText("Workspace name")).toBeInTheDocument();
  });
});
