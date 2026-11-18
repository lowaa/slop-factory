import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Ticket, type TicketAttempt } from "@/api";
import { useAsyncAction, errorMessage } from "@/lib/use_async_action";
import "./body_editor.scss";

// A replay is finished once it has opened its change: the PR is what a
// follow-up refers to.
export function followUpOffered(attempt: TicketAttempt): boolean {
  return !!attempt.replay && !!attempt.pr_url && ["review", "done", "closed"].includes(attempt.status);
}

// The branch an attempt works on is named by its ticket and number.
export function replayBranch(ticketId: number, attempt: number): string {
  return `ticket-${ticketId}-a${attempt}`;
}

export function followUpDraft(ticket: Ticket, attempt: TicketAttempt): { title: string; body: string } {
  const reference =
    `Reference: replay attempt ${attempt.number} of #${ticket.id}, PR ${attempt.pr_url}, ` +
    `branch \`${replayBranch(ticket.id, attempt.number)}\`. ` +
    `It started from the original's base commit, so use it as inspiration rather than rebasing it.`;
  return {
    title: `Follow-up to #${ticket.id}: ${ticket.title}`,
    body: ticket.body ? `${ticket.body}\n\n${reference}` : reference,
  };
}

// FollowUpAction files a ticket in preparing from a finished replay. It sends
// no base: a replay's own base is main as it was, and the follow-up starts
// from main's head.
export default function ReplayFollowUp({ ticket, attempt }: { ticket: Ticket; attempt: TicketAttempt }) {
  const [draft, setDraft] = useState<{ title: string; body: string } | null>(null);
  const [filed, setFiled] = useState<number | null>(null);
  const submit = useAsyncAction();
  if (!followUpOffered(attempt)) return null;

  if (filed !== null) {
    return (
      <p role="status">
        Filed <a href={`/tickets/${filed}`}>#{filed}</a> in preparing.
      </p>
    );
  }
  if (!draft) {
    return <Button onClick={() => setDraft(followUpDraft(ticket, attempt))}>Start a follow-up ticket</Button>;
  }
  return (
    <form
      aria-label="Follow-up ticket"
      className="ticket-detail__body-form"
      onSubmit={(e) => {
        e.preventDefault();
        void submit.run(async () => {
          const created = await apiClient.createTicket({ title: draft.title, body: draft.body, status: "preparing" });
          setFiled(created.id);
        }, (err) => errorMessage(err, "Could not file the follow-up ticket."));
      }}
    >
      {submit.error && (
        <p role="alert" className="ticket-detail__notice">
          {submit.error}
        </p>
      )}
      <label>
        <span>Title</span>
        <input type="text" value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} />
      </label>
      <label>
        <span>Description</span>
        <textarea value={draft.body} onChange={(e) => setDraft({ ...draft, body: e.target.value })} rows={8} />
      </label>
      <div className="ticket-detail__body-actions">
      <Button type="submit" variant="primary" disabled={submit.busy || !draft.title.trim()}>
        {submit.busy ? "Filing…" : "File in preparing"}
      </Button>
      <Button variant="ghost" disabled={submit.busy} onClick={() => setDraft(null)}>
        Cancel
      </Button>
      </div>
    </form>
  );
}
