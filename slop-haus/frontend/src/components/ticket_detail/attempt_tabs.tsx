import type { TicketAttempt } from "@/api";
import { attemptAccentClass } from "./attempt_accent";
import { isClosed, type AttemptLink } from "./attempt_view";
import "./attempt_tabs.scss";

interface AttemptTabsProps {
  attempts: TicketAttempt[];
  // The attempt on show; none is the ticket's own view.
  selected: number | undefined;
  link: AttemptLink;
}

// One link per attempt, finished ones included, beside the ticket's own
// view. Links rather than buttons: each tab has an address of its own. The
// Compare tab of a later part is one more entry in this list.
export default function AttemptTabs({ attempts, selected, link }: AttemptTabsProps) {
  return (
    <nav className="attempt-tabs" aria-label="Attempts">
      <ul>
        <li>
          {link(null, {
            className: selected === undefined ? "is-selected" : "",
            "aria-current": selected === undefined ? "page" : undefined,
            children: "Ticket",
          })}
        </li>
        {attempts.map((a) => (
          <li key={a.number}>
            {link(a.number, {
              className: `${attemptAccentClass(a.number)} ${selected === a.number ? "is-selected" : ""} ${isClosed(a) ? "is-closed" : ""}`,
              "aria-current": selected === a.number ? "page" : undefined,
              children: (
                <>
                  <span className="attempt-tabs__dot" aria-hidden="true" />
                  Attempt {a.number}
                  <span className="attempt-tabs__status">{a.status.replace("_", " ")}</span>
                </>
              ),
            })}
          </li>
        ))}
      </ul>
    </nav>
  );
}
