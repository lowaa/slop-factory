import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { ApiError, type SignInMethodsBody } from "@/api";
import { stubApi } from "@/test/story_harness";
import StepUpPrompt from "./step_up_prompt";

const meta = {
  title: "Forms/StepUpPrompt",
  component: StepUpPrompt,
  args: { next: "/settings", onConfirmed: fn(), onCancel: fn() },
  parameters: {
    harness: {
      auth: { providers: [{ name: "github", display_name: "GitHub" }] },
    },
  },
} satisfies Meta<typeof StepUpPrompt>;

export default meta;
type Story = StoryObj<typeof meta>;

function methods(overrides: Partial<SignInMethodsBody> = {}): SignInMethodsBody {
  return {
    identities: [{ provider: "github", login: "ada", subject: "1", usable: true }],
    password: { email: "ada@example.com", verified: true },
    password_login_enabled: true,
    reauth_required_after: "2026-10-07T00:00:00Z",
    ...overrides,
  };
}

const sentPasswords: string[] = [];

function stubPasswordReauth(fail?: ApiError) {
  sentPasswords.length = 0;
  stubApi({
    listSignInMethods: async () => methods(),
    reauthWithPassword: async (password: string) => {
      sentPasswords.push(password);
      if (fail) throw fail;
    },
  });
}

export const PasswordAndProvider: Story = {
  beforeEach: () => stubPasswordReauth(),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const confirm = await canvas.findByRole("button", { name: "Confirm" });
    await expect(confirm).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Continue with GitHub" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
    await expect(confirm).toHaveClass("button", "button--primary");
    await expect(canvas.getByRole("button", { name: "Cancel" })).toHaveClass("button", "button--ghost");

    await userEvent.type(canvas.getByLabelText("Current password"), "hunter2");
    await userEvent.click(confirm);

    await waitFor(() => expect(args.onConfirmed).toHaveBeenCalledTimes(1));
    await expect(sentPasswords).toEqual(["hunter2"]);
  },
};

export const WrongPassword: Story = {
  beforeEach: () => stubPasswordReauth(new ApiError(401, "incorrect password")),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Current password"), "nope");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("incorrect password");
    await expect(sentPasswords).toEqual(["nope"]);
    // The typed password stays for a retry and the button comes back.
    await expect(canvas.getByLabelText("Current password")).toHaveValue("nope");
    await expect(canvas.getByRole("button", { name: "Confirm" })).toBeEnabled();
    await expect(args.onConfirmed).not.toHaveBeenCalled();
  },
};

export const ProviderOnly: Story = {
  beforeEach: () => {
    stubApi({
      listSignInMethods: async () =>
        methods({
          password: undefined,
          identities: [
            { provider: "github", login: "ada", subject: "1", usable: true },
            { provider: "gitlab", login: "ada", subject: "2", usable: false },
          ],
        }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Continue with GitHub" })).toBeEnabled();
    await expect(canvas.queryByLabelText("Current password")).toBeNull();
    await expect(canvas.queryByRole("button", { name: "Confirm" })).toBeNull();
    // An identity of a provider this deployment no longer offers can't sign in.
    await expect(canvas.queryByRole("button", { name: /gitlab/i })).toBeNull();
  },
};

export const Confirming: Story = {
  beforeEach: () => {
    stubApi({
      listSignInMethods: async () => methods(),
      // Never settles: the story holds the in-flight state.
      reauthWithPassword: () => new Promise<void>(() => {}),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Current password"), "hunter2");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm" }));
    // A second submit while the first is out would double-send the password.
    await expect(await canvas.findByRole("button", { name: "Confirming…" })).toBeDisabled();
  },
};

export const ContinueWithProvider: Story = {
  beforeEach: () => {
    const sent: { provider: string; next: string }[] = [];
    const previous = window.location.hash;
    stubApi({
      listSignInMethods: async () => methods(),
      startReauth: async (provider: string, next: string) => {
        sent.push({ provider, next });
        // A hash-only URL: the prompt's real navigation stays in the story's frame.
        return "#provider-reauth";
      },
    });
    (globalThis as { __stepUpSent?: typeof sent }).__stepUpSent = sent;
    return () => {
      window.history.replaceState({}, "", window.location.pathname + window.location.search + previous);
      delete (globalThis as { __stepUpSent?: typeof sent }).__stepUpSent;
    };
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() => expect(window.location.hash).toBe("#provider-reauth"));
    const sent = (globalThis as { __stepUpSent?: { provider: string; next: string }[] }).__stepUpSent;
    await expect(sent).toEqual([{ provider: "github", next: "/settings" }]);
    await expect(args.onConfirmed).not.toHaveBeenCalled();
  },
};

export const ProviderError: Story = {
  beforeEach: () =>
    stubApi({
      listSignInMethods: async () => methods(),
      startReauth: async () => {
        throw new ApiError(502, "provider unavailable");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Continue with GitHub" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("provider unavailable");
    await expect(canvas.getByRole("button", { name: "Continue with GitHub" })).toBeEnabled();
  },
};

export const MethodsFailedToLoad: Story = {
  beforeEach: () =>
    stubApi({
      listSignInMethods: async () => {
        throw new ApiError(500, "Failed to load sign-in methods.");
      },
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Failed to load sign-in methods.");
    await expect(canvas.queryByLabelText("Current password")).toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(args.onCancel).toHaveBeenCalledTimes(1);
  },
};

export const StaysUsableWhenTheCallerKeepsItOpen: Story = {
  beforeEach: () => stubPasswordReauth(),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Current password"), "hunter2");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(args.onConfirmed).toHaveBeenCalledTimes(1));
    // A caller whose retry failed leaves the prompt up; it must not stay on "Confirming…".
    await expect(await canvas.findByRole("button", { name: "Confirm" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
  },
};

export const OnlyConfiguredUsableProvidersAndVerifiedPassword: Story = {
  beforeEach: () => {
    stubApi({
      listSignInMethods: async () =>
        methods({
          password: { email: "ada@example.com", verified: false },
          identities: [
            { provider: "github", login: "ada", subject: "1", usable: false },
            { provider: "gitlab", login: "ada", subject: "2", usable: true },
          ],
        }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Sign out and sign in again, then make the change.")).toBeVisible();
    // github is configured but unusable, gitlab usable but not configured, the password unverified.
    await expect(canvas.queryByRole("button", { name: /Continue with/ })).toBeNull();
    await expect(canvas.queryByLabelText("Current password")).toBeNull();
  },
};
