import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type BaseBranchResult, type Workspace, type WorkspaceAgent } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import { chooseFromDropdown } from "@/test/widgets";
import WorkspacePanel from "./workspace_panel";

// The GitHub App section is its own component with its own tests; here it
// only has to have something to load.
beforeEach(() => {
});

// The panel also hosts the fleet instructions section, which loads its own
// data; these tests are about the rest, so it has none to show.
beforeEach(() => {
  vi.spyOn(apiClient, "getFleetInstructions").mockResolvedValue({
    versions: [],
    default_id: null,
    can_edit: false,
    max_body_bytes: 20000,
  });
  // Likewise the variants section, which a non-owner sees as a refusal.
  vi.spyOn(apiClient, "listVariants").mockRejectedValue(new ApiError(403, "owners only"));
});

afterEach(() => vi.restoreAllMocks());

function agentFor(overrides: Partial<WorkspaceAgent>): WorkspaceAgent {
  return {
    agent_id: "a1",
    agent_name: "agent",
    status: "unknown",
    reported_at: null,
    layout: null,
    mismatches: [],
    ...overrides,
  };
}

const APP = { path: "app", remote: "github.com/acme/app" };
const MANIFEST = { repos: [APP], revision: 1, updated_at: "2026-10-04T12:00:00Z" };

function workspaceWith(overrides: Partial<Workspace>): Workspace {
  return { manifest: null, can_edit: true, proposal: null, agents: [], divergences: [], ...overrides };
}

function mockWorkspace(overrides: Partial<Workspace>) {
  return vi.spyOn(apiClient, "getWorkspace").mockResolvedValue(workspaceWith(overrides));
}

describe("WorkspacePanel agents", () => {
  it("shows what each agent reported, and unknown for one that has not", async () => {
    mockWorkspace({
      agents: [
        agentFor({
          agent_id: "a1",
          agent_name: "builder-1",
          status: "reported",
          reported_at: "2026-10-04T12:00:00Z",
          layout: {
            root: { path: ".", git: false, origin: "" },
            repos: [
              { path: "app", git: true, origin: "github.com/acme/app" },
              { path: "deploy", git: true, origin: "" },
            ],
          },
        }),
        agentFor({ agent_id: "a2", agent_name: "builder-2" }),
      ],
    });

    await renderSignedIn(<WorkspacePanel />);

    await screen.findByText("builder-1");
    expect(screen.queryByRole("heading", { name: "GitHub App" })).not.toBeInTheDocument();
    const rows = screen.getAllByRole("listitem").filter((li) => li.classList.contains("workspace-panel__row"));
    expect(within(rows[0]).getByText("reported")).toBeInTheDocument();
    expect(within(rows[0]).getByText(/No manifest to compare/)).toBeInTheDocument();
    expect(within(rows[0]).getByText("not a git repo")).toBeInTheDocument();
    expect(within(rows[0]).getByText("github.com/acme/app")).toBeInTheDocument();
    expect(within(rows[0]).getByText("git repo, no origin")).toBeInTheDocument();

    expect(within(rows[1]).getByText("unknown")).toBeInTheDocument();
    expect(within(rows[1]).getByText(/Restart the agent/)).toBeInTheDocument();
  });

  it("says what each kind of mismatch found, and that a matching agent matches", async () => {
    mockWorkspace({
      manifest: MANIFEST,
      agents: [
        agentFor({ agent_id: "a1", agent_name: "good", status: "matches" }),
        agentFor({
          agent_id: "a2",
          agent_name: "bad",
          status: "mismatch",
          mismatches: [
            { path: "app", problem: "no_repo", expected: "github.com/acme/app", found: "" },
            { path: "deploy", problem: "no_origin", expected: "github.com/acme/deploy", found: "" },
            { path: "tools", problem: "wrong_origin", expected: "github.com/acme/tools", found: "github.com/evil/tools" },
          ],
        }),
      ],
    });

    await renderSignedIn(<WorkspacePanel />);

    const bad = (await screen.findByText("bad")).closest("li")!;
    expect(bad).toHaveTextContent("no git repository found there (missing, or not a git repo)");
    expect(bad).toHaveTextContent("repository has no origin");
    expect(bad).toHaveTextContent("origin is github.com/evil/tools");
    expect(bad).toHaveTextContent("expected github.com/acme/tools");
    expect(screen.getByText("good").closest("li")).toHaveTextContent("Matches the manifest");
  });

  it("says so when there are no agents", async () => {
    mockWorkspace({});
    await renderSignedIn(<WorkspacePanel />);
    expect(await screen.findByText(/No enabled agents/)).toBeInTheDocument();
  });

  it("shows the server's error when the workspace fails to load", async () => {
    vi.spyOn(apiClient, "getWorkspace").mockRejectedValue(new ApiError(500, "database is down"));
    await renderSignedIn(<WorkspacePanel />);
    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
  });

  it("has a help section, closed until opened", async () => {
    mockWorkspace({});
    await renderSignedIn(<WorkspacePanel />);

    const help = (await screen.findByText("How workspaces work")).closest("details")!;
    expect(help.open).toBe(false);
    await userEvent.click(screen.getByText("How workspaces work"));
    expect(help.open).toBe(true);
  });
});

