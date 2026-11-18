import type { SupervisorStatus } from "@/api";
import { formatDuration } from "@/lib/format_duration";
import { SLOT_REASONS } from "./slot_reasons";
import "./supervisor_slots.scss";

type Slot = SupervisorStatus["slots"][number];

const STALL_REASONS: Record<Slot["stall_reason"], string> = {
  idle_with_work: "idle while tickets are claimable",
  no_heartbeat: "no heartbeat from the supervisor",
  repeating_error: "stuck repeating the same error",
  held_without_run: "holds a ticket with no run",
  "": "",
};

type Props = { slots: Slot[]; now?: number };

export default function SupervisorSlots({ slots, now = Date.now() }: Props) {
  if (slots.length === 0) return null;
  return (
    <ul className="supervisor-slots" aria-label="Supervisor slots">
      {slots.map((slot) => {
        // The age comes from when the phase began, not from the report's own
        // figure, so it keeps counting between status fetches.
        const age = formatDuration((now - new Date(slot.phase_entered_at).getTime()) / 1000);
        return (
          <li
            key={slot.agent_id}
            className={`supervisor-slots__slot${slot.stalled ? " supervisor-slots__slot--stalled" : ""}${slot.paused_at ? " supervisor-slots__slot--paused" : ""}`}
          >
            <span className="supervisor-slots__agent">{slot.agent_name}</span>
            <span className="supervisor-slots__phase">
              {slot.phase} for {age}
            </span>
            {slot.paused_at && (
              <span className="supervisor-slots__paused" role="alert">
                Paused for {formatDuration((now - new Date(slot.paused_at).getTime()) / 1000)} (since{" "}
                {new Date(slot.paused_at).toLocaleString()})
                {/* Free text from the slot: a text node, never markup. */}
                {slot.pause_reason !== "" && <>: {slot.pause_reason}</>}
              </span>
            )}
            {slot.phase !== "running" && slot.reason !== "unknown" && (
              <span className="supervisor-slots__reason">
                {SLOT_REASONS[slot.reason]}
                {/* Free text from the supervisor: a text node, never markup. */}
                {slot.reason_detail !== "" && <> ({slot.reason_detail})</>}
              </span>
            )}
            {slot.ticket_id !== null && <span className="supervisor-slots__ticket">#{slot.ticket_id}</span>}
            {slot.error_class !== "" && (
              <span className="supervisor-slots__error">
                error: {slot.error_class} x{slot.error_streak}
              </span>
            )}
            {slot.bundle_status === "stale" && (
              <span
                className="supervisor-slots__stale"
                title={`The workspace's scripts (bundle ${slot.bundle_hash}) are not the ones the server serves now.`}
              >
                workspace scripts out of date
              </span>
            )}
            {slot.offline && <span className="supervisor-slots__offline">offline</span>}
            {slot.stalled && (
              <span className="supervisor-slots__stalled" role="alert">
                Stalled: {STALL_REASONS[slot.stall_reason] || slot.stall_reason}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
