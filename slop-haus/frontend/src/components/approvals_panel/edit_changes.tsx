import type { ReactNode } from "react";
import type { Proposal } from "@/api";
import { BodyDiff } from "@/components/ticket_detail";
import { SharedOverlapTag } from "@/components/shared_overlap_tag";
import "./edit_changes.scss";

type EditChange = NonNullable<Proposal["edit"]>;

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="edit-changes__field">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function PathList({ paths, sign }: { paths: string[]; sign: "+" | "-" }) {
  return (
    <ul className="edit-changes__paths">
      {paths.map((p) => (
        <li key={p} className={sign === "+" ? "edit-changes__path--added" : "edit-changes__path--removed"}>
          {sign} {p}
        </li>
      ))}
    </ul>
  );
}

// EditChanges is exactly what approving an edit proposal writes, against the
// ticket as it is now: only the fields that would differ. Everything here is
// the proposing agent's own text, shown as text and never as markup. A
// decided proposal has no ticket to compare to, so it shows what was proposed.
export default function EditChanges({ edit, ticketId }: { edit: EditChange; ticketId: number }) {
  const pathsShown = edit.paths_added !== undefined || edit.paths_removed !== undefined;
  return (
    <section className="edit-changes" aria-label={`Changes to #${ticketId}`}>
      <dl>
        {edit.title && (
          <Field label="Title">
            {edit.title.old !== undefined && <del className="edit-changes__old">{edit.title.old}</del>}
            <ins className="edit-changes__new">{edit.title.new}</ins>
          </Field>
        )}
        {edit.priority && (
          <Field label="Priority">
            {edit.priority.old !== undefined && <del className="edit-changes__old">{edit.priority.old}</del>}
            <ins className="edit-changes__new">{edit.priority.new}</ins>
          </Field>
        )}
        {edit.body && (
          <Field label="Description">
            {edit.body.diff ? (
              <BodyDiff lines={edit.body.diff} />
            ) : (
              <pre className="edit-changes__plain">{edit.body.new}</pre>
            )}
          </Field>
        )}
        {edit.paths && (
          <Field label="Paths">
            {pathsShown ? (
              <>
                <PathList paths={edit.paths_added ?? []} sign="+" />
                <PathList paths={edit.paths_removed ?? []} sign="-" />
                {(edit.paths_added ?? []).length + (edit.paths_removed ?? []).length === 0 && (
                  <span>Unchanged from the ticket now.</span>
                )}
              </>
            ) : (
              <ul className="edit-changes__paths">
                {edit.paths.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            )}
          </Field>
        )}
      </dl>
      {edit.open && <p className="edit-changes__opens">and moves it to open</p>}
      {edit.overlaps && edit.overlaps.length > 0 && (
        <div role="note" className="edit-changes__overlaps">
          <strong>These paths overlap live tickets</strong>, so they can't be claimed in parallel, unless marked shared. A real
          conflict is fine.
          <ul>
            {edit.overlaps.map((o) => (
              <li key={o.ticket_id}>
                #{o.ticket_id} {o.title} ({o.status}): {o.path} overlaps {o.other}
                <SharedOverlapTag resolve={o.resolve} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
