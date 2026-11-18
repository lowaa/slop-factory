import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError, type ManifestRepo } from "@/api";
import { stubApi } from "@/test/story_harness";
import MoveRepo from "./move_repo";

const REPOS: ManifestRepo[] = [{ path: "old", remote: "git.example.test/acme/platform" }];

const PLAN = {
  applied: false,
  from_path: "old",
  to_path: "platform",
  repos: [{ path: "platform", remote: "git.example.test/acme/platform" }],
  tickets: [{ id: 7, title: "Fix it", status: "open", paths: [{ from: "app/x/", to: "platform/app/x/" }] }],
  intake_sources: [],
  warnings: [],
};

const meta = {
  title: "Workspace/MoveRepo",
  component: MoveRepo,
  args: { repos: REPOS, onMoved: fn() },
} satisfies Meta<typeof MoveRepo>;

export default meta;
type Story = StoryObj<typeof meta>;

export const PreviewThenMove: Story = {
  beforeEach: () => stubApi({ moveWorkspaceRepo: fn(async () => PLAN) }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Move a repo" }));
    await userEvent.type(canvas.getByLabelText("New path"), "platform");
    await userEvent.click(canvas.getByRole("button", { name: "Preview" }));
    await expect(await canvas.findByText(/#7 Fix it/)).toBeVisible();
    await expect(apiClient.moveWorkspaceRepo).toHaveBeenLastCalledWith("old", "platform", true);
    await userEvent.click(canvas.getByRole("button", { name: "Move it" }));
    await expect(apiClient.moveWorkspaceRepo).toHaveBeenLastCalledWith("old", "platform", false);
    await expect(args.onMoved).toHaveBeenCalledTimes(1);
  },
};

export const Blocked: Story = {
  beforeEach: () =>
    stubApi({
      moveWorkspaceRepo: fn(async () => {
        throw new ApiError(409, "stop the fleet first; blocked by #7 (live lease)");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Move a repo" }));
    await userEvent.type(canvas.getByLabelText("New path"), "platform");
    await userEvent.click(canvas.getByRole("button", { name: "Preview" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("#7 (live lease)");
  },
};
