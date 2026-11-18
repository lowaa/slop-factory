import type { Priority, Status, Ticket } from "@/api";
import { parseStatus } from "@/domain/status";
import { Dropdown } from "@slop-factory/ui";
import PathsEditor from "./paths_editor";
import ScopeOverlaps from "./scope_overlaps";
import type { usePathsEditor } from "./use_paths_editor";
import type { TicketStore } from "./use_ticket_store";

const PRIORITIES: Priority[] = ["low", "normal", "high", "urgent"];
const PRIORITY_OPTIONS = PRIORITIES.map((value) => ({ value, label: value }));

// The wire values are snake_case; the dropdown shows them as words. The
// mapping lives here rather than in the widget, which has no business
// knowing what a ticket status is.
function statusOptions(statuses: Status[]) {
  return statuses.map((value) => ({ value, label: value.replace("_", " ") }));
}

export function TicketFields({
  store,
  ticket,
}: {
  store: TicketStore;
  ticket: Ticket;
}) {
  return (
    <>
      <div className="ticket-detail__fields">
        {/* Only the moves the server says it will accept, so the
          rules live in one place (ticket.go's humanMove); the
          current status is among them, which the select needs to
          show its value. */}
        <Dropdown
          label="Status"
          value={ticket.status}
          options={statusOptions(ticket.allowed_moves)}
          onChange={(value) => {
            const status = parseStatus(value);
            if (status) store.patchField({ status });
          }}
        />
        <Dropdown
          label="Priority"
          value={ticket.priority}
          options={PRIORITY_OPTIONS}
          onChange={(priority) =>
            store.patchField({ priority })
          }
        />
        <label className="ticket-detail__checkbox-field">
          <input
            type="checkbox"
            checked={ticket.needs_discussion}
            onChange={(e) =>
              store.patchField({ needs_discussion: e.target.checked })
            }
          />
          <span>Needs discussion</span>
        </label>
        <label className="ticket-detail__checkbox-field">
          <input
            type="checkbox"
            checked={ticket.requires_approval}
            onChange={(e) =>
              store.patchField({ requires_approval: e.target.checked })
            }
          />
          <span>Requires human PR approval</span>
        </label>
      </div>
      <p className="ticket-detail__hint">
        Send this back for another planning pass -- the next agent to pick it up
        will ask before building.
      </p>
    </>
  );
}

export function TicketPaths({
  ticket,
  editor,
}: {
  ticket: Ticket;
  editor: ReturnType<typeof usePathsEditor>;
}) {
  return (
    <>
      <PathsEditor ticket={ticket} editor={editor} />

      <ScopeOverlaps ticket={ticket} />
    </>
  );
}
