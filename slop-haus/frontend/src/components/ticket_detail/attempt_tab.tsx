import type { AgentContext, Attachment, Comment, Ticket, TicketAttempt } from "@/api";
import { Button } from "@slop-factory/ui";
import AgentName from "@/shared/names/agent_name";
import PrLink from "@/shared/pr_link/pr_link";
import AgentContextHistory from "./agent_context_history";
import BlindConversations from "./blind_conversations";
import Comments from "./comments";
import { attemptAccentClass } from "./attempt_accent";
import { ReviewBanner } from "./review_panel";
import { WorkerCost } from "@/components/worker_cost";
import RunSection from "./run_section";
import SignalsSection from "./signals_section";
import ReviewRunsSection from "./review_runs_section";
import AttemptVerdict from "./attempt_verdict/attempt_verdict";
import VariantSection from "./variant_section";
import ReplayFollowUp from "./replay_follow_up";
import {
  attemptsOf,
  awaitingChoice,
  commentsOfAttempt,
  closable,
  promotable,
  contextsOfAttempt,
  isClosed,
  notChosenBy,
} from "./attempt_view";
import { useCommentForm } from "./use_comment_form";
import { useCloseAttempt, type CloseAttemptActions } from "./use_close_attempt";
import { usePromoteAttempt, type PromoteAttemptActions } from "./use_promote_attempt";
import { useReviewActions } from "./use_review_actions";
import { useVariantPicker, type VariantOptions } from "./use_variant_picker";
import type { TicketStore } from "./use_ticket_store";
import "./attempt_tab.scss";

interface AttemptTabProps {
  store: TicketStore;
  ticket: Ticket;
  attempt: TicketAttempt;
  comments: Comment[];
  contexts: AgentContext[];
  variants: VariantOptions;
  onAttached?: (attachments: Attachment[]) => void;
}

function Summary({ attempt }: { attempt: TicketAttempt }) {
  return (
    <dl className="attempt-tab__summary">
      <div>
        <dt>Status</dt>
        <dd>
          {attempt.status.replace("_", " ")}
          {awaitingChoice(attempt) && (
            <span className="attempt-tab__badge">awaiting choice</span>
          )}
        </dd>
      </div>
      {attempt.shadow && (
        <div>
          <dt>Shadow</dt>
          <dd>
            Runs beside the promoted attempt; its work is not the ticket&apos;s
            result until promoted.
          </dd>
        </div>
      )}
      {attempt.started_revision !== null && (
        <div>
          <dt>Started from</dt>
          <dd>revision {attempt.started_revision}</dd>
        </div>
      )}
      {attempt.holder && (
        <div>
          <dt>Worked by</dt>
          <dd>
            <AgentName id={attempt.holder.agent_id} />
          </dd>
        </div>
      )}
      {attempt.worker_cost.length > 0 && (
        <div>
          <dt>Worker cost</dt>
          <dd>
            <WorkerCost
              figures={attempt.worker_cost}
              className="attempt-tab__cost"
            />
          </dd>
        </div>
      )}
      {attempt.pr_url && (
        <div>
          <dt>PR</dt>
          <dd>
            <PrLink url={attempt.pr_url} />
          </dd>
        </div>
      )}
    </dl>
  );
}

function CloseAttempt({ attempt, close }: { attempt: TicketAttempt; close: CloseAttemptActions }) {
  if (close.unavailable || !closable(attempt)) return null;
  const { submit } = close;
  const running = attempt.status === "in_progress";
  return (
    <div className="attempt-tab__close">
      {submit.error && (
        <p role="alert" className="ticket-detail__notice">
          {submit.error}
        </p>
      )}
      {close.confirming ? (
        <div role="group" aria-label={`Confirm closing attempt ${attempt.number}`}>
          <p>
            Close attempt {attempt.number}?{" "}
            {running
              ? "Its work will be stopped and its PR closed."
              : "It will not be started."}
          </p>
          <div className="attempt-tab__close-actions">
            <Button variant="primary" disabled={submit.busy} onClick={close.confirm}>
              Confirm: close attempt {attempt.number}
            </Button>
            <Button variant="ghost" disabled={submit.busy} onClick={close.cancel}>
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <Button disabled={submit.busy} onClick={close.start}>
          {submit.busy ? "Closing…" : `Close attempt ${attempt.number}`}
        </Button>
      )}
    </div>
  );
}

function NotChosen({ ticket, attempt }: { ticket: Ticket; attempt: TicketAttempt }) {
  const chosen = notChosenBy(ticket, attempt);
  if (!chosen) return null;
  return (
    <p className="attempt-tab__not-chosen" role="status">
      Not chosen: #{ticket.id} was done through attempt {chosen.number}. Review its PR if you want to, then{" "}
      <strong>Close attempt</strong> to clear it.
    </p>
  );
}

