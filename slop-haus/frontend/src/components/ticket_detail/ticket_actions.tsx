import { apiClient, type Ticket } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { liveRecheck } from "@/lib/cooldown";
import { RecheckAt } from "@/components/ticket_card";
import type { TicketStore } from "./use_ticket_store";

// The status moves a person makes from the ticket page: open it out of
// Preparing, check a timer now, park and unpark.
export default function TicketActions({
  store,
  ticket,
}: {
  store: TicketStore;
  ticket: Ticket;
}) {
  const ticketId = ticket.id;
  const ready = useAsyncAction();
  // Park and Unpark share one action: only one of them is ever on screen.
  const park = useAsyncAction();
  const checkNow = useAsyncAction();

  // handleReady is the one way out of Preparing.
  function handleReady() {
    void store.statusButton(
      ready,
      () => apiClient.markTicketReady(ticketId),
      "Failed to open the ticket.",
    );
  }

  // handlePark sets a ticket aside, offered where the server's
  // allowed_moves says it may be, and handleUnpark brings it back to
  // Preparing -- never straight into the queue, so it is looked at again
  // before an agent can take it. Plain status PATCHes, as where the ticket
  // lands is fixed.
  function handlePark() {
    void store.statusButton(
      park,
      () => apiClient.updateTicket(ticketId, { status: "parked" }),
      "Failed to park the ticket.",
    );
  }

  // Check now is the board's ordinary move to open: a person's status write
  // ends the cooldown and its streak, and the server puts the ticket back in
  // the queue, or leaves it blocked while a dependency is unfinished.
  function handleCheckNow() {
    void store.statusButton(
      checkNow,
      () => apiClient.updateTicket(ticketId, { status: "open" }),
      "Failed to check the ticket now.",
    );
  }

  function handleUnpark() {
    void store.statusButton(
      park,
      () => apiClient.updateTicket(ticketId, { status: "preparing" }),
      "Failed to unpark the ticket.",
    );
  }

  return (
    <>
      {ticket.can_ready && (
        <div className="ticket-detail__preparing-banner">
          <div className="ticket-detail__preparing-banner-row">
            <p role="status">
              <strong>Not in the queue.</strong> This ticket is saved for you to
              keep refining -- no agent will pick it up until you open it.
            </p>
            <button type="button" onClick={handleReady} disabled={ready.busy}>
              {ready.busy ? "Opening…" : "Open"}
            </button>
          </div>
          {ready.error && (
            <p role="alert" className="ticket-detail__notice">
              {ready.error}
            </p>
          )}
        </div>
      )}

      {(() => {
        const recheck = liveRecheck(ticket);
        if (!recheck) return null;
        return (
          <div className="ticket-detail__cooldown">
            <p role="status">
              Waiting on a timer:{" "}
              <RecheckAt at={recheck} check={ticket.cooldown_releases} />
            </p>
            <button
              type="button"
              onClick={handleCheckNow}
              disabled={checkNow.busy}
            >
              {checkNow.busy ? "Checking…" : "Check now"}
            </button>
          </div>
        );
      })()}
      {checkNow.error && (
        <p role="alert" className="ticket-detail__notice">
          {checkNow.error}
        </p>
      )}

      {ticket.status !== "parked" &&
        ticket.allowed_moves.includes("parked") && (
          <div className="ticket-detail__park">
            <p>
              Not planning to do this? Parking keeps it, off the board and out
              of the queue.
            </p>
            <button type="button" onClick={handlePark} disabled={park.busy}>
              {park.busy ? "Parking…" : "Park"}
            </button>
          </div>
        )}

      {ticket.status === "parked" && (
        <div className="ticket-detail__preparing-banner ticket-detail__parked-banner">
          <div className="ticket-detail__preparing-banner-row">
            <p role="status">
              <strong>Parked.</strong> Kept, but not planned -- it is off the
              board unless &ldquo;Include parked&rdquo; is ticked, and no agent
              will pick it up. Unparking puts it back in Preparing.
            </p>
            <button type="button" onClick={handleUnpark} disabled={park.busy}>
              {park.busy ? "Unparking…" : "Unpark"}
            </button>
          </div>
        </div>
      )}

      {park.error && (
        <p role="alert" className="ticket-detail__notice">
          {park.error}
        </p>
      )}
    </>
  );
}
