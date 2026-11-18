import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TestAuthPagesProvider, makeFakeAuthApi } from "../test/auth_pages_fixture";
import type { AuthPagesConfig } from "../config";
import { agreeExtras } from "../test/sign_up_extras_fixture";
import RegisterPage from "./register_page";

function renderRegisterPage(config: Partial<AuthPagesConfig> = {}) {
  render(
    <TestAuthPagesProvider config={config}>
      <RegisterPage />
    </TestAuthPagesProvider>,
  );
}

describe("RegisterPage", () => {
  it("submits email, password and every name field to config.api.register", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderRegisterPage({
      api,
      nameFields: [
        { name: "first_name", label: "First name", autoComplete: "given-name" },
        { name: "last_name", label: "Last name", autoComplete: "family-name" },
      ],
    });

    await user.type(screen.getByLabelText("First name"), "Ada");
    await user.type(screen.getByLabelText("Last name"), "Lovelace");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(api.register).toHaveBeenCalledWith({
      email: "ada@example.com",
      password: "hunter2pass",
      names: { first_name: "Ada", last_name: "Lovelace" },
    });
  });

  it("shows the check-your-email notice, echoing the address, after a successful submit", async () => {
    const user = userEvent.setup();
    renderRegisterPage({
      copy: { registerSent: (email) => `We sent ${email} a link.` },
    });

    await user.type(screen.getByLabelText("Display name"), "Ada");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByText("We sent ada@example.com a link.")).toBeInTheDocument();
  });

  it("shows the message from config.describeError on a failed submit", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi({ register: vi.fn().mockRejectedValue(new Error("boom")) });
    renderRegisterPage({ api, describeError: () => "email already registered" });

    await user.type(screen.getByLabelText("Display name"), "Ada");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("email already registered");
  });

  it("calls onError with the register action on a failed submit", async () => {
    const user = userEvent.setup();
    const err = new Error("boom");
    const api = makeFakeAuthApi({ register: vi.fn().mockRejectedValue(err) });
    const onError = vi.fn();
    renderRegisterPage({ api, onError });

    await user.type(screen.getByLabelText("Display name"), "Ada");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Create account" }));

    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith("register", err);
  });

  // Negative-run this one first (flip registrationOpen, or drop the
  // early-return in register_page.tsx) to confirm it actually swaps the
  // whole page rather than just hiding a link.
  it("shows a closed notice instead of a form when registration is closed", () => {
    renderRegisterPage({
      registrationOpen: false,
      copy: { registrationClosed: "This board doesn't take open sign-ups." },
    });

    expect(screen.getByRole("status")).toHaveTextContent("This board doesn't take open sign-ups.");
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });

  it("offers no provider sign-up either when registration is closed", () => {
    renderRegisterPage({
      registrationOpen: false,
      api: makeFakeAuthApi({ startProviderSignUp: vi.fn() }),
      signUpExtras: agreeExtras,
      providers: [{ name: "dex", displayName: "Dex" }],
    });

    expect(screen.queryByRole("button", { name: "Continue with Dex" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("I agree")).not.toBeInTheDocument();
  });

  it("navigates to login from the closed notice", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    renderRegisterPage({ registrationOpen: false, navigate });

    await user.click(screen.getByRole("link"));

    expect(navigate).toHaveBeenCalledWith("/login");
  });

  it("applies name field minLength/maxLength to the rendered input", () => {
    renderRegisterPage({
      nameFields: [{ name: "display_name", label: "Display name", autoComplete: "name", minLength: 1, maxLength: 100 }],
    });

    const input = screen.getByLabelText("Display name");
    expect(input).toHaveAttribute("minlength", "1");
    expect(input).toHaveAttribute("maxlength", "100");
  });

  it("applies passwordRules min/max length to the password input", () => {
    renderRegisterPage({ passwordRules: { minLength: 10, maxLength: 72 } });

    const password = screen.getByLabelText("Password");
    expect(password).toHaveAttribute("minlength", "10");
    expect(password).toHaveAttribute("maxlength", "72");
    // jsdom doesn't implement minlength/maxlength constraint validation
    // (validity.tooShort is always false there), so unlike a real browser
    // this suite can only pin the attribute a browser enforces from, not
    // the enforcement itself.
  });

  it("navigates to login when 'already have an account' link is clicked", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    renderRegisterPage({ navigate });

    await user.click(screen.getByRole("link", { name: "Sign in" }));

    expect(navigate).toHaveBeenCalledWith("/login");
  });

  describe("with sign-up extras", () => {
    async function fillIn(user: ReturnType<typeof userEvent.setup>) {
      await user.type(screen.getByLabelText("Display name"), "Ada");
      await user.type(screen.getByLabelText("Email"), "ada@example.com");
      await user.type(screen.getByLabelText("Password"), "hunter2pass");
    }

    it("holds submit back until the extras are complete, then sends their values", async () => {
      const user = userEvent.setup();
      const api = makeFakeAuthApi();
      renderRegisterPage({ api, signUpExtras: agreeExtras });
      await fillIn(user);

      expect(screen.getByRole("button", { name: "Create account" })).toBeDisabled();
      await user.click(screen.getByLabelText("I agree"));
      await user.click(screen.getByRole("button", { name: "Create account" }));

      expect(api.register).toHaveBeenCalledWith({
        email: "ada@example.com",
        password: "hunter2pass",
        names: { display_name: "Ada" },
        extras: { agreed: true },
      });
    });

    it("does not submit incomplete extras even when the form is submitted without the button", async () => {
      const user = userEvent.setup();
      const api = makeFakeAuthApi();
      renderRegisterPage({ api, signUpExtras: agreeExtras });
      await fillIn(user);

      fireEvent.submit(screen.getByLabelText("Email").closest("form")!);

      expect(api.register).not.toHaveBeenCalled();
    });

    it("offers provider sign-up only with a way to start it, disabled until complete, carrying the extras", async () => {
      const user = userEvent.setup();
      const startProviderSignUp = vi.fn().mockResolvedValue("https://idp.example/auth");
      const redirect = vi.fn();
      renderRegisterPage({
        api: makeFakeAuthApi({ startProviderSignUp }),
        signUpExtras: agreeExtras,
        providers: [{ name: "dex", displayName: "Dex" }],
        redirect,
      });

      const button = screen.getByRole("button", { name: "Continue with Dex" });
      expect(button).toBeDisabled();
      await user.click(screen.getByLabelText("I agree"));
      await user.click(button);

      expect(startProviderSignUp).toHaveBeenCalledWith("dex", { agreed: true });
      expect(redirect).toHaveBeenCalledWith("https://idp.example/auth");
    });

    it("explains a refused provider sign-up by its code", async () => {
      const user = userEvent.setup();
      renderRegisterPage({
        api: makeFakeAuthApi({ startProviderSignUp: vi.fn().mockRejectedValue(new Error("x")) }),
        signUpExtras: agreeExtras,
        providers: [{ name: "dex", displayName: "Dex" }],
        errorCode: () => "age_requirement",
      });
      await user.click(screen.getByLabelText("I agree"));
      await user.click(screen.getByRole("button", { name: "Continue with Dex" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("old enough");
    });
  });

  it("offers no provider buttons, and leaves the register call as it was, without extras", async () => {
    const user = userEvent.setup();
    const api = makeFakeAuthApi();
    renderRegisterPage({ api, providers: [{ name: "dex", displayName: "Dex" }] });
    expect(screen.queryByRole("button", { name: "Continue with Dex" })).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Display name"), "Ada");
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("Password"), "hunter2pass");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(vi.mocked(api.register).mock.calls[0][0]).not.toHaveProperty("extras");
  });
});
