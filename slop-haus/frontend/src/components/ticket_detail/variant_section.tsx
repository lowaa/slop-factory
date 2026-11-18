import { Dropdown, type DropdownOption } from "@slop-factory/ui";
import type { Ticket, TicketAttempt, VariantOption } from "@/api";
import type { VariantOptions, VariantPickerActions } from "./use_variant_picker";
import "./variant_section.scss";

interface VariantSectionProps {
  ticket: Ticket;
  variants: VariantOptions;
  picker: VariantPickerActions;
  // The attempt to route; the promoted one (the ticket's only, before
  // attempts can be added) when left out.
  attempt?: TicketAttempt;
}

const NONE = "";

function label(v: { name: string; version: number }): string {
  return `${v.name} v${v.version}`;
}

// The attempt's variant: which agents will take it. Before attempts can be
// added a ticket has one, and this routes the whole ticket. The picker shows
// only while the variant can still be set (until the attempt's first lease);
// after that it is read-only text, since a new variant would mix two
// configurations in one attempt.
export default function VariantSection({ ticket, variants, picker, attempt: named }: VariantSectionProps) {
  // The promoted attempt is the one the ticket-level routing writes.
  const attempt = named ?? ticket.attempts?.find((a) => a.promoted);
  if (!attempt) return null;
  const { variant } = attempt;
  const settable = attempt.variant_settable;
  // Nothing to say or choose: no variant named, none to pick, and a fetch
  // that did not fail (which would otherwise hide why the picker is empty).
  if (!variant && (!settable || (variants.options.length === 0 && !variants.loadFailed))) return null;

  // An archived version the attempt already names stays selectable as itself,
  // so the select shows the truth rather than falling back to "none".
  const choices: Pick<VariantOption, "id" | "name" | "version">[] = [...variants.options];
  if (variant && !choices.some((o) => o.id === variant.version_id)) {
    choices.unshift({ id: variant.version_id, name: variant.name, version: variant.version });
  }

  const options: DropdownOption[] = [
    { value: NONE, label: "None: only agents in no variant claim it" },
    ...choices.map((o) => ({ value: o.id, label: label(o) })),
  ];

  return (
    <section className="ticket-detail__variant">
      <h2>Variant</h2>
      {settable ? (
        <div className="ticket-detail__variant-picker">
          <Dropdown
            label="Only agents on this variant version claim it"
            value={variant?.version_id ?? NONE}
            options={options}
            disabled={picker.saving}
            onChange={(value) => picker.choose(value === NONE ? null : value)}
          />
        </div>
      ) : (
        <p>
          Only agents on variant {variant ? label(variant) : "none"} claim this. It has been worked on, so this can no
          longer change.
        </p>
      )}
      {settable && variants.loadFailed && (
        <p role="status" className="ticket-detail__hint">
          Couldn&apos;t load the variants -- try again in a moment.
        </p>
      )}
      {picker.error && (
        <p role="alert" className="ticket-detail__notice">
          {picker.error}
        </p>
      )}
      {ticket.no_eligible_agent && variant && (
        <p role="status" className="ticket-detail__variant-warning">
          No enabled agent is on variant {label(variant)}, so no agent can claim this.
        </p>
      )}
    </section>
  );
}