describe("WorkspacePanel manifest", () => {
  it("says nothing is enforced without a manifest, proposes one, and adopts it on click", async () => {
    const get = mockWorkspace({
      proposal: {
        repos: [APP],
        disagreements: [{ agent_id: "a2", agent_name: "stale-1", found: [] }],

      },
    });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...MANIFEST, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);

    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(section).toHaveTextContent("nothing about the workspace layout is enforced");
    expect(section).toHaveTextContent("github.com/acme/app");
    expect(section).toHaveTextContent("odd one out");
    expect(section).toHaveTextContent("stale-1 found no git repo with an origin");

    get.mockResolvedValue(workspaceWith({ manifest: MANIFEST }));
    await userEvent.click(within(section).getByRole("button", { name: "Adopt this manifest" }));

    expect(set).toHaveBeenCalledWith([APP]);
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Adopt this manifest" })).not.toBeInTheDocument();
  });

  it.each([
    ["no_reports", /No agent has reported/],
    ["no_repos", /no git repo with an origin, so there is nothing to propose/],
    ["no_consensus", /agents disagree/],
  ] as const)("explains an empty proposal: %s", async (reason, text) => {
    mockWorkspace({
      proposal: {
        repos: [],
        reason,

        disagreements: reason === "no_consensus" ? [{ agent_id: "a1", agent_name: "left", found: [APP] }] : [],
      },
    });
    await renderSignedIn(<WorkspacePanel />);

    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(section).toHaveTextContent(text);
    expect(within(section).queryByRole("button", { name: "Adopt this manifest" })).not.toBeInTheDocument();
    if (reason === "no_consensus") {
      expect(section).toHaveTextContent("These agents differ");
      expect(section).toHaveTextContent("left found app → github.com/acme/app");
    }
  });

  it("does not offer a non-owner adopt, edit or remove", async () => {
    mockWorkspace({ can_edit: false, proposal: { repos: [APP], disagreements: [] } });
    await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(section).toHaveTextContent("github.com/acme/app");
    expect(section).toHaveTextContent("Only the tenant’s owners can adopt");
    expect(within(section).queryByRole("button")).not.toBeInTheDocument();
  });

  it("says what is wrong when an agent's workspace root is a repo", async () => {
    mockWorkspace({
      manifest: MANIFEST,
      agents: [
        agentFor({
          agent_id: "a1",
          agent_name: "rooted",
          status: "mismatch",
          mismatches: [{ path: ".", problem: "root_is_repo", expected: "", found: "github.com/acme/app" }],
        }),
      ],
    });
    await renderSignedIn(<WorkspacePanel />);
    const row = (await screen.findByText("rooted")).closest("li")!;
    expect(row).toHaveTextContent("the workspace root must not be a repo");
    expect(row).not.toHaveTextContent("expected");
  });

  it("shows a manifest read-only to a non-owner", async () => {
    mockWorkspace({ can_edit: false, manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(section).toHaveTextContent("github.com/acme/app");
    expect(section).toHaveTextContent("Only the tenant’s owners can change the manifest");
    expect(within(section).queryByRole("button")).not.toBeInTheDocument();
  });

  it("lets an owner add a repo and save, sending every row", async () => {
    const get = mockWorkspace({ manifest: MANIFEST });
    const saved = { ...MANIFEST, repos: [APP, { path: "deploy", remote: "github.com/acme/deploy" }] };
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...saved, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Add repo" }));
    await userEvent.type(screen.getByLabelText("Path 2"), "deploy");
    await userEvent.type(screen.getByLabelText("Remote 2"), "github.com/acme/deploy");

    get.mockResolvedValue(workspaceWith({ manifest: saved }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([APP, { path: "deploy", remote: "github.com/acme/deploy" }]);
    expect(await screen.findByText("github.com/acme/deploy")).toBeInTheDocument();
    expect(screen.queryByLabelText("Path 1")).not.toBeInTheDocument();
  });

  // jsdom loads no scss, so what can be pinned is the markup that picks up
  // the shared look: @slop-factory/ui's Button, not a bare <button>.
  it("uses the shared Button for every action in the manifest section", async () => {
    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);

    const section = await screen.findByRole("region", { name: "Manifest" });
    await userEvent.click(within(section).getByRole("button", { name: "Edit" }));
    await userEvent.click(within(section).getByRole("button", { name: "Remove repo 1" }));
    await userEvent.click(within(section).getByRole("button", { name: "Add repo" }));
    const buttons = within(section).getAllByRole("button");
    expect(buttons.length).toBeGreaterThanOrEqual(3);
    for (const b of buttons) expect(b).toHaveClass("button");
  });

  it("removes a row from the form, and Cancel sends nothing", async () => {
    mockWorkspace({ manifest: MANIFEST });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...MANIFEST, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove repo 1" }));
    expect(screen.queryByLabelText("Path 1")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(set).not.toHaveBeenCalled();
    expect(screen.getByText("github.com/acme/app")).toBeInTheDocument();
  });

  it("shows the server's refusal and keeps the form", async () => {
    mockWorkspace({ manifest: MANIFEST });
    vi.spyOn(apiClient, "setWorkspaceManifest").mockRejectedValue(new ApiError(422, "path must be one directory name"));
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("path must be one directory name");
    expect(screen.getByLabelText("Path 1")).toHaveValue("app");
  });

  it("shows a 403 from adopting", async () => {
    mockWorkspace({ proposal: { repos: [APP], disagreements: [] } });
    vi.spyOn(apiClient, "setWorkspaceManifest").mockRejectedValue(new ApiError(403, "only an owner may change the manifest"));
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Adopt this manifest" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("only an owner may change the manifest");
  });

  it("asks before removing the manifest, and removes it once confirmed", async () => {
    const get = mockWorkspace({ manifest: MANIFEST });
    const clear = vi.spyOn(apiClient, "clearWorkspaceManifest").mockResolvedValue();
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Remove manifest" }));
    const confirm = screen.getByRole("group", { name: "Confirm removing the manifest" });
    expect(clear).not.toHaveBeenCalled();

    await userEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("group", { name: "Confirm removing the manifest" })).not.toBeInTheDocument();
    expect(clear).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Remove manifest" }));
    get.mockResolvedValue(workspaceWith({}));
    await userEvent.click(screen.getByRole("button", { name: "Remove it" }));

    expect(clear).toHaveBeenCalledOnce();
    expect(await screen.findByText(/No manifest is declared/)).toBeInTheDocument();
  });

  it("keeps the manifest and shows the refusal when removing it fails", async () => {
    mockWorkspace({ manifest: MANIFEST });
    vi.spyOn(apiClient, "clearWorkspaceManifest").mockRejectedValue(new ApiError(403, "only an owner may change the manifest"));
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Remove manifest" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove it" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("only an owner may change the manifest");
    expect(screen.queryByText(/No manifest is declared/)).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Confirm removing the manifest" })).toBeInTheDocument();
  });
});

describe("WorkspacePanel deploy-only files", () => {
  const TAGS = { path: "deploy/*/kustomization.yaml", lines: "\\s*newTag: \\S+" };
  const PINS = { path: "deploy/pins.yaml" };
  const WITH_RULES = { repos: [{ ...APP, deploy_only: [TAGS, PINS] }], revision: 1, updated_at: "2026-10-12T00:00:00Z" };

  async function editManifest(manifest: Workspace["manifest"]) {
    mockWorkspace({ manifest });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...manifest!, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    return set;
  }

  it("shows each repo's rules, with the lines a rule allows", async () => {
    mockWorkspace({ can_edit: false, manifest: WITH_RULES });
    await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });

    expect(within(section).getByRole("columnheader", { name: "Deploy-only files" })).toBeInTheDocument();
    expect(section).toHaveTextContent("deploy/*/kustomization.yaml only lines matching \\s*newTag: \\S+");
    expect(section).toHaveTextContent("deploy/pins.yaml");
  });

  it("shows no deploy-only column while no repo has rules", async () => {
    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(within(section).queryByRole("columnheader", { name: "Deploy-only files" })).not.toBeInTheDocument();
  });

  it("adds a rule to a repo and saves it with the repo", async () => {
    const set = await editManifest(MANIFEST);

    await userEvent.click(screen.getByRole("button", { name: "Add deploy-only file to repo 1" }));
    await userEvent.type(screen.getByLabelText("Deploy-only file 1 of repo 1"), TAGS.path);
    // userEvent.type reads { and [ as keys; paste takes the text as it is.
    await userEvent.click(screen.getByLabelText("Only lines matching, file 1 of repo 1"));
    await userEvent.paste(TAGS.lines);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, deploy_only: [TAGS] }]);
  });

  it("keeps a repo's rules when its other fields are edited", async () => {
    const set = await editManifest(WITH_RULES);

    await userEvent.clear(screen.getByLabelText("Remote 1"));
    await userEvent.type(screen.getByLabelText("Remote 1"), "github.com/acme/renamed");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ path: "app", remote: "github.com/acme/renamed", deploy_only: [TAGS, PINS] }]);
  });

  it("edits and removes rules, and sends none once the last is gone", async () => {
    const set = await editManifest(WITH_RULES);

    expect(screen.getByLabelText("Deploy-only file 1 of repo 1")).toHaveValue(TAGS.path);
    expect(screen.getByLabelText("Only lines matching, file 1 of repo 1")).toHaveValue(TAGS.lines);
    await userEvent.clear(screen.getByLabelText("Deploy-only file 2 of repo 1"));
    await userEvent.type(screen.getByLabelText("Deploy-only file 2 of repo 1"), "deploy/other.yaml");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenLastCalledWith([{ ...APP, deploy_only: [TAGS, { path: "deploy/other.yaml" }] }]);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove deploy-only file 2 of repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove deploy-only file 1 of repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenLastCalledWith([APP]);
  });

  it("drops a rule row that was added and left blank", async () => {
    const set = await editManifest(MANIFEST);

    await userEvent.click(screen.getByRole("button", { name: "Add deploy-only file to repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([APP]);
  });

  it("shows the server's refusal of a rule and keeps the form", async () => {
    mockWorkspace({ manifest: MANIFEST });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockRejectedValue(
      new ApiError(422, "manifest: invalid: repo 1 (app): deploy_only: rule 1 (x.yaml): lines is not a regular expression"),
    );
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Add deploy-only file to repo 1" }));
    await userEvent.type(screen.getByLabelText("Deploy-only file 1 of repo 1"), "x.yaml");
    await userEvent.click(screen.getByLabelText("Only lines matching, file 1 of repo 1"));
    await userEvent.paste("newTag: (");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, deploy_only: [{ path: "x.yaml", lines: "newTag: (" }] }]);
    expect(await screen.findByRole("alert")).toHaveTextContent("lines is not a regular expression");
    expect(screen.getByLabelText("Deploy-only file 1 of repo 1")).toHaveValue("x.yaml");
    expect(screen.getByLabelText("Only lines matching, file 1 of repo 1")).toHaveValue("newTag: (");
  });
});

