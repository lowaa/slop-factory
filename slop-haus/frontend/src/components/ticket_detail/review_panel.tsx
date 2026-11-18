import type { Ticket } from "@/api";
import AgentName from "@/shared/names/agent_name";
import MemberName from "@/shared/names/member_name";
import { NewTicketForm } from "@/components/new_ticket_form";
import PrLink from "@/shared/pr_link/pr_link";
import TicketLink from "@/shared/ticket_link/ticket_link";
import Timestamp from "@/shared/timestamp/timestamp";
import type { ReviewActions } from "./use_review_actions";
import "./review_panel.scss";

// awaitingPostReview mirrors the server's filter for the Unreviewed tab
// (server/internal/ticket/post_review.go's awaitingPostReview): a done
// ticket whose change merged without anyone approving it, and that nobody
// has marked reviewed since.
function awaitingPostReview(ticket: Ticket): boolean {
  return (
    ticket.status === "done" && !ticket.requires_approval && !ticket.approved_at && !ticket.post_reviewed_at
  );
}

function followUpDefaults(ticket: Ticket): { title: string; body: string } {
  return {
    title: `Follow-up to #${ticket.id}: `,
    body: `Found while reviewing #${ticket.id} ${ticket.title}${ticket.pr_url ? ` (${ticket.pr_url})` : ""}.`,
  };
}

interface ReviewBannerProps {
  prUrl: string | null;
  resumeAgentId: string | null;
  review: ReviewActions;
  // The attempt the banner is of, when it is one of several: the buttons
  // name it, since nothing else on the page's controls says so.
  attempt?: number;
}

// The banner for a change waiting on a human: the ticket's, or, on the page
// of one attempt of several, that attempt's.
export function ReviewBanner({ prUrl, resumeAgentId, review, attempt }: ReviewBannerProps) {
  return (
    <div className="ticket-detail__review-banner">
      <div className="ticket-detail__review-banner-row">
        <p role="status">
          <strong>Waiting on your review.</strong> The agent's PR is ready for you to review.{" "}
          <PrLink url={prUrl} />
          {resumeAgentId && (
            <>
              {" "}
              First claim goes back to <AgentName id={resumeAgentId} />.
            </>
          )}
        </p>
        <button type="button" onClick={review.handleApprove} disabled={review.approve.busy}>
          {review.approve.busy ? "Approving…" : attempt === undefined ? "Approve" : `Approve attempt ${attempt}`}
        </button>
      </div>
      {review.approve.error && (
        <p role="alert" className="ticket-detail__notice">
          {review.approve.error}
        </p>
      )}
      <form className="ticket-detail__request-changes" onSubmit={review.handleRequestChanges}>
        <label>
          <span>What needs to change</span>
          <textarea
            rows={3}
            required
            value={review.changesComment}
            onChange={(e) => review.setChangesComment(e.target.value)}
          />
        </label>
        {review.requestChanges.error && (
          <p role="alert" className="ticket-detail__notice">
            {review.requestChanges.error}
          </p>
        )}
        <button type="submit" disabled={review.requestChanges.busy}>
          {review.requestChanges.busy
            ? "Sending…"
            : attempt === undefined
              ? "Request changes"
              : `Request changes on attempt ${attempt}`}
        </button>
      </form>
      </div>
  );
}

interface ReviewPanelProps {
  ticket: Ticket;
  review: ReviewActions;
  // The ticket has several attempts, each reviewed on its own page: the
  // banner there is the attempt's, and an approval here would have to guess.
  attemptsSplit?: boolean;
}

// ReviewPanel is the review banner while a ticket waits on a human's
// approval, the outcome once it has one, and the post-merge review of a
// done ticket nobody approved.
export default function ReviewPanel({ ticket, review, attemptsSplit }: ReviewPanelProps) {
  return (
    <>
      {ticket.status === "review" && !attemptsSplit && (
        <ReviewBanner prUrl={ticket.pr_url} resumeAgentId={ticket.resume_agent_id} review={review} />
      )}

      {ticket.status !== "review" &&
        (ticket.approved_at ? (
          <p className="ticket-detail__review-outcome">
            Approved <Timestamp at={ticket.approved_at} />
            {ticket.status !== "done" && ticket.status !== "cancelled" && " -- the agent will merge it"}.
          </p>
        ) : (
          ticket.changes_requested_at && (
            <p className="ticket-detail__review-outcome">
              Changes requested <Timestamp at={ticket.changes_requested_at} />.
            </p>
          )
        ))}

      {ticket.post_reviewed_at && (ticket.post_reviewed_by_user_id || ticket.post_reviewed_by_user_id_deleted) && (
        <p className="ticket-detail__review-outcome">
          Reviewed after merge by{" "}
          <MemberName id={ticket.post_reviewed_by_user_id} deleted={ticket.post_reviewed_by_user_id_deleted} />{" "}
          <Timestamp at={ticket.post_reviewed_at} />.
        </p>
      )}

      {awaitingPostReview(ticket) && (
        <form className="ticket-detail__post-review" onSubmit={review.handleMarkReviewed}>
          <p role="status">
            <strong>Merged without review.</strong> Look over its change
            {/* The PR link is already in the header above. */}
            {ticket.pr_url ? " (the PR above)" : " -- no PR was reported"} and mark it reviewed. File a
            follow-up ticket below for anything that needs fixing.
          </p>
          <label>
            <span>Anything you found (optional)</span>
            <textarea
              rows={3}
              value={review.postReviewComment}
              onChange={(e) => review.setPostReviewComment(e.target.value)}
            />
          </label>
          {review.postReview.error && (
            <p role="alert" className="ticket-detail__notice">
              {review.postReview.error}
            </p>
          )}
          <button type="submit" disabled={review.postReview.busy}>
            {review.postReview.busy ? "Marking…" : "Mark reviewed"}
          </button>
        </form>
      )}

      {ticket.status === "done" && (
        <section className="ticket-detail__follow-up">
          <NewTicketForm
            heading="File a follow-up ticket"
            defaults={followUpDefaults(ticket)}
            onCreated={review.addFollowUp}
          />
          {review.followUps.length > 0 && (
            <p role="status" className="ticket-detail__follow-up-filed">
              Filed{" "}
              {review.followUps.map((id, i) => (
                <span key={id}>
                  {i > 0 && ", "}
                  <TicketLink ticketId={id} from="board">
                    #{id}
                  </TicketLink>
                </span>
              ))}
              .
            </p>
          )}
        </section>
      )}
    </>
  );
}
