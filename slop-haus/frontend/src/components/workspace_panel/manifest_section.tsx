import { useState } from "react";
import { Link } from "@slop-factory/router";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type BaseBranchResult, type ManifestRepo, type Workspace } from "@/api";
import MoveRepo from "./move_repo";
import ProtectedPathsEditor from "./protected_paths_editor";
import SyncRepos from "./sync_repos";
import "./manifest_section.scss";
import { errorMessage } from "@/lib/use_async_action";

type Proposal = NonNullable<Workspace["proposal"]>;
type DeployOnlyRule = NonNullable<ManifestRepo["deploy_only"]>[number];
type SharedPathRule = NonNullable<ManifestRepo["shared_paths"]>[number];
type ResolveKind = SharedPathRule["resolve"];

const RESOLVE_OPTIONS: { value: ResolveKind; label: string }[] = [
  { value: "migration", label: "migration (renumber, update LATEST)" },
  { value: "regenerate", label: "regenerate (re-run the generator)" },
  { value: "merge", label: "merge (merge the base branch, resolve by hand)" },
];

const NO_PROPOSAL_REASON: Record<NonNullable<Proposal["reason"]>, string> = {
  no_reports: "No agent has reported its workspace yet. Restart each agent in its workspace.",
  no_repos: "The reports found no git repo with an origin, so there is nothing to propose.",
  no_consensus: "The agents disagree, so no side is picked.",
};

function describeRepos(repos: ManifestRepo[]): string {
  return repos.length === 0 ? "no git repo with an origin" : repos.map((r) => `${r.path} → ${r.remote}`).join(", ");
}

