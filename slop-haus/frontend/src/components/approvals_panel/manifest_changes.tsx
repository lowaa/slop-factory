import type { Proposal } from "@/api";
import "./manifest_changes.scss";

type ManifestEdit = NonNullable<Proposal["manifest_edit"]>;
type RepoChange = NonNullable<ManifestEdit["changes"]>[number];
type Repo = ManifestEdit["repos"][number];

function Rules({ added, removed }: { added: string[]; removed: string[] }) {
  return (
    <ul className="manifest-changes__rules">
      {added.map((r) => (
        <li key={`+${r}`} className="manifest-changes__rule--added">
          + {r}
        </li>
      ))}
      {removed.map((r) => (
        <li key={`-${r}`} className="manifest-changes__rule--removed">
          - {r}
        </li>
      ))}
    </ul>
  );
}

const deployText = (d: { path: string; lines?: string }) =>
  d.lines ? `${d.path} (lines matching ${d.lines})` : d.path;
const sharedText = (s: { path: string; resolve: string }) =>
  `${s.path} (${s.resolve})`;

const protectedText = (p: { path: string; reason?: string }) =>
  p.reason ? `${p.path} (${p.reason})` : p.path;

const flagText = (on: boolean) => (on ? "on" : "off");

function RepoChangeItem({ change }: { change: RepoChange }) {
  const resolved = change.shared_paths_resolve_changed ?? [];
  return (
    <li className="manifest-changes__repo">
      <h4>
        <code>{change.path}</code>{" "}
        <span
          className={`manifest-changes__op manifest-changes__op--${change.op}`}
        >
          {change.op}
        </span>
      </h4>
      {change.op === "changed" && change.old_remote !== undefined && (
        <p>
          Remote: <del>{change.old_remote}</del> <ins>{change.new_remote}</ins>
        </p>
      )}
      {change.op !== "changed" && (
        <p>Remote: {change.new_remote ?? change.old_remote}</p>
      )}
      {((change.deploy_only_added ?? []).length > 0 ||
        (change.deploy_only_removed ?? []).length > 0) && (
        <section aria-label={`Deploy-only rules of ${change.path}`}>
          <h5>Deploy-only (loosens the merge gate)</h5>
          <Rules
            added={(change.deploy_only_added ?? []).map(deployText)}
            removed={(change.deploy_only_removed ?? []).map(deployText)}
          />
        </section>
      )}
      {change.base_branch_changed && (
        <section aria-label={`Base branch of ${change.path}`}>
          <h5>Base branch</h5>
          <p>
            {change.base_branch_changed.old || "default branch"} becomes {change.base_branch_changed.new || "default branch"}
          </p>
        </section>
      )}
      {((change.shared_paths_added ?? []).length > 0 ||
        (change.shared_paths_removed ?? []).length > 0 ||
        resolved.length > 0) && (
        <section aria-label={`Shared paths of ${change.path}`}>
          <h5>Shared paths (loosen scope exclusivity)</h5>
          <Rules
            added={(change.shared_paths_added ?? []).map(sharedText)}
            removed={(change.shared_paths_removed ?? []).map(sharedText)}
          />
          {resolved.length > 0 && (
            <ul className="manifest-changes__rules">
              {resolved.map((r) => (
                <li key={r.path}>
                  ~ {r.path}: resolved by {r.old} becomes {r.new}
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
      {((change.protected_paths_added ?? []).length > 0 ||
        (change.protected_paths_removed ?? []).length > 0) && (
        <section aria-label={`Protected paths of ${change.path}`}>
          <h5>Protected paths (what the steward may not decide)</h5>
          <Rules
            added={(change.protected_paths_added ?? []).map(protectedText)}
            removed={(change.protected_paths_removed ?? []).map(protectedText)}
          />
        </section>
      )}
      {((change.independent_apps_added ?? []).length > 0 ||
        (change.independent_apps_removed ?? []).length > 0 ||
        change.batch_within_apps_changed ||
        change.skip_retest_across_apps_changed) && (
        <section aria-label={`Independent apps of ${change.path}`}>
          <h5>Apps (batching and re-test skipping use them)</h5>
          <Rules
            added={change.independent_apps_added ?? []}
            removed={change.independent_apps_removed ?? []}
          />
          {change.batch_within_apps_changed && (
            <p>
              Batch merges within an app: {flagText(change.batch_within_apps_changed.old)} becomes{" "}
              {flagText(change.batch_within_apps_changed.new)}
            </p>
          )}
          {change.skip_retest_across_apps_changed && (
            <p>
              Skip re-testing when main moved only in other apps: {flagText(change.skip_retest_across_apps_changed.old)} becomes{" "}
              {flagText(change.skip_retest_across_apps_changed.new)}
            </p>
          )}
        </section>
      )}
    </li>
  );
}

function RepoSummary({ repo }: { repo: Repo }) {
  return (
    <li className="manifest-changes__repo">
      <h4>
        <code>{repo.path}</code>
      </h4>
      <p>Remote: {repo.remote}</p>
      {repo.base_branch && <p>Base branch: {repo.base_branch}</p>}
      <Rules
        added={(repo.deploy_only ?? []).map(
          (d) => `deploy-only ${deployText(d)}`,
        )}
        removed={[]}
      />
      <Rules
        added={(repo.shared_paths ?? []).map((s) => `shared ${sharedText(s)}`)}
        removed={[]}
      />
      <Rules
        added={(repo.protected_paths ?? []).map((p) => `protected ${protectedText(p)}`)}
        removed={[]}
      />
      <Rules
        added={(repo.independent_apps ?? []).map((a) => `independent app ${a}`)}
        removed={[]}
      />
      {repo.batch_within_apps && <p>Batch merges within an app: on</p>}
      {repo.skip_retest_across_apps && <p>Skip re-testing when main moved only in other apps: on</p>}
    </li>
  );
}

// ManifestChanges is what approving a manifest proposal does to the workspace
// manifest as it is now: repos, deploy-only rules and shared paths added,
// removed or changed, never the raw JSON. All of it is the proposing agent's
// text, shown as text. A decided proposal has no live manifest to compare with,
// so it lists what was proposed.
export default function ManifestChanges({ edit }: { edit: ManifestEdit }) {
  return (
    <section
      className="manifest-changes"
      aria-label="Changes to the workspace manifest"
    >
      {edit.changes === undefined ? (
        <>
          <p className="manifest-changes__note">
            The manifest proposed (revision {edit.base_revision} when it was
            made):
          </p>
          <ul>
            {edit.repos.map((r) => (
              <RepoSummary key={r.path} repo={r} />
            ))}
          </ul>
        </>
      ) : edit.changes.length === 0 ? (
        <p className="manifest-changes__note">
          No difference from the manifest now.
        </p>
      ) : (
        <ul>
          {edit.changes.map((c) => (
            <RepoChangeItem key={c.path} change={c} />
          ))}
        </ul>
      )}
    </section>
  );
}