describe("WorkspacePanel independent apps", () => {
  const WITH_APPS = {
    repos: [{ ...APP, independent_apps: ["slop-haus/"], batch_within_apps: true, skip_retest_across_apps: true }],
    revision: 1,
    updated_at: "2026-10-12T00:00:00Z",
  };

  async function editManifest(manifest: Workspace["manifest"]) {
    mockWorkspace({ manifest });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...manifest!, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    return set;
  }

  it("shows the apps and whether they are enforced, and no column without any", async () => {
    mockWorkspace({ can_edit: false, manifest: WITH_APPS });
    const { unmount } = await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(within(section).getByRole("columnheader", { name: "Apps" })).toBeInTheDocument();
    expect(section).toHaveTextContent("slop-haus/");
    expect(section).toHaveTextContent("Batch merges within an app: on");
    expect(section).toHaveTextContent("Skip re-testing when main moved only in other apps: on");
    unmount();

    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    const plain = await screen.findByRole("region", { name: "Manifest" });
    expect(within(plain).queryByRole("columnheader", { name: "Apps" })).not.toBeInTheDocument();
  });

  it("adds an app and turns enforcement on, and saves both with the repo", async () => {
    const set = await editManifest(MANIFEST);

    await userEvent.click(screen.getByRole("button", { name: "Add independent app to repo 1" }));
    await userEvent.type(screen.getByLabelText("Independent app 1 of repo 1"), "billing/");
    await userEvent.click(screen.getByRole("checkbox", { name: /Batch merges within an app, for repo 1/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Skip re-testing when main moved only in other apps, for repo 1/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, independent_apps: ["billing/"], batch_within_apps: true, skip_retest_across_apps: true }]);
  });

  it("keeps them when other fields are edited, and sends none once removed or left blank", async () => {
    const set = await editManifest(WITH_APPS);
    await userEvent.clear(screen.getByLabelText("Remote 1"));
    await userEvent.type(screen.getByLabelText("Remote 1"), "github.com/acme/renamed");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenCalledWith([
      { path: "app", remote: "github.com/acme/renamed", independent_apps: ["slop-haus/"], batch_within_apps: true, skip_retest_across_apps: true },
    ]);

    set.mockClear();
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove independent app 1 of repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Add independent app to repo 1" }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Batch merges within an app, for repo 1/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Skip re-testing when main moved only in other apps, for repo 1/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenCalledWith([APP]);
  });
});

