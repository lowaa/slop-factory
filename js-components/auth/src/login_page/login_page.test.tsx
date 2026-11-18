import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider, makeFakeAuthApi } from "../test/auth_pages_fixture";
import { FakeAuthError, fakeErrorCode } from "../test/fake_sign_in_methods_api";
import type { AuthAction, AuthPagesConfig } from "../config";
import LoginPage from "./login_page";

function renderLoginPage(
  config: Partial<AuthPagesConfig> = {},
  notice: string | null = null,
  callbackError: string | null = null,
) {
  render(
    <TestAuthPagesProvider config={config}>
      <LoginPage notice={notice} callbackError={callbackError} />
    </TestAuthPagesProvider>,
  );
}

const withProviders: Partial<AuthPagesConfig> = {
  providers: [
    { name: "dex", displayName: "Dex" },
    { name: "github", displayName: "GitHub" },
  ],
  providerSignInUrl: (name) => `/auth/login/${name}`,
};

describe("LoginPage", () => {
  it("submits entered credentials to config.api.signIn", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderLoginPage({ api });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(api.signIn).toHaveBeenCalledWith("person@example.com", "hunter2pass");
  });

  it("shows the message from config.describeError on a failed sign-in", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ signIn: vi.fn().mockRejectedValue(new Error("boom")) });
    renderLoginPage({ api, describeError: () => "invalid email or password" });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid email or password");
    expect(screen.getByRole("button", { name: "Sign in" })).not.toBeDisabled();
  });

  it("calls onError with the sign-in action and the error", async () => {
    const user = userEvent.setup();
    const err = new Error("boom");
    const api = makeFakeAuthApi({ signIn: vi.fn().mockRejectedValue(err) });
    const onError = vi.fn<(action: AuthAction, error: unknown) => void>();
    renderLoginPage({ api, onError });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith("sign-in", err);
  });

  it("disables the submit button and relabels it while the request is in flight", async () => {
    const user = userEvent.setup();
    let resolveSignIn: () => void = () => {};
    const api = makeFakeAuthApi({
      signIn: vi.fn().mockReturnValue(
        new Promise<void>((resolve) => {
          resolveSignIn = resolve;
        }),
      ),
    });
    renderLoginPage({ api });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(screen.getByRole("button", { name: "Signing in…" })).toBeDisabled();
    resolveSignIn();
  });

  it("navigates to the register route when the sign-up link is clicked", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    renderLoginPage({ navigate, registrationOpen: true });

    await user.click(await screen.findByRole("link", { name: "Create one" }));

    expect(navigate).toHaveBeenCalledWith("/register");
  });

  // Negative-run this one first (flip registrationOpen, or drop the
  // condition around the link in login_page.tsx) to confirm it actually
  // distinguishes the two states.
  it("hides the sign-up link when registration is closed", async () => {
    renderLoginPage({ registrationOpen: false });

    await screen.findByRole("heading", { name: "Sign in" });
    expect(screen.queryByRole("link", { name: "Create one" })).not.toBeInTheDocument();
  });

  it("navigates to the forgot-password route when the link is clicked", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    renderLoginPage({ navigate });

    await user.click(screen.getByRole("link", { name: "Forgot your password?" }));

    expect(navigate).toHaveBeenCalledWith("/forgot-password");
  });

  it("shows the session-ended notice when passed one", () => {
    renderLoginPage({}, "Your session has ended — sign in again.");

    expect(screen.getByRole("status")).toHaveTextContent(/session has ended/i);
  });

  it("shows no notice by default", () => {
    renderLoginPage();

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("applies copy overrides", () => {
    renderLoginPage({ copy: { signIn: "Log in" } });

    expect(screen.getByRole("heading", { name: "Log in" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Log in" })).toBeInTheDocument();
  });

  // An app that configures no providers: the page must be exactly the password
  // form it always was.
  it("offers no provider buttons when none are configured", () => {
    renderLoginPage();

    expect(screen.queryByRole("button", { name: /^Continue with/ })).not.toBeInTheDocument();
    expect(screen.queryByText("or")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  // Negative-run by dropping the providers.map in ProviderButtons, or the
  // providers config, to see the buttons' absence fail this.
  it("offers a button per configured provider that navigates to its sign-in", async () => {
    const user = userEvent.setup();
    const redirect = vi.fn();
    renderLoginPage({ ...withProviders, redirect });

    expect(screen.getByRole("button", { name: "Continue with Dex" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue with GitHub" }));

    expect(redirect).toHaveBeenCalledWith("/auth/login/github");
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  it("hides the password form when the app turns password sign-in off", () => {
    renderLoginPage({ ...withProviders, passwordLogin: false });

    expect(screen.getByRole("button", { name: "Continue with Dex" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Forgot your password?" })).not.toBeInTheDocument();
  });

  it("hides the forgot-password link when the app can't send a reset", () => {
    renderLoginPage({ passwordReset: false });

    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Forgot your password?" })).not.toBeInTheDocument();
  });

  it("explains the code a refused provider sign-in came back with", () => {
    renderLoginPage({ providers: [{ name: "dex", displayName: "Dex" }], providerSignInUrl: () => "/x" }, null, "no_account");

    expect(screen.getByRole("alert")).toHaveTextContent("There’s no account here for that Dex sign-in.");
  });

  // slop-haus answers a failed password sign-in with these codes
  // (rate_limited, post_required) among others -- this
  // package's own words for them should win over the app's describeError,
  // the same as every other call in these pages already does.
  it.each([
    ["rate_limited", "Too many attempts. Wait a little and try again."],
    ["post_required", "Start again from this site’s own page."],
  ])("shows this package's own message for a %s sign-in failure", async (code, expected) => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ signIn: vi.fn().mockRejectedValue(new FakeAuthError(code)) });
    renderLoginPage({ api, errorCode: fakeErrorCode, describeError: () => "should not be shown" });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
  });

  // A wrong password carries no code this package explains (each app words
  // that one itself, to avoid confirming an address exists) -- describeError
  // is still the fallback.
  it("falls back to describeError for a code this package doesn't explain", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({
      signIn: vi.fn().mockRejectedValue(new FakeAuthError("invalid_credentials")),
    });
    renderLoginPage({
      api,
      errorCode: fakeErrorCode,
      describeError: () => "invalid email or password",
    });

    await user.type(screen.getByLabelText("Email"), "person@example.com");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("invalid email or password");
  });

  it("offers what signInErrorExtra gives for the code of a failed sign-in", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ signIn: vi.fn().mockRejectedValue(new FakeAuthError("deletion_scheduled")) });
    renderLoginPage({
      api,
      errorCode: fakeErrorCode,
      signInErrorExtra: (code) => (code === "deletion_scheduled" ? <button>Cancel the deletion</button> : null),
    });

    expect(screen.queryByRole("button", { name: "Cancel the deletion" })).toBeNull();
    await user.type(screen.getByLabelText("Email"), "a@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("button", { name: "Cancel the deletion" })).toBeTruthy();
    expect(screen.getByText(/scheduled for deletion/)).toBeTruthy();
  });

  it("offers it for a refused provider sign-in too, by the callback's code", () => {
    renderLoginPage(
      { signInErrorExtra: (code) => (code === "deletion_scheduled" ? <button>Cancel the deletion</button> : null) },
      null,
      "deletion_scheduled",
    );
    expect(screen.getByRole("button", { name: "Cancel the deletion" })).toBeTruthy();
  });
});
