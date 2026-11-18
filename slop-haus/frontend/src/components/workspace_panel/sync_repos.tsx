import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type BranchDivergence, type RepoSync } from "@/api";
import "./manifest_section.scss";
import { errorMessage } from "@/lib/use_async_action";

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function outcomeText(d: BranchDivergence, r: RepoSync): string {
  switch (r.outcome) {
    case "merged":
      return `Merged ${r.default_branch} into ${d.base_branch}; it is now at ${r.head?.slice(0, 12)}.`;
    case "up_to_date":
      return `${d.base_branch} already has everything on ${r.default_branch}.`;
    case "conflict":
      return `${r.default_branch} conflicts with ${d.base_branch}, so nothing changed. Ticket #${r.ticket_id} was filed (in preparing) to resolve it on a branch and merge it through the normal flow.`;
  }
}

// SyncRepos shows how far each integration branch is behind and ahead of its
// repo's default branch, and lets an owner merge the default branch in. The
// counts are read by the server from the code host when the page loads.
// Getting an integration branch into the default branch is a pull request on
// the code host, by a person: there is no button for it.
export default function SyncRepos({
  divergences,
  canEdit,
  onSynced,
}: {
  divergences: BranchDivergence[];
  canEdit: boolean;
  onSynced: () => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [notes, setNotes] = useState<Record<string, { text: string; error: boolean }>>({});

  if (divergences.length === 0) return null;

  async function sync(d: BranchDivergence) {
    setBusy(d.path);
    setNotes((n) => ({ ...n, [d.path]: { text: "", error: false } }));
    try {
      const result = await apiClient.syncWorkspaceRepo(d.path);
      setNotes((n) => ({ ...n, [d.path]: { text: outcomeText(d, result), error: result.outcome === "conflict" } }));
      onSynced();
    } catch (err) {
      const text = errorMessage(err, "That did not work. Try again.");
      setNotes((n) => ({ ...n, [d.path]: { text, error: true } }));
    } finally {
      setBusy(null);
    }
  }

  return (
    <ul className="manifest-section__rules" aria-label="Integration branches">
      {divergences.map((d) => (
        <li key={d.path}>
          <code>{d.path}</code>: <code>{d.base_branch}</code>{" "}
          {d.error ? (
            <span className="manifest-section__error">could not be compared ({d.error})</span>
          ) : (
            <span>
              is {plural(d.behind, "commit")} behind <code>{d.default_branch}</code>, {d.ahead} ahead
            </span>
          )}
          {canEdit && !d.error && (
            <Button onClick={() => void sync(d)} disabled={busy !== null || d.behind === 0}>
              Sync from {d.default_branch}
            </Button>
          )}
          {notes[d.path]?.text && (
            <p role={notes[d.path].error ? "alert" : "status"} className={notes[d.path].error ? "manifest-section__error" : "manifest-section__hint"}>
              {notes[d.path].text}
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}
