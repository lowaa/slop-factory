import type { Agent } from "@/api";

// What a supervisor last said about the steward, in a line for a person; null
// when there is nothing worth showing. The supervisor words a failure itself
// (it knows which harness and probe refused), so a failed reason is shown as sent.
export function describeStewardReport(observed: Agent["observed"]): string | null {
  if (!observed) return null;
  switch (observed.phase) {
    case "failed":
      return observed.reason || "The steward failed to start.";
    case "paused":
      return observed.reason ? `The steward is paused: ${observed.reason}` : "The steward is paused.";
    case "provisioning":
      return "The steward is starting.";
    default:
      return null;
  }
}
