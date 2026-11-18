import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import { AuthContext } from "@/context/auth";
import { makeConversationDetail } from "@/test/fixtures";
import { signedInAuth } from "@/test/render_signed_in";
import RepoState from "./repo_state";
import "./conversation_view.scss";

const repo = {
  branch: "ticket-9-x", head: "1234567890abcdef", dirty: true, dirty_count: 2, upstream: "origin/ticket-9-x",
  ahead: 1, behind: 3, commits: [], pushes: [], changed_paths: ["a/b.go", "c.md"], changed_total: 2,
};

const meta = {
  title: "RemoteControl/RepoState",
  component: RepoState,
  decorators: [
    (Story) => (
      <TestRouterProvider>
        <AuthContext.Provider value={signedInAuth()}>
          <Story />
        </AuthContext.Provider>
      </TestRouterProvider>
    ),
  ],
} satisfies Meta<typeof RepoState>;

export default meta;
type Story = StoryObj<typeof meta>;

export const WorkingCopy: Story = {
  args: { detail: makeConversationDetail({ repo }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText(/Working copy at/)).toBeVisible();
  },
};

export const WithScopeOverlap: Story = {
  args: {
    detail: makeConversationDetail({
      repo,
      scope_overlaps: [{ ticket_id: 42, title: "Held work", status: "in_progress", path: "a/b.go", held: "a/" }],
    }),
  },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("alert")).toHaveTextContent("Held work");
  },
};
