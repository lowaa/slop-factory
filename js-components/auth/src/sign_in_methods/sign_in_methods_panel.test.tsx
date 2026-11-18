import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import SignInMethodsPanel, { type SignInMethodsPanelProps } from "./sign_in_methods_panel";
import { stashPendingStepUp } from "./pending_step_up";
import {
  dexOnly,
  fakeDescribeError,
  fakeErrorCode,
  makeFakeSignInMethodsApi,
  passwordAndDex,
  passwordOnly,
  providers,
  type FakeServerOptions,
} from "../test/fake_sign_in_methods_api";

function renderPanel(server: FakeServerOptions, props: Partial<SignInMethodsPanelProps> = {}) {
  const api = makeFakeSignInMethodsApi(server);
  const redirect = vi.fn();
  render(
    <SignInMethodsPanel
      api={api}
      providers={providers}
      returnPath="/account"
      errorCode={fakeErrorCode}
      describeError={fakeDescribeError}
      redirect={redirect}
      {...props}
    />,
  );
  return { api, redirect };
}

afterEach(() => {
  sessionStorage.clear();
});

describe("SignInMethodsPanel", () => {
  it("lists the password and each identity by its provider's name, and offers to link the others", async () => {
    renderPanel({ methods: passwordAndDex });

    const list = await screen.findByRole("list");
    const [password, dex] = within(list).getAllByRole("listitem");
    expect(password).toHaveTextContent("Password");
    expect(password).toHaveTextContent("ada@example.com");
    expect(dex).toHaveTextContent("Dex");
    expect(dex).toHaveTextContent("ada@example.com");
    expect(screen.getByRole("button", { name: "Link GitHub" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Link Dex" })).not.toBeInTheDocument();
  });

  it("offers to add a password when there is none", async () => {
    renderPanel({ methods: dexOnly });

    expect(await screen.findByText("Not set")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add a password" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove password" })).not.toBeInTheDocument();
  });

  it("links a provider by sending the browser where the server says, coming back to the page", async () => {
    const user = userEvent.setup();
    const { api, redirect } = renderPanel({ methods: passwordAndDex });

    await user.click(await screen.findByRole("button", { name: "Link GitHub" }));

    expect(api.startLink).toHaveBeenCalledWith("github", "/account");
    expect(redirect).toHaveBeenCalledWith("https://idp.example.test/github?next=/account");
  });

  // The load-bearing flow: a change refused for want of a recent sign-in
  // is retried by itself once the person confirms. Negative-run by
  // dropping the retry in the step-up prompt's onPassword.
  it("asks a stale session to confirm with its password, then retries the change it refused", async () => {
    const user = userEvent.setup();
    const { api, redirect } = renderPanel({ methods: passwordAndDex, stale: true, password: "the-password" });

    await user.click(await screen.findByRole("button", { name: "Link GitHub" }));

    const prompt = await screen.findByRole("region", { name: "Confirm it’s you" });
    expect(redirect).not.toHaveBeenCalled();
    await user.type(within(prompt).getByLabelText("Current password"), "the-password");
    await user.click(within(prompt).getByRole("button", { name: "Confirm" }));

    expect(api.reauthWithPassword).toHaveBeenCalledWith("the-password");
    expect(api.startLink).toHaveBeenCalledTimes(2);
    expect(redirect).toHaveBeenCalledWith("https://idp.example.test/github?next=/account");
  });

  it("keeps the prompt open on a wrong password", async () => {
    const user = userEvent.setup();
    const { api } = renderPanel({ methods: passwordAndDex, stale: true, password: "the-password" });

    await user.click(await screen.findByRole("button", { name: "Remove password" }));
    await user.click(screen.getByRole("button", { name: "Yes, remove" }));
    const prompt = await screen.findByRole("region", { name: "Confirm it’s you" });
    await user.type(within(prompt).getByLabelText("Current password"), "wrong");
    await user.click(within(prompt).getByRole("button", { name: "Confirm" }));

    expect(await within(prompt).findByRole("alert")).toHaveTextContent("That password isn’t right.");
    expect(api.removePassword).toHaveBeenCalledTimes(1);
  });

  it("offers only a linked provider to confirm with when there is no password, and remembers the change", async () => {
    const user = userEvent.setup();
    const { api, redirect } = renderPanel({ methods: dexOnly, stale: true });

    await user.click(await screen.findByRole("button", { name: "Add a password" }));
    await user.type(screen.getByLabelText("New password"), "a-new-password");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const prompt = await screen.findByRole("region", { name: "Confirm it’s you" });
    expect(within(prompt).queryByLabelText("Current password")).not.toBeInTheDocument();
    expect(within(prompt).queryByRole("button", { name: "Continue with GitHub" })).not.toBeInTheDocument();
    await user.click(within(prompt).getByRole("button", { name: "Continue with Dex" }));

    expect(api.startReauth).toHaveBeenCalledWith("dex", "/account");
    expect(redirect).toHaveBeenCalledWith("https://idp.example.test/dex?next=/account");
    const stashed = sessionStorage.getItem("slop-factory-auth:pending-step-up") ?? "";
    expect(stashed).toContain("add-password");
    expect(stashed).not.toContain("a-new-password");
  });

  it("remembers nothing when the step-up through a provider couldn't start", async () => {
    const user = userEvent.setup();
    const { api, redirect } = renderPanel({ methods: dexOnly, stale: true });
    api.startReauth.mockRejectedValueOnce(new Error("provider unavailable"));

    await user.click(await screen.findByRole("button", { name: "Link GitHub" }));
    const prompt = await screen.findByRole("region", { name: "Confirm it’s you" });
    await user.click(within(prompt).getByRole("button", { name: "Continue with Dex" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("provider unavailable");
    expect(redirect).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("slop-factory-auth:pending-step-up")).toBeNull();
  });

  it("back from a provider step-up, offers the change that needed it", async () => {
    const user = userEvent.setup();
    stashPendingStepUp({ kind: "link", provider: "github" });
    const { api, redirect } = renderPanel({ methods: passwordAndDex });

    await user.click(await screen.findByRole("button", { name: "Continue: Link GitHub" }));

    expect(api.startLink).toHaveBeenCalledWith("github", "/account");
    expect(redirect).toHaveBeenCalled();
  });

  // This exercises the resume end to end under StrictMode, which is what a
  // host app actually renders under in development. The regression itself
  // -- a combined read-and-clear losing the stash to one of the two
  // initializer calls -- is what pending_step_up.test.ts's "returns the
  // stashed change without removing it" negative-runs directly; this test's
  // own double-invoked initializer didn't reproduce it under this React
  // version's actual (first-call-wins) behavior, which is exactly why that
  // behavior isn't something to rely on here.
  it("still offers the resume under StrictMode's double-invoked initializer, and retries it", async () => {
    const user = userEvent.setup();
    stashPendingStepUp({ kind: "link", provider: "github" });
    const api = makeFakeSignInMethodsApi({ methods: passwordAndDex });
    const redirect = vi.fn();

    render(
      <StrictMode>
        <SignInMethodsPanel
          api={api}
          providers={providers}
          returnPath="/account"
          errorCode={fakeErrorCode}
          describeError={fakeDescribeError}
          redirect={redirect}
        />
      </StrictMode>,
    );

    await user.click(await screen.findByRole("button", { name: "Continue: Link GitHub" }));

    expect(api.startLink).toHaveBeenCalledWith("github", "/account");
    expect(redirect).toHaveBeenCalled();
  });

  it("back from a provider step-up for a password change, reopens the form rather than replaying a password", async () => {
    stashPendingStepUp({ kind: "change-password", password: "never-stored" });
    renderPanel({ methods: passwordAndDex });

    expect(await screen.findByLabelText("New password")).toHaveValue("");
    expect(screen.getByRole("status")).toHaveTextContent("Enter the password again");
  });

  it("shows a refused round trip's code, and drops the change it was for", async () => {
    stashPendingStepUp({ kind: "link", provider: "github" });
    renderPanel({ methods: passwordAndDex }, { callbackError: "identity_in_use" });

    expect(await screen.findByRole("alert")).toHaveTextContent("already linked to a different account");
    expect(screen.queryByRole("button", { name: /^Continue:/ })).not.toBeInTheDocument();
  });

  it("names the provider in a refused round trip when there is only one", async () => {
    renderPanel({ methods: passwordAndDex }, { callbackError: "identity_in_use", providers: [providers[0]] });

    expect(await screen.findByRole("alert")).toHaveTextContent("That Dex account is already linked");
  });

  it("refuses to remove the last way in, saying why", async () => {
    const user = userEvent.setup();
    renderPanel({ methods: passwordOnly });

    await user.click(await screen.findByRole("button", { name: "Remove password" }));
    await user.click(screen.getByRole("button", { name: "Yes, remove" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("That’s the only way you can sign in");
    expect(screen.getByText("ada@example.com")).toBeInTheDocument();
  });

  it("removes an identity once confirmed, and lists what is left", async () => {
    const user = userEvent.setup();
    const { api } = renderPanel({ methods: passwordAndDex });

    await user.click(await screen.findByRole("button", { name: "Remove Dex sign-in" }));
    expect(api.removeIdentity).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Yes, remove" }));

    expect(await screen.findByRole("status")).toHaveTextContent("Dex sign-in removed.");
    expect(api.removeIdentity).toHaveBeenCalledWith({ provider: "dex", subject: "sub-ada" });
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Link Dex" })).toBeInTheDocument();
  });

  it("changes the password", async () => {
    const user = userEvent.setup();
    const { api } = renderPanel({ methods: passwordAndDex });

    await user.click(await screen.findByRole("button", { name: "Change password" }));
    await user.type(screen.getByLabelText("New password"), "a-changed-password");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("status")).toHaveTextContent("Password changed.");
    expect(api.changePassword).toHaveBeenCalledWith("a-changed-password");
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
  });

  it("asks for the password's address where the app wants one, and says to confirm it", async () => {
    const user = userEvent.setup();
    const { api } = renderPanel(
      { methods: dexOnly, addedPasswordsNeedVerifying: true },
      { addPasswordAsksEmail: true },
    );

    await user.click(await screen.findByRole("button", { name: "Add a password" }));
    await user.type(screen.getByLabelText("Email"), "ada@example.com");
    await user.type(screen.getByLabelText("New password"), "a-new-password");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(api.addPassword).toHaveBeenCalledWith({ email: "ada@example.com", password: "a-new-password" });
    expect(await screen.findByRole("status")).toHaveTextContent("Check ada@example.com for a link to confirm it.");
    expect(screen.getByText(/waiting for you to confirm/)).toBeInTheDocument();
  });

  it("shows a failed load with a retry", async () => {
    const user = userEvent.setup();
    const api = makeFakeSignInMethodsApi({ methods: passwordAndDex });
    api.list.mockRejectedValueOnce(new Error("network down"));
    render(
      <SignInMethodsPanel
        api={api}
        providers={providers}
        returnPath="/account"
        errorCode={fakeErrorCode}
        describeError={fakeDescribeError}
      />,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("network down");
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list")).toBeInTheDocument();
  });
});
