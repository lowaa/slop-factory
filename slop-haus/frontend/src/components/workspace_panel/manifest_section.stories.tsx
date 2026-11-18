import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError, type ManifestRepo, type Workspace } from "@/api";
import { stubApi } from "@/test/story_harness";
import ManifestSection from "./manifest_section";

const REPOS: ManifestRepo[] = [
  { path: "platform", remote: "git.example.test/acme/platform" },
  { path: "tools", remote: "git.example.test/acme/tools" },
];

function makeWorkspace(overrides: Partial<Workspace> = {}): Workspace {
  return {
    agents: [],
    can_edit: true,
    manifest: null,
    proposal: null,
    divergences: [],
    ...overrides,
  };
}

const withManifest = makeWorkspace({
  manifest: { repos: REPOS, revision: 1, updated_at: "2026-10-05T12:00:00Z" },
});

const meta = {
  title: "Workspace/ManifestSection",
  component: ManifestSection,
  args: { workspace: makeWorkspace(), onChanged: fn() },
} satisfies Meta<typeof ManifestSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoManifestOwner: Story = {
  args: {
    workspace: makeWorkspace({ proposal: { repos: REPOS, disagreements: [] } }),
  },
  beforeEach: () =>
    stubApi({
      setWorkspaceManifest: fn(async () => ({
        repos: REPOS,
        revision: 1, updated_at: "2026-10-05T12:00:00Z", base_branches: [],
      })),
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText(/No manifest is declared/)).toBeVisible();
    await userEvent.click(
      canvas.getByRole("button", { name: "Adopt this manifest" }),
    );
    await expect(apiClient.setWorkspaceManifest).toHaveBeenCalledWith(REPOS);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const NoManifestNoConsensus: Story = {
  args: {
    workspace: makeWorkspace({
      can_edit: false,
      proposal: {
        repos: [],
        reason: "no_consensus",
        disagreements: [
          {
            agent_id: "a1",
            agent_name: "fake-agent-one",
            found: [{ path: ".", remote: "git.example.test/acme/other" }],
          },
        ],
      },
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      canvas.getByText("The agents disagree, so no side is picked."),
    ).toBeVisible();
    await expect(canvas.getByText("fake-agent-one")).toBeVisible();
    await expect(canvas.queryByRole("button")).toBeNull();
    await expect(
      canvas.getByText(/Only the tenant.s owners can adopt/),
    ).toBeVisible();
  },
};

export const ManifestListed: Story = {
  args: { workspace: withManifest },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const rows = canvas.getAllByRole("row").slice(1);
    await expect(rows).toHaveLength(2);
    await expect(rows[0]).toHaveTextContent(
      "platformgit.example.test/acme/platform",
    );
    await expect(rows[1]).toHaveTextContent("toolsgit.example.test/acme/tools");
    await expect(canvas.getByRole("button", { name: "Edit" })).toBeEnabled();
    await expect(
      canvas.getByRole("button", { name: "Remove manifest" }),
    ).toBeEnabled();
  },
};

export const ManifestListedReadOnly: Story = {
  args: { workspace: makeWorkspace({ ...withManifest, can_edit: false }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByRole("button")).toBeNull();
    await expect(
      canvas.getByText(/Only the tenant.s owners can change the manifest/),
    ).toBeVisible();
  },
};

export const ConfirmRemove: Story = {
  args: { workspace: withManifest },
  beforeEach: () =>
    stubApi({ clearWorkspaceManifest: fn(async () => undefined as never) }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Remove manifest" }),
    );
    await expect(apiClient.clearWorkspaceManifest).not.toHaveBeenCalled();
    await userEvent.click(canvas.getByRole("button", { name: "Remove it" }));
    await expect(apiClient.clearWorkspaceManifest).toHaveBeenCalledTimes(1);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const EditSaveError: Story = {
  args: { workspace: withManifest },
  beforeEach: () =>
    stubApi({
      setWorkspaceManifest: fn(async () => {
        throw new ApiError(
          422,
          "Remote git.example.test/acme/bad is not a known repo host.",
        );
      }),
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Edit" }));
    const remote = canvas.getByLabelText("Remote 2");
    await userEvent.clear(remote);
    await userEvent.type(remote, "git.example.test/acme/bad");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "is not a known repo host",
    );
    await expect(apiClient.setWorkspaceManifest).toHaveBeenCalledWith([
      REPOS[0],
      { path: "tools", remote: "git.example.test/acme/bad" },
    ]);
    // The draft is kept for another attempt, and nothing was reported saved.
    await expect(canvas.getByLabelText("Remote 2")).toHaveValue(
      "git.example.test/acme/bad",
    );
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
    await expect(args.onChanged).not.toHaveBeenCalled();
  },
};

export const EditSaves: Story = {
  args: { workspace: withManifest },
  beforeEach: () =>
    stubApi({
      setWorkspaceManifest: fn(async (repos: ManifestRepo[]) => ({
        repos,
        revision: 1, updated_at: "2026-10-06T12:00:00Z", base_branches: [],
      })),
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Edit" }));
    await expect(canvas.getByLabelText("Base branch 1")).toHaveAccessibleDescription(/cut from and merged into/);
    await userEvent.click(
      canvas.getByRole("button", { name: "Remove repo 2" }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Add repo" }));
    await userEvent.type(canvas.getByLabelText("Path 2"), "docs");
    await userEvent.type(
      canvas.getByLabelText("Remote 2"),
      "git.example.test/acme/docs",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(apiClient.setWorkspaceManifest).toHaveBeenCalledWith([
      REPOS[0],
      { path: "docs", remote: "git.example.test/acme/docs" },
    ]);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
    await expect(canvas.queryByRole("alert")).toBeNull();
    await expect(canvas.queryByRole("button", { name: "Save" })).toBeNull();
    await expect(canvas.getByRole("button", { name: "Edit" })).toBeVisible();
  },
};

export const NoManifestNoReports: Story = {
  args: {
    workspace: makeWorkspace({
      proposal: { repos: [], reason: "no_reports", disagreements: [] },
    }),
  },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByText(
        /No agent has reported its workspace yet/,
      ),
    ).toBeVisible();
  },
};

export const NoManifestNoRepos: Story = {
  args: {
    workspace: makeWorkspace({
      proposal: { repos: [], reason: "no_repos", disagreements: [] },
    }),
  },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByText(
        /no git repo with an origin, so there is nothing to propose/,
      ),
    ).toBeVisible();
  },
};

const TAG_RULE = { path: "deploy/*/kustomization.yaml", lines: "\\s*newTag: \\S+" };

const withDeployOnly = makeWorkspace({
  manifest: {
    repos: [{ ...REPOS[0], deploy_only: [TAG_RULE, { path: "deploy/pins.yaml" }] }, REPOS[1]],
    revision: 1, updated_at: "2026-10-12T00:00:00Z",
  },
});

export const DeployOnlyListed: Story = {
  args: { workspace: withDeployOnly },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("columnheader", { name: "Deploy-only files" })).toBeVisible();
    // Each repo's rules sit in its own row: platform's two, the tools repo none.
    const [, root, tools] = canvas.getAllByRole("row");
    const rootCells = within(root).getAllByRole("cell");
    await expect(rootCells[0]).toHaveTextContent("platform");
    await expect(rootCells[2]).toHaveTextContent("deploy/*/kustomization.yaml only lines matching \\s*newTag: \\S+");
    await expect(rootCells[2]).toHaveTextContent("deploy/pins.yaml");
    const toolsCells = within(tools).getAllByRole("cell");
    await expect(toolsCells[0]).toHaveTextContent("tools");
    await expect(toolsCells[2]).toHaveTextContent(/^$/);
  },
};

export const DeployOnlyEditSaves: Story = {
  args: { workspace: withDeployOnly },
  beforeEach: () =>
    stubApi({
      setWorkspaceManifest: fn(async (repos: ManifestRepo[]) => ({ repos, revision: 1, updated_at: "2026-10-12T00:00:00Z", base_branches: [] })),
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Edit" }));
    await expect(canvas.getByLabelText("Deploy-only file 1 of repo 1")).toHaveValue(TAG_RULE.path);
    await userEvent.click(canvas.getByRole("button", { name: "Remove deploy-only file 2 of repo 1" }));
    await userEvent.click(canvas.getByRole("button", { name: "Add deploy-only file to repo 2" }));
    await userEvent.type(canvas.getByLabelText("Deploy-only file 1 of repo 2"), "release/version.txt");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(apiClient.setWorkspaceManifest).toHaveBeenCalledWith([
      { ...REPOS[0], deploy_only: [TAG_RULE] },
      { ...REPOS[1], deploy_only: [{ path: "release/version.txt" }] },
    ]);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const SharedPathWarnsOnMissingSlash: Story = {
  args: { workspace: withManifest },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Edit" }));
    await userEvent.click(canvas.getByRole("button", { name: "Add shared path to repo 1" }));
    const input = canvas.getByLabelText("Shared path 1 of repo 1");
    await userEvent.type(input, "db/migrations");
    await expect(canvas.getByRole("status")).toBeVisible();
    await userEvent.type(input, "/");
    await expect(canvas.queryByRole("status")).toBeNull();
  },
};