function PromoteAttempt({
  ticket,
  attempt,
  promote,
}: {
  ticket: Ticket;
  attempt: TicketAttempt;
  promote: PromoteAttemptActions;
}) {
  if (promote.unavailable || !promotable(attempt) || ticket.status === "done" || ticket.status === "cancelled") {
    return null;
  }
  const { submit } = promote;
  const current = attemptsOf(ticket).find((a) => a.promoted && a.number !== attempt.number);
  return (
    <div className="attempt-tab__promote">
      {submit.error && (
        <p role="alert" className="ticket-detail__notice">
          {submit.error}
        </p>
      )}
      {promote.confirming ? (
        <div role="group" aria-label={`Confirm promoting attempt ${attempt.number}`}>
          <p>Promote attempt {attempt.number}?</p>
          <ul>
            <li>Its PR is retargeted to main.</li>
            {awaitingChoice(attempt) && <li>It goes back to its agent to merge.</li>}
            <li>The other attempts&apos; PRs are marked [DO NOT MERGE] and stop holding scope.</li>
            {current && (
              <li>
                Attempt {current.number} is promoted now: it is demoted and its PR goes back to the base branch.
              </li>
            )}
          </ul>
          <div className="attempt-tab__close-actions">
            <Button variant="primary" disabled={submit.busy} onClick={promote.confirm}>
              Confirm: promote attempt {attempt.number}
            </Button>
            <Button variant="ghost" disabled={submit.busy} onClick={promote.cancel}>
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <Button disabled={submit.busy} onClick={promote.start}>
          {submit.busy ? "Promoting…" : `Promote attempt ${attempt.number}`}
        </Button>
      )}
    </div>
  );
}

// The page of one attempt of a ticket that has several: where it stands, the
// variant it is routed to, its own conversation and review. Everything that
// is sent from here names the attempt, so it reaches that attempt alone.
//
// The page is keyed by the attempt where it is used, so the drafts below
// (a comment, a request for changes) belong to one attempt: a half-typed
// answer must never be sent to the attempt on the next tab.
export default function AttemptTab({
  store,
  ticket,
  attempt,
  comments,
  contexts,
  variants,
  onAttached,
}: AttemptTabProps) {
  const commentForm = useCommentForm(store, ticket.id, attempt.number, onAttached);
  const review = useReviewActions(store, ticket.id, attempt.number);
  const close = useCloseAttempt(store, ticket.id, attempt.number);
  const promote = usePromoteAttempt(store, ticket.id, attempt.number);
  const variantPicker = useVariantPicker(store, ticket.id, attempt.number);
  const siblings = attemptsOf(ticket).filter(
    (a) => a.number !== attempt.number,
  );
  const handoffs = contextsOfAttempt(contexts, attempt.number);

  return (
    <div className={`attempt-tab ${attemptAccentClass(attempt.number)}${isClosed(attempt) ? " attempt-tab--closed" : ""}`}>
      <h2>Attempt {attempt.number}</h2>
      <Summary attempt={attempt} />
      {isClosed(attempt) && (
        <p className="attempt-tab__closed-note" role="status">
          This attempt was closed: its work was stopped and it is no longer a candidate.
        </p>
      )}
      <NotChosen ticket={ticket} attempt={attempt} />
      <PromoteAttempt ticket={ticket} attempt={attempt} promote={promote} />
      <CloseAttempt attempt={attempt} close={close} />
      <ReplayFollowUp ticket={ticket} attempt={attempt} />

      <VariantSection
        ticket={ticket}
        attempt={attempt}
        variants={variants}
        picker={variantPicker}
      />
      <RunSection attempt={attempt} />
      <SignalsSection attempt={attempt} />
      <AttemptVerdict store={store} ticketId={ticket.id} attempt={attempt} />
      <ReviewRunsSection ticketId={ticket.id} attempt={attempt} />

      {attempt.status === "discussion" && (
        <p className="ticket-detail__discussion-banner" role="status">
          <strong>Waiting on you.</strong> This attempt is paused for your
          answer in the comments below -- your next comment there is the answer,
          and it reaches this attempt alone.
        </p>
      )}
      {attempt.status === "review" && (
        <ReviewBanner
          prUrl={attempt.pr_url}
          resumeAgentId={attempt.holder?.agent_id ?? null}
          review={review}
          attempt={attempt.number}
        />
      )}

      {handoffs.length > 0 && (
        <section>
          <h2>Handoffs</h2>
          <AgentContextHistory contexts={handoffs} />
        </section>
      )}

      <Comments
        ticket={ticket}
        comments={commentsOfAttempt(comments, attempt.number)}
        form={commentForm}
        attempt={attempt.number}
        answering={attempt.status === "discussion"}
        audience={`Comments here reach attempt ${attempt.number} only.`}
      />

      <BlindConversations
        siblings={siblings}
        comments={comments}
        contexts={contexts}
      />
    </div>
  );
}