describe("WorkspacePanel base branch", () => {
  const WITH_BASE = { repos: [{ ...APP, base_branch: "slop-haus-acme" }], revision: 1, updated_at: "2026-10-12T00:00:00Z" };

  async function editManifest(manifest: Workspace["manifest"], base_branches: BaseBranchResult[] = []) {
    mockWorkspace({ manifest });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...manifest!, base_branches });
    await renderSignedIn(<WorkspacePanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    return set;
  }

  it("shows the base branch, and no column while no repo has one", async () => {
    mockWorkspace({ can_edit: false, manifest: WITH_BASE });
    const { unmount } = await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(within(section).getByRole("columnheader", { name: "Base branch" })).toBeInTheDocument();
    expect(section).toHaveTextContent("slop-haus-acme");
    unmount();

    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    const plain = await screen.findByRole("region", { name: "Manifest" });
    expect(within(plain).queryByRole("columnheader", { name: "Base branch" })).not.toBeInTheDocument();
  });

  it("saves a base branch with the repo, and sends none once it is cleared", async () => {
    const set = await editManifest(MANIFEST);
    await userEvent.type(screen.getByLabelText("Base branch 1"), "slop-haus-acme");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenCalledWith([{ ...APP, base_branch: "slop-haus-acme" }]);

    set.mockClear();
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.clear(screen.getByLabelText("Base branch 1"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenCalledWith([APP]);
  });

  it("describes the base branch input with its hint, under a visible label", async () => {
    await editManifest(MANIFEST);
    const input = screen.getByLabelText("Base branch 1");
    expect(input).toHaveAccessibleDescription(/cut from and merged into/);
    expect(screen.getByText("Base branch", { selector: "label" })).toHaveAttribute("for", input.id);
  });

  it("reports a branch that could not be made, saying the setting is saved", async () => {
    const failed: BaseBranchResult = { path: "app", branch: "slop-haus-acme", status: "failed", message: "host down" };
    await editManifest(WITH_BASE, [failed]);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/host down.*saved/);
  });

  it("notes a base branch that is the default branch, without an alert", async () => {
    const note: BaseBranchResult = { path: "app", branch: "main", status: "default_branch", message: "main is the repo's default branch" };
    await editManifest(WITH_BASE, [note]);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("status")).toHaveTextContent("default branch");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("WorkspacePanel shared paths", () => {
  const MIGRATIONS = { path: "server/migrations/", resolve: "migration" as const };
  const WITH_SHARED = { repos: [{ ...APP, shared_paths: [MIGRATIONS] }], revision: 1, updated_at: "2026-10-12T00:00:00Z" };

  async function editManifest(manifest: Workspace["manifest"]) {
    mockWorkspace({ manifest });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...manifest!, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    return set;
  }

  it("shows each repo's shared paths with their resolve kind, and no column without any", async () => {
    mockWorkspace({ can_edit: false, manifest: WITH_SHARED });
    const { unmount } = await renderSignedIn(<WorkspacePanel />);
    const section = await screen.findByRole("region", { name: "Manifest" });
    expect(within(section).getByRole("columnheader", { name: "Shared paths" })).toBeInTheDocument();
    expect(section).toHaveTextContent("server/migrations/ migration");
    unmount();

    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    const plain = await screen.findByRole("region", { name: "Manifest" });
    expect(within(plain).queryByRole("columnheader", { name: "Shared paths" })).not.toBeInTheDocument();
  });

  it("offers the merge kind for a shared path", async () => {
    const set = await editManifest(MANIFEST);

    await userEvent.click(screen.getByRole("button", { name: "Add shared path to repo 1" }));
    await userEvent.type(screen.getByLabelText("Shared path 1 of repo 1"), "docs/");
    await chooseFromDropdown(userEvent.setup(), "How a clash is resolved, shared path 1 of repo 1", /^merge/);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, shared_paths: [{ path: "docs/", resolve: "merge" }] }]);
  });

  it("adds a shared path, picks its kind and saves it with the repo", async () => {
    const set = await editManifest(MANIFEST);

    await userEvent.click(screen.getByRole("button", { name: "Add shared path to repo 1" }));
    await userEvent.type(screen.getByLabelText("Shared path 1 of repo 1"), "web/gen/");
    await chooseFromDropdown(userEvent.setup(), "How a clash is resolved, shared path 1 of repo 1", /^regenerate/);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, shared_paths: [{ path: "web/gen/", resolve: "regenerate" }] }]);
  });

  it("warns, without refusing, when a shared path looks like a directory missing its slash", async () => {
    const set = await editManifest(MANIFEST);
    await userEvent.click(screen.getByRole("button", { name: "Add shared path to repo 1" }));
    const input = screen.getByLabelText("Shared path 1 of repo 1");

    await userEvent.type(input, "web/api_generated");
    expect(screen.getByRole("status")).toHaveTextContent(/trailing \//);

    for (const ok of ["/", "x/*.sql", "schema.d.ts"]) {
      await userEvent.clear(input);
      await userEvent.type(input, "web/gen" + ok);
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
    }

    await userEvent.clear(input);
    await userEvent.type(input, "web/api_generated");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(set).toHaveBeenCalled();
  });

  it("keeps a repo's shared paths when its other fields are edited", async () => {
    const set = await editManifest(WITH_SHARED);

    await userEvent.clear(screen.getByLabelText("Remote 1"));
    await userEvent.type(screen.getByLabelText("Remote 1"), "github.com/acme/renamed");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ path: "app", remote: "github.com/acme/renamed", shared_paths: [MIGRATIONS] }]);
  });

  it("sends none once the last is removed, or when an added row is left blank", async () => {
    const set = await editManifest(WITH_SHARED);

    await userEvent.click(screen.getByRole("button", { name: "Remove shared path 1 of repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Add shared path to repo 1" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([APP]);
  });
});

describe("WorkspacePanel move a repo", () => {
  const PLAN = {
    applied: false,
    from_path: "app",
    to_path: "mono",
    repos: [{ path: "mono", remote: "github.com/acme/app" }],
    tickets: [{ id: 7, title: "Fix it", status: "open", paths: [{ from: "app/x/", to: "mono/x/" }] }],
    intake_sources: [],
    warnings: [],
  };

  it("previews with a dry run and writes only when confirmed", async () => {
    mockWorkspace({ manifest: MANIFEST });
    const move = vi.spyOn(apiClient, "moveWorkspaceRepo").mockResolvedValue(PLAN);
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Move a repo" }));
    await userEvent.type(screen.getByLabelText("New path"), "mono");
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));

    const preview = await screen.findByRole("group", { name: "Move preview" });
    expect(preview).toHaveTextContent("#7 Fix it");
    expect(preview).toHaveTextContent("app/x/ → mono/x/");
    expect(move).toHaveBeenCalledTimes(1);
    expect(move).toHaveBeenLastCalledWith("app", "mono", true);

    await userEvent.click(within(preview).getByRole("button", { name: "Move it" }));
    expect(move).toHaveBeenLastCalledWith("app", "mono", false);
  });

  it("shows the tickets that block the move", async () => {
    mockWorkspace({ manifest: MANIFEST });
    vi.spyOn(apiClient, "moveWorkspaceRepo").mockRejectedValue(
      new ApiError(409, "stop the fleet first; blocked by #7 (live lease)"),
    );
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Move a repo" }));
    await userEvent.type(screen.getByLabelText("New path"), "mono");
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("#7 (live lease)");
    expect(screen.queryByRole("button", { name: "Move it" })).not.toBeInTheDocument();
  });

  it("is not offered to someone who cannot edit", async () => {
    mockWorkspace({ manifest: MANIFEST, can_edit: false });
    await renderSignedIn(<WorkspacePanel />);
    await screen.findByText("github.com/acme/app");
    expect(screen.queryByRole("button", { name: "Move a repo" })).not.toBeInTheDocument();
  });
});

