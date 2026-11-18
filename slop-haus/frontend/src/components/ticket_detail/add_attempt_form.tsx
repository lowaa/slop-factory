import { Button, Dropdown, type DropdownOption } from "@slop-factory/ui";
import type { VariantOptions } from "./use_variant_picker";
import { MAX_LIVE_ATTEMPTS, NONE, type AddAttemptsActions, type AttemptCapacity } from "./use_add_attempts";
import "./add_attempt_form.scss";

interface AddAttemptFormProps {
  variants: VariantOptions;
  form: AddAttemptsActions;
}

// Several attempts at one ticket, each routed to a variant version (or to
// none), added together. Nothing is saved between uses: running a set again
// is what replay batches are for.
export default function AddAttemptForm({ variants, form }: AddAttemptFormProps) {
  if (form.unavailable) return null;
  const options: DropdownOption[] = [
    { value: NONE, label: "None: only agents in no variant claim it" },
    ...variants.options.map((o) => ({ value: o.id, label: `${o.name} v${o.version}` })),
  ];
  const { rows, submit, capacity, replay } = form;
  const full = capacity.remaining === 0;
  const tooMany = rows.length > capacity.remaining;
  const count = rows.length === 1 ? "1 attempt" : `${rows.length} attempts`;
  return (
    <section className="add-attempt-form">
      <h2>{replay ? "Replay this ticket" : "Add attempts"}</h2>
      {replay && (
        <p className="ticket-detail__hint add-attempt-form__replay-warning" role="note">
          This ticket is done. A replay runs it again as a shadow attempt, from the base commit the original started at and
          on the ticket as it was first given. It never merges and the ticket stays done, but its agent is a real agent: a
          ticket whose work deploys, pushes to main or touches live systems may do so unless the replay is stopped, so
          you choose which tickets to replay.
        </p>
      )}
      <p className="ticket-detail__hint">
        Each row is one more {replay ? "replay" : "attempt at this ticket"}. They are added together, or not at all if that would pass the
        cap of {MAX_LIVE_ATTEMPTS} live attempts.
      </p>
      <p className="ticket-detail__hint">
        This ticket has {capacity.live} of {MAX_LIVE_ATTEMPTS} live attempts;{" "}
        {full ? "no more can be added until one finishes." : `${capacity.remaining} more can be added.`}
      </p>
      <form onSubmit={form.handleSubmit}>
        <ul>
          {rows.map((row, i) => (
            <li key={row.key}>
              <Dropdown
                label={`Variant for new attempt ${i + 1}`}
                value={row.versionId}
                options={options}
                disabled={submit.busy}
                onChange={(value) => form.setVersion(row.key, value)}
              />
              {rows.length > 1 && (
                <Button variant="ghost" disabled={submit.busy} onClick={() => form.removeRow(row.key)}>
                  Remove row {i + 1}
                </Button>
              )}
            </li>
          ))}
        </ul>
        {variants.loadFailed && (
          <p role="status" className="ticket-detail__hint">
            Couldn&apos;t load the variant list, so only "none" is offered -- try again in a moment.
          </p>
        )}
        {submit.error && (
          <p role="alert" className="ticket-detail__notice">
            {submit.error}
          </p>
        )}
        {tooMany && !full && (
          <p role="alert" className="ticket-detail__notice">
            Only {capacity.remaining} more can be added: remove a row.
          </p>
        )}
        {form.confirming && (
          <div className="add-attempt-form__confirm" role="group" aria-label="Confirm new attempts">
            <p>
              Add {count}? {replay ? "They run as shadows and never merge; the ticket stays done." : consequence(capacity.promoted)}
            </p>
            <div className="add-attempt-form__actions">
              <Button variant="primary" disabled={submit.busy} onClick={form.confirm}>
                Confirm: add {count}
              </Button>
              <Button variant="ghost" onClick={form.cancelConfirm}>
                Cancel
              </Button>
            </div>
          </div>
        )}
        <div className="add-attempt-form__actions">
          <Button onClick={form.addRow} disabled={submit.busy || rows.length >= capacity.remaining}>
            Add another
          </Button>
          <Button type="submit" variant="primary" disabled={submit.busy || full || tooMany}>
            {submit.busy ? "Adding…" : rows.length === 1 ? "Add attempt" : `Add ${rows.length} attempts`}
          </Button>
        </div>
      </form>
    </section>
  );
}

function consequence(promoted: AttemptCapacity["promoted"]): string {
  if (!promoted) return "";
  return promoted.started
    ? `Attempt ${promoted.number} has started work, so the new attempts are shadows: they run, but a human chooses whether any replaces it.`
    : `Attempt ${promoted.number} has not started work, so it loses its promotion.`;
}
