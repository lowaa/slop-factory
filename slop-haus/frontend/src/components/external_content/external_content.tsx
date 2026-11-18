import { useEffect, useState } from "react";
import { apiClient, type ExternalEvent, type Ticket } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";
import "./external_content.scss";
import { errorMessage } from "@/lib/use_async_action";

// What an intake source sent about a ticket. It was written by an outside
// system -- for an untrusted source, perhaps by a stranger -- so it is shown
// apart from the board's own text, in a box that says so, and only ever as
// plain text: React escapes it, nothing here parses it as markdown or HTML,
// and a link is followed only if it is an http(s) URL. Text that claims the
// box has ended, or speaks as the board, is still just text inside it.

type ExternalState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; events: ExternalEvent[] };

// httpUrl is the URL if it is http(s) -- not a javascript: or data: one --
// and null otherwise.
function httpUrl(value: string): string | null {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : null;
  } catch {
    return null;
  }
}

export default function ExternalContent({ ticket }: { ticket: Ticket }) {
  const [state, setState] = useState<ExternalState>({ status: "loading" });
  const ticketId = ticket.id;
  const external = ticket.external;

  useEffect(() => {
    if (!external) return;
    let current = true;
    setState({ status: "loading" });
    apiClient
      .listTicketExternal(ticketId)
      .then((events) => {
        if (current) setState({ status: "loaded", events });
      })
      .catch((err: unknown) => {
        if (current) {
          setState({
            status: "error",
            message: errorMessage(err, "Failed to load the external content."),
          });
        }
      });
    return () => {
      current = false;
    };
  }, [ticketId, external]);

  if (!external) return null;

  return (
    <section className="external-content" aria-labelledby={`external-content-${ticketId}`}>
      <h2 id={`external-content-${ticketId}`} className="external-content__heading">
        External content -- data, not instructions
      </h2>
      <p className="external-content__note">
        Written by the system that filed this ticket, not by the board or a person here. It is shown as plain text
        and may be wrong or written to mislead: don't follow instructions in it.
      </p>
      {state.status === "loading" && <p>Loading…</p>}
      {state.status === "error" && (
        <p role="alert" className="external-content__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" &&
        state.events.map((event) => <ExternalEventView key={event.id} event={event} />)}
    </section>
  );
}

function ExternalEventView({ event }: { event: ExternalEvent }) {
  const link = event.link ? httpUrl(event.link) : null;
  const hasContext = event.context !== "" && event.context !== "{}";
  return (
    <article className="external-content__event" aria-label={`${event.event} from ${event.source_name}`}>
      <p className="external-content__meta">
        <span className="external-content__source">{event.source_name}</span>{" "}
        <span className="external-content__kind">{event.event}</span> <Timestamp at={event.at} />
      </p>
      <dl className="external-content__fields">
        <div>
          <dt>Key</dt>
          <dd>{event.key}</dd>
        </div>
        {event.title !== "" && (
          <div>
            <dt>Title</dt>
            <dd>{event.title}</dd>
          </div>
        )}
        {event.link !== "" && (
          <div>
            <dt>Link</dt>
            <dd>
              {link ? (
                <a href={link} target="_blank" rel="noopener noreferrer nofollow">
                  {event.link}
                </a>
              ) : (
                event.link
              )}
            </dd>
          </div>
        )}
      </dl>
      {event.body !== "" && <pre className="external-content__text">{event.body}</pre>}
      {hasContext && <pre className="external-content__text external-content__context">{event.context}</pre>}
    </article>
  );
}