describe("WorkspacePanel sync an integration branch", () => {
  const TEAM = { ...APP, base_branch: "team-a" };
  const DIVERGENCE = { path: "app", base_branch: "team-a", default_branch: "main", behind: 3, ahead: 1 };
  const SYNCED = { path: "app", base_branch: "team-a", default_branch: "main", outcome: "merged" as const, head: "abcdef0123456789" };

  it("shows how far behind and ahead the branch is, with the action for an owner", async () => {
    mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [DIVERGENCE] });
    await renderSignedIn(<WorkspacePanel />);

    const list = await screen.findByRole("list", { name: "Integration branches" });
    expect(list).toHaveTextContent("team-a is 3 commits behind main, 1 ahead");
    expect(within(list).getByRole("button", { name: "Sync from main" })).toBeEnabled();
  });

  it("offers nothing when no repo has an integration branch", async () => {
    mockWorkspace({ manifest: MANIFEST });
    await renderSignedIn(<WorkspacePanel />);
    await screen.findByText("github.com/acme/app");
    expect(screen.queryByRole("list", { name: "Integration branches" })).not.toBeInTheDocument();
  });

  it("shows the counts but not the action to someone who cannot edit", async () => {
    mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [DIVERGENCE], can_edit: false });
    await renderSignedIn(<WorkspacePanel />);
    await screen.findByRole("list", { name: "Integration branches" });
    expect(screen.queryByRole("button", { name: "Sync from main" })).not.toBeInTheDocument();
  });

  it("syncs and reports the new head", async () => {
    const get = mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [DIVERGENCE] });
    const sync = vi.spyOn(apiClient, "syncWorkspaceRepo").mockResolvedValue(SYNCED);
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Sync from main" }));

    expect(sync).toHaveBeenCalledWith("app");
    expect(await screen.findByRole("status")).toHaveTextContent("abcdef012345");
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("says a conflict changed nothing and names the ticket filed", async () => {
    mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [DIVERGENCE] });
    vi.spyOn(apiClient, "syncWorkspaceRepo").mockResolvedValue({ ...SYNCED, outcome: "conflict", head: undefined, ticket_id: 812 });
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Sync from main" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("nothing changed");
    expect(alert).toHaveTextContent("#812");
  });

  it("shows who holds the lock when the sync is refused", async () => {
    mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [DIVERGENCE] });
    vi.spyOn(apiClient, "syncWorkspaceRepo").mockRejectedValue(new ApiError(409, "held by worker-7 for #9"));
    await renderSignedIn(<WorkspacePanel />);

    await userEvent.click(await screen.findByRole("button", { name: "Sync from main" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("worker-7");
  });

  it("reports a branch that could not be compared, without an action", async () => {
    mockWorkspace({ manifest: { ...MANIFEST, repos: [TEAM] }, divergences: [{ ...DIVERGENCE, behind: 0, ahead: 0, error: "host down" }] });
    await renderSignedIn(<WorkspacePanel />);
    expect(await screen.findByText(/could not be compared \(host down\)/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sync from main" })).not.toBeInTheDocument();
  });
});

describe("WorkspacePanel protected paths", () => {
  async function editManifest(manifest: Workspace["manifest"]) {
    mockWorkspace({ manifest });
    const set = vi.spyOn(apiClient, "setWorkspaceManifest").mockResolvedValue({ ...manifest!, base_branches: [] });
    await renderSignedIn(<WorkspacePanel />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    return set;
  }

  it("explains the input next to the field, offers no defaults and prefills nothing", async () => {
    await editManifest(MANIFEST);

    const help = screen.getByTestId("protected-paths-help");
    expect(help).toHaveTextContent(/always the owner.s decision/);
    expect(help).toHaveTextContent(/does not affect workers, scopes or merging/);
    expect(help).toHaveTextContent(/trailing \/ covers everything under that directory/);
    expect(help).toHaveTextContent("infrastructure/");
    expect(help).toHaveTextContent("*.env");
    expect(help).toHaveTextContent(/relative to the repo.s root/);
    expect(help).toHaveTextContent(/agent that could edit this list would choose its own limits/);
    expect(help).toHaveTextContent("database migrations");
    expect(screen.queryByRole("button", { name: /default/i })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Protected path 1 of repo 1")).not.toBeInTheDocument();
  });

  it("warns while the list is empty and stops once a path is typed", async () => {
    await editManifest(MANIFEST);
    expect(screen.getByText(/nothing is protected/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Add protected path to repo 1" }));
    expect(screen.getByText(/nothing is protected/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Protected path 1 of repo 1"), "infrastructure/");

    expect(screen.queryByText(/nothing is protected/)).not.toBeInTheDocument();
  });

  it("previews which rule protects a typed path", async () => {
    await editManifest({
      repos: [{ ...APP, protected_paths: [{ path: "infrastructure/" }, { path: "*.env" }] }],
      revision: 1,
      updated_at: "2026-10-12T00:00:00Z",
    });
    const input = screen.getByLabelText("Preview a path of repo 1");

    await userEvent.type(input, "infrastructure/k8s/a.yaml");
    expect(screen.getByLabelText("Preview result of repo 1")).toHaveTextContent("Protected by infrastructure/.");

    await userEvent.clear(input);
    await userEvent.type(input, "sub/prod.env");
    expect(screen.getByLabelText("Preview result of repo 1")).toHaveTextContent("Not protected");

    await userEvent.clear(input);
    await userEvent.type(input, "prod.env");
    expect(screen.getByLabelText("Preview result of repo 1")).toHaveTextContent("Protected by *.env.");
  });

  it("saves protected paths with their reason, dropping blank rows", async () => {
    const set = await editManifest(MANIFEST);
    await userEvent.click(screen.getByRole("button", { name: "Add protected path to repo 1" }));
    await userEvent.type(screen.getByLabelText("Protected path 1 of repo 1"), "infrastructure/");
    await userEvent.type(screen.getByLabelText("Reason, protected path 1 of repo 1"), "prod config");
    await userEvent.click(screen.getByRole("button", { name: "Add protected path to repo 1" }));

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(set).toHaveBeenCalledWith([{ ...APP, protected_paths: [{ path: "infrastructure/", reason: "prod config" }] }]);
  });

  it("shows protected paths in the manifest table only once a repo has some", async () => {
    mockWorkspace({
      can_edit: false,
      manifest: { repos: [{ ...APP, protected_paths: [{ path: "infrastructure/", reason: "prod" }] }], revision: 1, updated_at: "2026-10-12T00:00:00Z" },
    });
    await renderSignedIn(<WorkspacePanel />);

    expect(await screen.findByRole("columnheader", { name: "Protected paths" })).toBeInTheDocument();
    expect(screen.getByText("infrastructure/")).toBeInTheDocument();
  });
});
