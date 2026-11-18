import { DAY_MS } from "@/lib/format_time";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError, type Invite } from "@/api";
import { makeInvite, makeMember } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import MembersPanel from "./members_panel";


// The expiry label is computed from the clock, so fixed dates would drift
// into "expired".
function inDays(days: number): string {
  return new Date(Date.now() + days * DAY_MS).toISOString();
}

const members = [
  makeMember({ id: "m1", display_name: "Ada Example" }),
  makeMember({ id: "m2", display_name: "Bea Example" }),
];

function invitesList(): Invite[] {
  return [
    makeInvite({ id: "i1", email: "carol@example.com", expires_at: inDays(5) }),
    makeInvite({ id: "i2", email: "dave@example.com", expires_at: inDays(-1) }),
  ];
}

const meta = {
  title: "Settings/MembersPanel",
  component: MembersPanel,
} satisfies Meta<typeof MembersPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  beforeEach: () => stubApi({ listMembers: async () => [members[0]], listInvites: async () => [] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("No outstanding invites.");
    await expect(canvas.getByLabelText("Email")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Send invite" })).toBeEnabled();
    await expect(canvas.getByText("Ada Example")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Revoke" })).toBeNull();
  },
};

export const MembersAndInvites: Story = {
  beforeEach: () => stubApi({ listMembers: async () => members, listInvites: async () => invitesList() }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("carol@example.com");
    await expect(canvas.getByText("Ada Example")).toBeVisible();
    await expect(canvas.getByText("Bea Example")).toBeVisible();
    await expect(canvas.getByText("expires in 5d")).toBeVisible();
    await expect(canvas.getByText("expired")).toBeVisible();
    await expect(canvas.getAllByRole("button", { name: "Revoke" })).toHaveLength(2);
  },
};

export const SendInvite: Story = {
  beforeEach: () => {
    let sent: Invite[] = [];
    stubApi({
      listMembers: async () => members,
      listInvites: async () => sent,
      createInvite: fn(async (email: string) => {
        const invite = makeInvite({ id: "i-new", email, expires_at: inDays(7) });
        sent = [invite];
        return invite;
      }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("No outstanding invites.");
    await userEvent.type(canvas.getByLabelText("Email"), "erin@example.com");
    await userEvent.click(canvas.getByRole("button", { name: "Send invite" }));

    await canvas.findByText("expires in 7d");
    await expect(apiClient.createInvite).toHaveBeenCalledTimes(1);
    await expect(apiClient.createInvite).toHaveBeenCalledWith("erin@example.com");
    await expect(canvas.getByText("erin@example.com")).toBeVisible();
    await expect(canvas.getByLabelText("Email")).toHaveValue("");
  },
};

export const InviteError: Story = {
  beforeEach: () =>
    stubApi({
      listMembers: async () => members,
      listInvites: async () => [],
      createInvite: fn(async () => {
        throw new ApiError(409, "That person is already a member.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("No outstanding invites.");
    await userEvent.type(canvas.getByLabelText("Email"), "ada@example.com");
    await userEvent.click(canvas.getByRole("button", { name: "Send invite" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("That person is already a member.");
    await expect(apiClient.createInvite).toHaveBeenCalledWith("ada@example.com");
    // The address is kept so it can be corrected.
    await expect(canvas.getByLabelText("Email")).toHaveValue("ada@example.com");
    await expect(canvas.getByRole("button", { name: "Send invite" })).toBeEnabled();
  },
};

export const RevokeInvite: Story = {
  beforeEach: () => {
    let invites = invitesList();
    stubApi({
      listMembers: async () => members,
      listInvites: async () => invites,
      revokeInvite: fn(async (id: string) => {
        invites = invites.filter((invite) => invite.id !== id);
      }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("carol@example.com");
    const row = canvas.getByText("carol@example.com").closest("li") as HTMLElement;
    await userEvent.click(within(row).getByRole("button", { name: "Revoke" }));

    await expect(apiClient.revokeInvite).toHaveBeenCalledTimes(1);
    await expect(apiClient.revokeInvite).toHaveBeenCalledWith("i1");
    await expect(canvas.queryByText("carol@example.com")).toBeNull();
    await expect(canvas.getByText("dave@example.com")).toBeVisible();
  },
};

export const RevokeError: Story = {
  beforeEach: () =>
    stubApi({
      listMembers: async () => members,
      listInvites: async () => invitesList(),
      revokeInvite: fn(async () => {
        throw new ApiError(404, "That invite no longer exists.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("carol@example.com");
    await userEvent.click(canvas.getAllByRole("button", { name: "Revoke" })[0]);

    await expect(await canvas.findByRole("alert")).toHaveTextContent("That invite no longer exists.");
    await expect(canvas.getByText("carol@example.com")).toBeVisible();
    await expect(canvas.getAllByRole("button", { name: "Revoke" })[0]).toBeEnabled();
  },
};

export const LoadErrors: Story = {
  beforeEach: () =>
    stubApi({
      listMembers: async () => {
        throw new ApiError(500, "Members are unavailable.");
      },
      listInvites: async () => {
        throw new ApiError(500, "Invites are unavailable.");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("Members are unavailable.");
    await expect(canvas.getByText("Invites are unavailable.")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Send invite" })).toBeEnabled();
  },
};