function RepoTable({ repos }: { repos: ManifestRepo[] }) {
  // The column only appears once a repo has rules: most workspaces have
  // none, and an empty column would read as something to fill in.
  const showDeployOnly = repos.some((r) => (r.deploy_only ?? []).length > 0);
  const showShared = repos.some((r) => (r.shared_paths ?? []).length > 0);
  const showProtected = repos.some((r) => (r.protected_paths ?? []).length > 0);
  const showApps = repos.some((r) => (r.independent_apps ?? []).length > 0 || r.batch_within_apps || r.skip_retest_across_apps);
  const showBase = repos.some((r) => r.base_branch);
  return (
    <table className="manifest-section__table">
      <thead>
        <tr>
          <th>Path</th>
          <th>Remote</th>
          {showBase && <th>Base branch</th>}
          {showDeployOnly && <th>Deploy-only files</th>}
          {showShared && <th>Shared paths</th>}
          {showProtected && <th>Protected paths</th>}
          {showApps && <th>Apps</th>}
        </tr>
      </thead>
      <tbody>
        {repos.map((repo) => (
          <tr key={repo.path}>
            <td>
              <code>{repo.path}</code>
            </td>
            <td>
              <code>{repo.remote}</code>
            </td>
            {showBase && (
              <td>{repo.base_branch ? <code>{repo.base_branch}</code> : <span className="manifest-section__hint">default branch</span>}</td>
            )}
            {showDeployOnly && (
              <td>
                <ul className="manifest-section__rules">
                  {(repo.deploy_only ?? []).map((rule, k) => (
                    <li key={k}>
                      <code>{rule.path}</code>
                      {rule.lines && (
                        <>
                          {" "}
                          <span className="manifest-section__hint">only lines matching</span> <code>{rule.lines}</code>
                        </>
                      )}
                    </li>
                  ))}
                </ul>
              </td>
            )}
            {showShared && (
              <td>
                <ul className="manifest-section__rules">
                  {(repo.shared_paths ?? []).map((rule, k) => (
                    <li key={k}>
                      <code>{rule.path}</code> <span className="manifest-section__hint">{rule.resolve}</span>
                    </li>
                  ))}
                </ul>
              </td>
            )}
            {showProtected && (
              <td>
                <ul className="manifest-section__rules">
                  {(repo.protected_paths ?? []).map((rule, k) => (
                    <li key={k}>
                      <code>{rule.path}</code>
                      {rule.reason && <span className="manifest-section__hint"> {rule.reason}</span>}
                    </li>
                  ))}
                </ul>
              </td>
            )}
            {showApps && (
              <td>
                <ul className="manifest-section__rules">
                  {(repo.independent_apps ?? []).map((app) => (
                    <li key={app}>
                      <code>{app}</code>
                    </li>
                  ))}
                </ul>
                <span className="manifest-section__hint">
                  Batch merges within an app: {repo.batch_within_apps ? "on" : "off"}. Skip re-testing when main moved only in other apps:{" "}
                  {repo.skip_retest_across_apps ? "on" : "off"}.
                </span>
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ProposalView({ proposal, canEdit, busy, onAdopt }: {
  proposal: Proposal;
  canEdit: boolean;
  busy: boolean;
  onAdopt: () => void;
}) {
  const hasProposal = proposal.repos.length > 0;
  return (
    <div className="manifest-section__proposal">
      {hasProposal ? (
        <>
          <p>Proposed from what the agents report:</p>
          <RepoTable repos={proposal.repos} />
          {canEdit && (
            <div className="manifest-section__actions">
              <Button variant="primary" onClick={onAdopt} disabled={busy}>
                Adopt this manifest
              </Button>
            </div>
          )}
        </>
      ) : (
        <p>{proposal.reason ? NO_PROPOSAL_REASON[proposal.reason] : "There is nothing to propose."}</p>
      )}
      {proposal.disagreements.length > 0 && (
        <div className="manifest-section__disagreements">
          <p>{hasProposal ? "These agents are the odd one out, fix them first:" : "These agents differ:"}</p>
          <ul>
            {proposal.disagreements.map((d) => (
              <li key={d.agent_id}>
                <strong>{d.agent_name}</strong> found {describeRepos(d.found)}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

// forSave drops rule rows left blank and an empty line pattern, so an
// "Add" clicked and not used saves nothing; anything half-filled goes to the
// server, which says what is wrong with it.
function forSave(repo: ManifestRepo): ManifestRepo {
  const rules = (repo.deploy_only ?? [])
    .filter((r) => r.path.trim() !== "" || (r.lines ?? "").trim() !== "")
    .map((r): DeployOnlyRule => (r.lines ? { path: r.path, lines: r.lines } : { path: r.path }));
  const out: ManifestRepo = { ...repo };
  if (!(repo.base_branch ?? "").trim()) delete out.base_branch;
  delete out.deploy_only;
  if (rules.length > 0) out.deploy_only = rules;
  const shared = (repo.shared_paths ?? []).filter((r) => r.path.trim() !== "");
  delete out.shared_paths;
  if (shared.length > 0) out.shared_paths = shared;
  const protectedPaths = (repo.protected_paths ?? [])
    .filter((r) => r.path.trim() !== "")
    .map((r) => ((r.reason ?? "").trim() ? { path: r.path, reason: r.reason } : { path: r.path }));
  delete out.protected_paths;
  if (protectedPaths.length > 0) out.protected_paths = protectedPaths;
  const apps = (repo.independent_apps ?? []).filter((a) => a.trim() !== "");
  delete out.independent_apps;
  if (apps.length > 0) out.independent_apps = apps;
  if (!repo.batch_within_apps) delete out.batch_within_apps;
  if (!repo.skip_retest_across_apps) delete out.skip_retest_across_apps;
  return out;
}

// A directory is only a directory with its trailing slash; without it the
// entry is a file pattern that silently matches nothing. We can't know which
// was meant, so an entry with no wildcard and no extension is only flagged.
export function looksLikeDirectoryMissingSlash(path: string): boolean {
  const p = path.trim();
  if (p === "" || p.endsWith("/") || /[*?]/.test(p)) return false;
  return !/\.[^./]+$/.test(p);
}

function SharedPathsEditor({ repoIndex, rules, onChange }: {
  repoIndex: number;
  rules: SharedPathRule[];
  onChange: (rules: SharedPathRule[]) => void;
}) {
  const n = repoIndex + 1;
  const update = (k: number, patch: Partial<SharedPathRule>) =>
    onChange(rules.map((r, j) => (j === k ? { ...r, ...patch } : r)));
  return (
    <fieldset className="manifest-section__rules-edit">
      <legend>Shared paths of repo {n}</legend>
      <p className="manifest-section__hint">End with / for a directory; otherwise it is a file pattern.</p>
      {rules.map((rule, k) => (
        <div key={k} className="manifest-section__edit-row">
          <input
            aria-label={`Shared path ${k + 1} of repo ${n}`}
            value={rule.path}
            placeholder="server/internal/schema/migrations/"
            onChange={(e) => update(k, { path: e.target.value })}
          />
          {looksLikeDirectoryMissingSlash(rule.path) && (
            <span role="status" className="manifest-section__warning">
              Is this a directory? Without a trailing / it is read as a file pattern and probably matches nothing.
            </span>
          )}
          <Dropdown
            label={`How a clash is resolved, shared path ${k + 1} of repo ${n}`}
            value={rule.resolve}
            options={RESOLVE_OPTIONS}
            onChange={(value) => update(k, { resolve: value })}
          />
          <Button variant="danger" onClick={() => onChange(rules.filter((_, j) => j !== k))}>
            Remove shared path {k + 1} of repo {n}
          </Button>
        </div>
      ))}
      <div className="manifest-section__actions">
        <Button onClick={() => onChange([...rules, { path: "", resolve: "migration" }])}>
          Add shared path to repo {n}
        </Button>
      </div>
    </fieldset>
  );
}

function IndependentAppsEditor({ repoIndex, apps, batch, skipRetest, onChange, onBatchChange, onSkipRetestChange }: {
  repoIndex: number;
  apps: string[];
  batch: boolean;
  skipRetest: boolean;
  onChange: (apps: string[]) => void;
  onBatchChange: (on: boolean) => void;
  onSkipRetestChange: (on: boolean) => void;
}) {
  const n = repoIndex + 1;
  return (
    <fieldset className="manifest-section__rules-edit">
      <legend>Apps of repo {n}</legend>
      <p className="manifest-section__hint">
        Directories that build, test and deploy on their own, e.g. <code>slop-haus/</code>. A change inside one can't
        break another's tests. A trailing / is added if missing. Used by: merge batching, re-test skipping.
      </p>
      {apps.map((app, k) => (
        <div key={k} className="manifest-section__edit-row">
          <input
            aria-label={`Independent app ${k + 1} of repo ${n}`}
            value={app}
            placeholder="slop-haus/"
            onChange={(e) => onChange(apps.map((a, j) => (j === k ? e.target.value : a)))}
          />
          <Button variant="danger" onClick={() => onChange(apps.filter((_, j) => j !== k))}>
            Remove independent app {k + 1} of repo {n}
          </Button>
        </div>
      ))}
      <div className="manifest-section__actions">
        <Button onClick={() => onChange([...apps, ""])}>Add independent app to repo {n}</Button>
      </div>
      <label className="manifest-section__toggle">
        <input type="checkbox" checked={batch} onChange={(e) => onBatchChange(e.target.checked)} /> Batch merges within
        an app, for repo {n}
      </label>
      <p className="manifest-section__hint">
        Changes that each touch only one app may land as one tested batch. Also needs a batch size above 1 in{" "}
        <Link to="/settings">Tenant Settings</Link>; changes touching files outside the apps land alone.
      </p>
      <label className="manifest-section__toggle">
        <input type="checkbox" checked={skipRetest} onChange={(e) => onSkipRetestChange(e.target.checked)} /> Skip
        re-testing when main moved only in other apps, for repo {n}
      </label>
      <p className="manifest-section__hint">
        A change behind main may merge on its existing CI when main moved only in other apps.
      </p>
    </fieldset>
  );
}

function DeployOnlyEditor({ repoIndex, rules, onChange }: {
  repoIndex: number;
  rules: DeployOnlyRule[];
  onChange: (rules: DeployOnlyRule[]) => void;
}) {
  const n = repoIndex + 1;
  const update = (k: number, patch: Partial<DeployOnlyRule>) =>
    onChange(rules.map((r, j) => (j === k ? { ...r, ...patch } : r)));
  return (
    <fieldset className="manifest-section__rules-edit">
      <legend>Deploy-only files of repo {n}</legend>
      {rules.map((rule, k) => (
        <div key={k} className="manifest-section__edit-row">
          <input
            aria-label={`Deploy-only file ${k + 1} of repo ${n}`}
            value={rule.path}
            placeholder="deploy/*/kustomization.yaml"
            onChange={(e) => update(k, { path: e.target.value })}
          />
          <input
            aria-label={`Only lines matching, file ${k + 1} of repo ${n}`}
            value={rule.lines ?? ""}
            placeholder="any line (optional regex)"
            onChange={(e) => update(k, { lines: e.target.value })}
          />
          <Button variant="danger" onClick={() => onChange(rules.filter((_, j) => j !== k))}>
            Remove deploy-only file {k + 1} of repo {n}
          </Button>
        </div>
      ))}
      <div className="manifest-section__actions">
        <Button onClick={() => onChange([...rules, { path: "" }])}>Add deploy-only file to repo {n}</Button>
      </div>
    </fieldset>
  );
}

function ManifestEditor({ initial, busy, onSave, onCancel }: {
  initial: ManifestRepo[];
  busy: boolean;
  onSave: (repos: ManifestRepo[]) => void;
  onCancel: () => void;
}) {
  const [rows, setRows] = useState<ManifestRepo[]>(initial.map((r) => ({ ...r })));
  const update = (i: number, patch: Partial<ManifestRepo>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    <form
      className="manifest-section__form"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(rows.map(forSave));
      }}
    >
      <p className="manifest-section__hint">
        A path is the repo&rsquo;s directory under the workspace root. Deploy-only file patterns are relative to that
        repo&rsquo;s own root; see &ldquo;How workspaces work&rdquo; before adding any. A shared path ends in{" "}
        <code>/</code> for a directory (or is a file pattern): tickets whose scopes overlap only inside one are never
        kept apart, and the overlap is shown as shared.
      </p>
      {rows.map((row, i) => (
        <div key={i} className="manifest-section__edit-repo">
          <div className="manifest-section__edit-row">
            <input
              aria-label={`Path ${i + 1}`}
              value={row.path}
              placeholder="app"
              onChange={(e) => update(i, { path: e.target.value })}
            />
            <input
              aria-label={`Remote ${i + 1}`}
              value={row.remote}
              placeholder="github.com/acme/app"
              onChange={(e) => update(i, { remote: e.target.value })}
            />
            <Button variant="danger" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
              Remove repo {i + 1}
            </Button>
          </div>
          <div className="manifest-section__field">
            <label htmlFor={`base-branch-${i}`}>Base branch</label>
            <input
              id={`base-branch-${i}`}
              aria-describedby={`base-branch-hint-${i}`}
              aria-label={`Base branch ${i + 1}`}
              value={row.base_branch ?? ""}
              placeholder="slop-haus-<tenant>"
              onChange={(e) => update(i, { base_branch: e.target.value })}
            />
            <span id={`base-branch-hint-${i}`} className="manifest-section__hint">
              Branch this repo&rsquo;s changes are cut from and merged into; empty uses the repo&rsquo;s default branch.
              Created from the default branch&rsquo;s tip if missing. Getting it into the default branch is a pull
              request you open.
            </span>
          </div>
          <DeployOnlyEditor
            repoIndex={i}
            rules={row.deploy_only ?? []}
            onChange={(rules) => update(i, { deploy_only: rules })}
          />
          <SharedPathsEditor
            repoIndex={i}
            rules={row.shared_paths ?? []}
            onChange={(rules) => update(i, { shared_paths: rules })}
          />
          <ProtectedPathsEditor
            repoIndex={i}
            rules={row.protected_paths ?? []}
            onChange={(rules) => update(i, { protected_paths: rules })}
          />
          <IndependentAppsEditor
            repoIndex={i}
            apps={row.independent_apps ?? []}
            batch={row.batch_within_apps ?? false}
            skipRetest={row.skip_retest_across_apps ?? false}
            onChange={(apps) => update(i, { independent_apps: apps })}
            onBatchChange={(on) => update(i, { batch_within_apps: on })}
            onSkipRetestChange={(on) => update(i, { skip_retest_across_apps: on })}
          />
        </div>
      ))}
      <div className="manifest-section__actions">
        <Button onClick={() => setRows((rs) => [...rs, { path: "", remote: "" }])}>Add repo</Button>
        <Button type="submit" variant="primary" disabled={busy}>
          Save
        </Button>
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

type Mode = "view" | "edit" | "confirm_remove";

// ManifestSection is where the tenant's owners declare the workspace's
// shape. Until they do, nothing is enforced, so the section says that and
// offers what the agents' reports suggest. Authorisation is the server's:
// can_edit only decides which controls to show.
export default function ManifestSection({ workspace, onChanged }: { workspace: Workspace; onChanged: () => void }) {
  const { manifest, proposal, can_edit: canEdit } = workspace;
  const [mode, setMode] = useState<Mode>("view");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [branchNotes, setBranchNotes] = useState<BaseBranchResult[]>([]);

  async function run(action: () => Promise<{ base_branches?: BaseBranchResult[] } | unknown>) {
    setBusy(true);
    setError(null);
    setBranchNotes([]);
    try {
      const saved = await action();
      const results = (saved as { base_branches?: BaseBranchResult[] } | undefined)?.base_branches ?? [];
      setBranchNotes(results.filter((r) => r.status !== "ready"));
      setMode("view");
      onChanged();
    } catch (err) {
      setError(errorMessage(err, "That did not work. Try again."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="manifest-section" aria-label="Manifest">
      <h3 className="manifest-section__heading">Manifest</h3>
      {error && (
        <p role="alert" className="manifest-section__error">
          {error}
        </p>
      )}

      {branchNotes.map((n) => (
        <p
          key={n.path}
          role={n.status === "default_branch" ? "status" : "alert"}
          className={n.status === "default_branch" ? "manifest-section__hint" : "manifest-section__error"}
        >
          Base branch of {n.path}: {n.message}
          {n.status === "failed" || n.status === "unavailable" ? " The setting is saved; save again to retry." : ""}
        </p>
      ))}

      {manifest === null ? (
        <>
          <p>No manifest is declared, so nothing about the workspace layout is enforced.</p>
          {proposal && (
            <ProposalView
              proposal={proposal}
              canEdit={canEdit}
              busy={busy}
              onAdopt={() => void run(() => apiClient.setWorkspaceManifest(proposal.repos))}
            />
          )}
          {!canEdit && <p className="manifest-section__hint">Only the tenant&rsquo;s owners can adopt a manifest.</p>}
        </>
      ) : mode === "edit" ? (
        <ManifestEditor
          initial={manifest.repos}
          busy={busy}
          onSave={(repos) => void run(() => apiClient.setWorkspaceManifest(repos))}
          onCancel={() => {
            setError(null);
            setMode("view");
          }}
        />
      ) : (
        <>
          <RepoTable repos={manifest.repos} />
          <SyncRepos divergences={workspace.divergences} canEdit={canEdit} onSynced={onChanged} />
          {canEdit && mode === "view" && <MoveRepo repos={manifest.repos} onMoved={onChanged} />}
          {canEdit ? (
            <div className="manifest-section__actions">
              <Button onClick={() => setMode("edit")} disabled={mode === "confirm_remove"}>
                Edit
              </Button>
              <Button variant="danger" onClick={() => setMode("confirm_remove")} disabled={mode === "confirm_remove"}>
                Remove manifest
              </Button>
            </div>
          ) : (
            <p className="manifest-section__hint">Only the tenant&rsquo;s owners can change the manifest.</p>
          )}
          {mode === "confirm_remove" && (
            <div className="manifest-section__confirm" role="group" aria-label="Confirm removing the manifest">
              <p>
                Removing the manifest stops all layout checking: every agent goes back to being only reported, and
                nothing is enforced.
              </p>
              <div className="manifest-section__actions">
                <Button variant="danger" onClick={() => void run(() => apiClient.clearWorkspaceManifest())} disabled={busy}>
                  Remove it
                </Button>
                <Button variant="ghost" onClick={() => setMode("view")} disabled={busy}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}
