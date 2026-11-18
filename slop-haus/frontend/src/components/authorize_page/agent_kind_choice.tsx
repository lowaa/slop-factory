import type { components } from "@/api_generated/schema";
import { MAX_SLOT_CAP, parseSlotCap } from "@/domain/slot_cap";
import "./authorize_page.scss";
type Schemas = components["schemas"];

export type NewAgentKind = NonNullable<Schemas["DecisionBody"]["agent_kind"]>;

interface AgentKindChoiceProps {
  name: string;
  kind: NewAgentKind;
  onChange: (kind: NewAgentKind) => void;
  // slotCap is the raw text of the input, so a half-typed value is not lost.
  slotCap: string;
  onSlotCap: (value: string) => void;
}

// Worker stays the preselection even though the device sign-in (not this
// page) is what worker launchers use: this page also serves anyone
// connecting a client by hand, and a wrong preselection would silently grant
// the console's wider reach, which the consent is the only gate on.
export function AgentKindChoice({ name, kind, onChange, slotCap, onSlotCap }: AgentKindChoiceProps) {
  return (
    <fieldset className="authorize-page__kind">
      <legend>This agent is</legend>
      <label>
        <input
          type="radio"
          name={name}
          value="worker"
          checked={kind === "worker"}
          onChange={() => onChange("worker")}
        />
        A worker
      </label>
      {kind === "worker" && <p className="authorize-page__kind-detail">A worker takes work from the queue.</p>}
      <label>
        <input
          type="radio"
          name={name}
          value="console"
          checked={kind === "console"}
          onChange={() => onChange("console")}
        />
        My interactive session (console)
      </label>
      {kind === "console" && (
        <p className="authorize-page__kind-detail">
          This session can propose your owner-only actions &mdash; answers, review decisions, edits &mdash; which apply
          at once if you have auto-approve on. It claims a ticket only when you name one, and never waits in the queue
          for work.
        </p>
      )}
      <label>
        <input
          type="radio"
          name={name}
          value="supervisor"
          checked={kind === "supervisor"}
          onChange={() => onChange("supervisor")}
        />
        A supervisor
      </label>
      {kind === "supervisor" && (
        <>
          <p className="authorize-page__kind-detail">
            A supervisor creates and retires worker agents for this machine, up to the number below, each with its own
            credential, without asking you again. Revoke or disable it to disable them all.
          </p>
          <label className="authorize-page__slot-cap">
            Workers it may run at once
            <input
              type="number"
              min={1}
              max={MAX_SLOT_CAP}
              step={1}
              value={slotCap}
              onChange={(e) => onSlotCap(e.target.value)}
              aria-invalid={parseSlotCap(slotCap) === null}
            />
          </label>
          {parseSlotCap(slotCap) === null && (
            <p className="authorize-page__kind-detail" role="note">
              Enter a whole number from 1 to {MAX_SLOT_CAP}.
            </p>
          )}
        </>
      )}
    </fieldset>
  );
}
