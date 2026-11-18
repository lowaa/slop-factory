import type { TicketAttempt } from "@/api";
import { attemptAccentClass } from "./attempt_accent";
import { attemptChipText } from "./attempt_view";
import "./attempt_chip.scss";

// Beside the ticket's title while one of its attempts is on show, so the
// header cannot be mistaken for the ticket's own view.
export default function AttemptChip({ attempt }: { attempt: TicketAttempt }) {
  return <span className={`attempt-chip ${attemptAccentClass(attempt.number)}`}>{attemptChipText(attempt)}</span>;
}
