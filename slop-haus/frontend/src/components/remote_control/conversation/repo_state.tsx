import type { ConversationDetail, HeldScopeOverlap, RepoStateBody } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { SharedOverlapTag } from "@/components/shared_overlap_tag";
import { shortCommit } from "@/domain/transcript";

// RepoState shows where the working copy stands after the newest turn (branch,
// head, uncommitted changes, ahead and behind its upstream) and warns of
// changes inside a scope an agent holds. The session runs in the working copy
// as it is and the owner or the model moves it, so this only says where it is:
// all of it is what the host reported, shown and never relied on, and nothing
// is blocked.
export default function RepoState({ detail }: { detail: ConversationDetail }) {
  return (
    <div className="repo-state">
      {detail.repo && <WorkingCopy repo={detail.repo} />}
      {detail.scope_overlaps.length > 0 && <ScopeOverlaps overlaps={detail.scope_overlaps} />}
    </div>
  );
}

function WorkingCopy({ repo }: { repo: RepoStateBody }) {
  const facts = [
    repo.branch === "" ? "detached head" : `branch ${repo.branch}`,
    repo.dirty ? `${repo.dirty_count} uncommitted` : "clean",
    repo.upstream === "" ? `${repo.ahead} on no remote branch` : `${repo.ahead} ahead, ${repo.behind} behind ${repo.upstream}`,
  ];
  const changed = repo.changed_paths ?? [];
  return (
    <div className="repo-state__full">
      <p className="repo-state__line">
        Working copy at <code>{shortCommit(repo.head)}</code>: {facts.join(", ")}.
      </p>
      {changed.length > 0 && (
        <details className="repo-state__changed">
          <summary>
            {repo.changed_total} {repo.changed_total === 1 ? "path" : "paths"} changed by this session
          </summary>
          <ul>
            {changed.map((path) => (
              <li key={path}>
                <code>{path}</code>
              </li>
            ))}
          </ul>
          {repo.changed_total > changed.length && <p>and {repo.changed_total - changed.length} more.</p>}
        </details>
      )}
    </div>
  );
}

// Shown in full they would push the transcript off the screen when many
// tickets hold paths the session changed, so the count is the summary and the
// rows are cut off. The alert is a div: role="alert" is not allowed on details.
const MAX_OVERLAPS_SHOWN = 20;

function ScopeOverlaps({ overlaps }: { overlaps: HeldScopeOverlap[] }) {
  const agents = new Set(overlaps.map((o) => o.ticket_id)).size;
  return (
    <div className="repo-state__overlaps" role="alert">
      <details>
        <summary>
          This session changed paths that {agents} {agents === 1 ? "agent holds" : "agents hold"}
        </summary>
        <ul>
          {overlaps.slice(0, MAX_OVERLAPS_SHOWN).map((o) => (
            <li key={`${o.ticket_id}:${o.path}`}>
              <code>{o.path}</code> is in the scope <code>{o.held}</code> of{" "}
              <TicketLink ticketId={o.ticket_id} from="board">
                #{o.ticket_id}
              </TicketLink>{" "}
              ({o.title}), which is {o.status === "review" ? "in review" : "being worked"}. Its agent's pushes may conflict.
              <SharedOverlapTag resolve={o.resolve} />
            </li>
          ))}
        </ul>
        {overlaps.length > MAX_OVERLAPS_SHOWN && <p>and {overlaps.length - MAX_OVERLAPS_SHOWN} more.</p>}
      </details>
    </div>
  );
}
