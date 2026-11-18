import { useState } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type ManifestRepo, type RepoMove } from "@/api";
import "./manifest_section.scss";
import { errorMessage } from "@/lib/use_async_action";

// MoveRepo changes a repo's path and, with it, the scopes of every open
// ticket and intake source under it. The preview is the server's dry run:
// nothing is written until the owner confirms what it lists.
export default function MoveRepo({ repos, onMoved }: { repos: ManifestRepo[]; onMoved: () => void }) {
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState(repos[0]?.path ?? "");
  const [to, setTo] = useState("");
  const [preview, setPreview] = useState<RepoMove | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run(dryRun: boolean) {
    setBusy(true);
    setError(null);
    try {
      const moved = await apiClient.moveWorkspaceRepo(from, to.trim(), dryRun);
      if (dryRun) {
        setPreview(moved);
      } else {
        setOpen(false);
        setPreview(null);
        onMoved();
      }
    } catch (err) {
      setPreview(null);
      setError(errorMessage(err, "That did not work. Try again."));
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <div className="manifest-section__actions">
        <Button onClick={() => setOpen(true)}>Move a repo</Button>
      </div>
    );
  }
  const intakeCount = preview?.intake_sources.length ?? 0;
  return (
    <form
      className="manifest-section__form"
      aria-label="Move a repo"
      onSubmit={(e) => {
        e.preventDefault();
        void run(true);
      }}
    >
      <p className="manifest-section__hint">
        Stop the fleet first: the move is refused while a ticket under the repo has a live lease or holds the
        integration lock. Every open ticket&rsquo;s scope and every intake source&rsquo;s paths under the repo are
        rewritten with it; done and cancelled tickets are left as they were.
      </p>
      {error && (
        <p role="alert" className="manifest-section__error">
          {error}
        </p>
      )}
      <div className="manifest-section__edit-row">
        <Dropdown
          label="Repo to move"
          value={from}
          options={repos.map((r) => ({ value: r.path, label: r.path }))}
          onChange={(value) => {
            setFrom(value);
            setPreview(null);
          }}
        />
        <input
          aria-label="New path"
          value={to}
          placeholder="app"
          onChange={(e) => { setTo(e.target.value); setPreview(null); }}
        />
        <Button type="submit" disabled={busy || to.trim() === ""}>
          Preview
        </Button>
        <Button variant="ghost" onClick={() => { setOpen(false); setPreview(null); setError(null); }} disabled={busy}>
          Cancel
        </Button>
      </div>
      {preview && (
        <div role="group" aria-label="Move preview">
          {preview.warnings.map((w) => (
            <p key={w} role="status" className="manifest-section__warning">
              {w}
            </p>
          ))}
          <p>
            Moving <code>{preview.from_path}</code> to <code>{preview.to_path}</code> rewrites{" "}
            {preview.tickets.length} open ticket{preview.tickets.length === 1 ? "" : "s"} and {intakeCount} intake
            source{intakeCount === 1 ? "" : "s"}.
          </p>
          <ul className="manifest-section__rules">
            {preview.tickets.map((t) => (
              <li key={t.id}>
                #{t.id} {t.title} <span className="manifest-section__hint">({t.status})</span>
                <ul>
                  {t.paths.map((p) => (
                    <li key={p.from}>
                      <code>{p.from}</code> → <code>{p.to}</code>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
            {preview.intake_sources.map((s) => (
              <li key={s.id}>
                Intake source {s.name}
                <ul>
                  {s.paths.map((p) => (
                    <li key={p.from}>
                      <code>{p.from}</code> → <code>{p.to}</code>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
          <div className="manifest-section__actions">
            <Button variant="primary" onClick={() => void run(false)} disabled={busy}>
              Move it
            </Button>
          </div>
        </div>
      )}
    </form>
  );
}
