import { apiClient, type WorkspaceAgent } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import Timestamp from "@/shared/timestamp/timestamp";
import { FleetInstructionsWidget } from "@/components/fleet_instructions";
import { VariantsWidget } from "@/components/variants";
import ManifestSection from "./manifest_section";
import WorkspaceHelp from "./workspace_help";
import "./workspace_panel.scss";

type Layout = NonNullable<WorkspaceAgent["layout"]>;
type Entry = Layout["root"];

function describeEntry(entry: Entry): string {
  if (!entry.git) return "not a git repo";
  return entry.origin === "" ? "git repo, no origin" : entry.origin;
}

function LayoutEntries({ layout }: { layout: Layout }) {
  return (
    <ul className="workspace-panel__entries">
      {[layout.root, ...layout.repos].map((entry) => (
        <li key={entry.path}>
          <code>{entry.path === "." ? "./ (root)" : `${entry.path}/`}</code>
          <span className="workspace-panel__origin">{describeEntry(entry)}</span>
        </li>
      ))}
    </ul>
  );
}

const MISMATCH_PROBLEM = {
  no_repo: () => "no git repository found there (missing, or not a git repo)",
  no_origin: () => "repository has no origin",
  wrong_origin: (found: string) => `origin is ${found}`,
  root_is_repo: () => "the workspace root must not be a repo: repos go in subfolders",
} as const;

const STATUS_NOTE: Partial<Record<WorkspaceAgent["status"], string>> = {
  reported: "No manifest to compare with.",
  matches: "Matches the manifest.",
};

function AgentStatus({ entry }: { entry: WorkspaceAgent }) {
  return (
    <li className="workspace-panel__row">
      <div className="workspace-panel__who">
        <span className="workspace-panel__name">{entry.agent_name}</span>
        <span className={`workspace-panel__status workspace-panel__status--${entry.status}`}>{entry.status}</span>
      </div>
      <div>
        {entry.status === "unknown" && (
          <p className="workspace-panel__unknown">
            Has not reported its workspace. Restart the agent in it.
          </p>
        )}
        {entry.status === "mismatch" && (
          <ul className="workspace-panel__mismatches">
            {entry.mismatches.map((m) => (
              <li key={m.path}>
                <code>{m.path}</code>
                {m.problem === "root_is_repo" ? ": " : <> expected <code>{m.expected}</code>: </>}
                {MISMATCH_PROBLEM[m.problem](m.found)}
              </li>
            ))}
          </ul>
        )}
        {STATUS_NOTE[entry.status] && <p className="workspace-panel__unknown">{STATUS_NOTE[entry.status]}</p>}
        {entry.layout && entry.reported_at && (
          <>
            <LayoutEntries layout={entry.layout} />
            <p className="workspace-panel__when">
              Reported <Timestamp at={entry.reported_at} />
            </p>
          </>
        )}
      </div>
    </li>
  );
}

// WorkspacePanel shows the tenant's manifest and how each agent's workspace
// compares with it. Layouts are self-asserted by the agent's machine: the
// server does the comparing, this only displays it.
export default function WorkspacePanel() {
  const state = usePolledResource(() => apiClient.getWorkspace(), { fallbackError: "Failed to load the workspace." });

  return (
    <div className="workspace-panel">
      <h2 className="workspace-panel__heading">Workspace</h2>
      <WorkspaceHelp />

      {state.status === "loading" && <p>Loading workspace…</p>}
      {state.status === "error" && (
        <p role="alert" className="workspace-panel__notice">
          {state.error}
        </p>
      )}

      {state.status === "ready" && state.data && (
        <>
          <ManifestSection workspace={state.data} onChanged={() => void state.reload()} />
          <FleetInstructionsWidget />
          <VariantsWidget />
          {state.data.agents.length === 0 ? (
            <p className="workspace-panel__empty">No enabled agents to report a workspace.</p>
          ) : (
            <ul className="workspace-panel__list">
              {state.data.agents.map((entry) => (
                <AgentStatus key={entry.agent_id} entry={entry} />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
}
