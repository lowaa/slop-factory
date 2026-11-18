import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { ApiError, type Invite } from "@/api";
import { makeAuthConfig, makeInvite, makeProfile } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AdminApp from "./admin_app";

// admin_app.scss is imported by admin_app.tsx itself, and the admin entry
// (main.tsx) loads the same index.scss the preview does, so the controls
// are styled here exactly as in the admin bundle.
const meta = {
  title: "Admin/AdminApp",
  component: AdminApp,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AdminApp>;

export default meta;
type Story = StoryObj<typeof meta>;

// AuthProvider fetches the auth config alongside the profile, so every
// story needs it stubbed.
const stubAuthConfig = () => stubApi({ getAuthConfig: async () => makeAuthConfig() });

const signedInAsAdmin = () => {
  stubAuthConfig();
  stubApi({ me: async () => makeProfile({ is_admin: true, display_name: "Ada Admin" }) });
};

export const Empty: Story = {
  beforeEach: () => {
    signedInAsAdmin();
    stubApi({ listAdminInvites: async () => [] });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Sign out" })).toBeVisible();
    await expect(await canvas.findByText("No outstanding invites.")).toBeVisible();
    await expect(canvas.getByLabelText("Email")).toHaveValue("");
    await expect(canvas.getByRole("button", { name: "Send invite" })).toBeEnabled();
  },
};

export const WithInvites: Story = {
  beforeEach: (ctx) => {
    signedInAsAdmin();
    // The list the server would return, minus what was revoked, so the
    // story shows the refresh after a revoke rather than only the call.
    let invites = [
      makeInvite({ id: "i1", email: "first@example.com", kind: "new_tenant" }),
      makeInvite({ id: "i2", email: "second@example.com", kind: "new_tenant" }),
    ];
    ctx.revokeAdminInvite = fn(async (id: string) => {
      invites = invites.filter((invite) => invite.id !== id);
    });
    stubApi({
      listAdminInvites: async () => invites,
      revokeAdminInvite: ctx.revokeAdminInvite,
    });
  },
  play: async ({ canvasElement, revokeAdminInvite }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("first@example.com")).toBeVisible();
    await expect(canvas.getAllByRole("button", { name: "Revoke" })).toHaveLength(2);
    await userEvent.click(canvas.getAllByRole("button", { name: "Revoke" })[1]);
    await expect(revokeAdminInvite).toHaveBeenCalledWith("i2");
    await waitFor(() => expect(canvas.queryByText("second@example.com")).not.toBeInTheDocument());
    await expect(canvas.getByText("first@example.com")).toBeVisible();
  },
};

export const SendInvite: Story = {
  beforeEach: (ctx) => {
    signedInAsAdmin();
    let invites: Invite[] = [];
    ctx.createAdminInvite = fn(async (email: string) => {
      const invite = makeInvite({ email, kind: "new_tenant" });
      invites = [invite];
      return invite;
    });
    stubApi({ listAdminInvites: async () => invites, createAdminInvite: ctx.createAdminInvite });
  },
  play: async ({ canvasElement, createAdminInvite }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Email"), "new@example.com");
    await userEvent.click(canvas.getByRole("button", { name: "Send invite" }));
    await expect(createAdminInvite).toHaveBeenCalledWith("new@example.com");
    await expect(canvas.getByLabelText("Email")).toHaveValue("");
    await expect(await canvas.findByText("new@example.com")).toBeVisible();
  },
};

export const InviteFailed: Story = {
  beforeEach: () => {
    signedInAsAdmin();
    stubApi({
      listAdminInvites: async () => [],
      createAdminInvite: async () => {
        throw new ApiError(409, "an invite for that email is already outstanding");
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Email"), "dup@example.com");
    await userEvent.click(canvas.getByRole("button", { name: "Send invite" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(/already outstanding/);
  },
};

export const SignedOut: Story = {
  beforeEach: () => {
    stubAuthConfig();
    stubApi({
      me: async () => {
        throw new ApiError(401, "unauthorized");
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  },
};
