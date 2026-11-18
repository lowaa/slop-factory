import { useId, type ReactNode } from "react";
import { useNow } from "@/lib/use_now";
import { useDocumentTitle } from "@/lib/use_document_title";
import { AgentName } from "@/components/agent_name";
import RichText, { RichTextMode } from "@/shared/rich_text/rich_text";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import { useRenderMarkdown } from "@/lib/markdown_setting";
import Timestamp from "@/shared/timestamp/timestamp";
import TicketHistory from "./ticket_history";
import AgentContextHistory from "./agent_context_history";
import RolesEditor from "./roles_editor";
import RunSection from "./run_section";
import SignalsSection from "./signals_section";
import ReviewRunsSection from "./review_runs_section";
import VariantSection from "./variant_section";
import ProposalsStrip from "./proposals_strip";
import ReviewPanel from "./review_panel";
import DiscussionAnswer from "./discussion_answer";
import { answerKey } from "./use_discussion_answer";
import CloseAsMergedPanel from "./close_as_merged";
import BodyEditor from "./body_editor";
import { ExternalContent } from "@/components/external_content";
import Blockers from "./blockers";
import TicketUsage from "./ticket_usage";
import Comments from "./comments";
import { AttachmentGallery, useTicketAttachments } from "@/components/attachments";
import AttemptTabs from "./attempt_tabs";
import AttemptTab from "./attempt_tab";
import TicketHeader from "./ticket_header";
import TicketActions from "./ticket_actions";
import { TicketFields, TicketPaths } from "./ticket_meta";
import AddAttemptForm from "./add_attempt_form";
import { useAddAttempts } from "./use_add_attempts";
import {
  attemptsOf,
  commentsOfAttempt,
  hasSeveralAttempts,
  selectedAttempt,
  type AttemptLink,
} from "./attempt_view";
import {
  useRolesEditor,
  useTenantRoles,
  type TenantRoles,
} from "./use_roles_editor";
import {
  useVariantOptions,
  useVariantPicker,
  type VariantOptions,
} from "./use_variant_picker";
import { usePathsEditor } from "./use_paths_editor";
import { useReviewActions } from "./use_review_actions";
import { useCloseAsMerged } from "./use_close_as_merged";
import { useBodyEditor } from "./use_body_editor";
import { useBlockers } from "./use_blockers";
import { useStatusHistory } from "./use_status_history";
import { useCommentForm } from "./use_comment_form";
import { useTicketStore } from "./use_ticket_store";
import {
  TICKET_VIEW_LABELS,
  TICKET_VIEWS,
  type TicketView,
} from "./ticket_view";
import "./ticket_detail.scss";

interface TicketDetailProps {
  ticketId: number;
  // The way back to where the ticket was opened from, drawn above it. The
  // caller's to make: it knows which tab that was, this only shows a ticket.
  back?: ReactNode;
  // A shortcut to talk the ticket over in Remote Control, drawn above its
  // body. The caller's to make, like back: it needs the router, which this
  // only shows a ticket.
  discuss?: ReactNode;
  // The tab on show. The caller's to keep, like back: the route keeps it
  // in the URL, so a reload, Back and a copied link all land on it.
  view: TicketView;
  onViewChange: (view: TicketView) => void;
  // The attempt on show, when the address names one. Only a ticket with
  // several attempts is split into tabs; for any other the address is shown
  // as the ticket itself.
  attempt?: number;
  // How an attempt's tab is linked to; the caller's, like back, for it needs
  // the router.
  attemptLink: AttemptLink;
}

// Everything on the page belongs to one ticket, so a change of id starts
// again rather than carrying anything over: the page is keyed by the id
// here, rather than trusting every caller to key it. A remount drops every
// draft, error and busy flag, and every write generation with them; and a
// reply for the old ticket that lands later can only set state on the
// unmounted page, which goes nowhere -- it cannot merge into, or report a
// failure on, the ticket now on show.
//
// The tenant's roles and the variant versions are what is held above the key:
// they are the same for every ticket, so a change of ticket has no reason to
// refetch them.
export default function TicketDetail(props: TicketDetailProps) {
  const tenantRoles = useTenantRoles();
  const variants = useVariantOptions();
  return (
    <UserSettingsProvider>
      <TicketDetailPage
        key={props.ticketId}
        {...props}
        tenantRoles={tenantRoles}
        variants={variants}
      />
    </UserSettingsProvider>
  );
}

function TicketDetailPage({
  ticketId,
  back,
  discuss,
  view,
  onViewChange,
  attempt,
  attemptLink,
  tenantRoles,
  variants,
}: TicketDetailProps & { tenantRoles: TenantRoles; variants: VariantOptions }) {
  const tabsId = useId();
  const markdown = useRenderMarkdown();
  // A clock for the first-claim note, ticking once a minute so a window
  // that runs out while the page is open drops its note on its own rather
  // than waiting for something else to re-render.
  const now = useNow(60_000);

  // Every section's state is held here, through the hook its file exports,
  // rather than in the section itself, so all of it has the one lifetime:
  // this page's, which is one ticket's. The order of the calls is the order
  // their requests go out in.
  const store = useTicketStore(ticketId);
  const blockers = useBlockers(store, ticketId);
  const statusChanges = useStatusHistory(ticketId, store.state.status === "loaded" ? store.state.ticket.status : null);
  const editor = useBodyEditor(store, ticketId);
  const rolesEditor = useRolesEditor(store, ticketId);
  const variantPicker = useVariantPicker(store, ticketId);
  const pathsEditor = usePathsEditor(store, ticketId);
  const review = useReviewActions(store, ticketId);
  const closeAsMerged = useCloseAsMerged(store, ticketId);
  const attachments = useTicketAttachments(ticketId);
  const commentForm = useCommentForm(store, ticketId, undefined, attachments.added);
  const addAttempts = useAddAttempts(store, ticketId);

  const { state } = store;
  // A ticket with several attempts keeps its own conversation, handoffs and
  // review per attempt: this page is then the ticket's own view, or one
  // attempt's.
  const several = state.status === "loaded" && hasSeveralAttempts(state.ticket);
  const shown = state.status === "loaded" ? selectedAttempt(state.ticket, attempt) : undefined;
  // The answer widget carries the Remote Control shortcut, so the loose one
  // below only shows where there is no widget.
  const answering =
    state.status === "loaded" &&
    (several ? shown?.status === "discussion" : !!state.ticket.attempts?.some((a) => a.status === "discussion"));
  const missingAttempt = several && attempt !== undefined && !shown;
  // The header is the ticket's, and on an attempt's page its fields would
  // read as the attempt's: say whose they are.

  useDocumentTitle(
    state.status !== "loaded"
      ? null
      : shown
        ? `#${state.ticket.id} · attempt ${shown.number} – slop-haus`
        : `#${state.ticket.id} ${state.ticket.title} – slop-haus`,
  );

  return (
    <RichTextMode render={markdown.value}>
    <div className="ticket-detail">
      {back}

      {state.status === "loading" && <p>Loading ticket…</p>}
      {state.status === "error" && (
        <p role="alert" className="ticket-detail__notice">
          {state.message}
        </p>
      )}

      {state.status === "loaded" && (
        <>
          <TicketHeader ticket={state.ticket} shown={shown} markdown={markdown} />

          {several && (
            <AttemptTabs attempts={attemptsOf(state.ticket)} selected={shown?.number} link={attemptLink} />
          )}

          {missingAttempt && (
            <p role="alert" className="ticket-detail__notice">
              This ticket has no attempt {attempt}.
            </p>
          )}

          <AttachmentGallery attachments={attachments} />

          {shown && several && shown.status === "discussion" && (
            <DiscussionAnswer key={answerKey(shown)} ticketId={state.ticket.id} attempt={shown} />
          )}

          {shown && (
            <AttemptTab
              // Keyed so a draft or an expansion made on one attempt is
              // never carried to another.
              key={shown.number}
              store={store}
              ticket={state.ticket}
              attempt={shown}
              comments={state.comments}
              contexts={state.contexts}
              variants={variants}
              onAttached={attachments.added}
            />
          )}

          {!shown && (
            <>
          {!several && <div
            className="ticket-detail__tabs"
            role="tablist"
            aria-label="Ticket views"
          >
            {TICKET_VIEWS.map((v) => (
              <button
                key={v}
                type="button"
                role="tab"
                id={`${tabsId}-${v}-tab`}
                aria-controls={`${tabsId}-${v}-panel`}
                aria-selected={v === view}
                className={v === view ? "is-selected" : ""}
                onClick={() => onViewChange(v)}
              >
                {TICKET_VIEW_LABELS[v]}
                {v === "agent-context" &&
                  state.contexts.length > 0 &&
                  ` (${state.contexts.length})`}
              </button>
            ))}
          </div>}

          {/* Both panels stay mounted, the other one hidden: a half-typed
              follow-up ticket or a handoff opened out of its fold is still
              there after a look at the other tab. */}
          {!several && (
          <div
            className="ticket-detail__panel"
            role="tabpanel"
            id={`${tabsId}-agent-context-panel`}
            aria-labelledby={`${tabsId}-agent-context-tab`}
            hidden={view !== "agent-context"}
          >
            <AgentContextHistory contexts={state.contexts} />
          </div>
          )}

          <div
            className="ticket-detail__panel"
            role="tabpanel"
            id={`${tabsId}-main-panel`}
            aria-labelledby={`${tabsId}-main-tab`}
            hidden={several ? false : view !== "main"}
          >
            <TicketFields store={store} ticket={state.ticket} />

            <RolesEditor
              ticket={state.ticket}
              tenantRoles={tenantRoles}
              editor={rolesEditor}
            />

            {/* Each attempt of several is routed on its own page. */}
            {!several && (
              <>
                <VariantSection
                  ticket={state.ticket}
                  variants={variants}
                  picker={variantPicker}
                />
                {state.ticket.attempts?.[0] && <RunSection attempt={state.ticket.attempts[0]} />}
                {state.ticket.attempts?.[0] && <SignalsSection attempt={state.ticket.attempts[0]} />}
                {state.ticket.attempts?.[0] && <ReviewRunsSection ticketId={state.ticket.id} attempt={state.ticket.attempts[0]} />}
              </>
            )}

            <TicketActions store={store} ticket={state.ticket} />

            {state.ticket.status === "discussion" && (
              <p className="ticket-detail__discussion-banner" role="status">
                <strong>Waiting on you.</strong>{" "}
                {several
                  ? "An attempt of this ticket is paused for your answer: answer it on its own tab, where the comment reaches that attempt alone."
                  : "This ticket is paused for your answer in the comments below -- your next comment there is the answer, and it puts the ticket straight back into the queue."}
                {state.ticket.resume_agent_id && (
                  <>
                    {" "}
                    First claim goes back to{" "}
                    <AgentName id={state.ticket.resume_agent_id} />.
                  </>
                )}
              </p>
            )}

            <ProposalsStrip ticketId={state.ticket.id} />

            <CloseAsMergedPanel ticket={state.ticket} action={closeAsMerged} />

            <ReviewPanel ticket={state.ticket} review={review} attemptsSplit={several} />

            {/* With several attempts each answers on its own tab. */}
            {!several && state.ticket.attempts
              ?.filter((a) => a.status === "discussion")
              .map((a) => <DiscussionAnswer key={answerKey(a)} ticketId={state.ticket.id} attempt={a} />)}

            {!answering && discuss && <div className="ticket-detail__discuss">{discuss}</div>}

            {state.ticket.status !== "discussion" &&
              state.ticket.status !== "review" &&
              state.ticket.resume_agent_id &&
              state.ticket.affinity_until &&
              // The server keeps the window's end until the ticket is next
              // claimed, so a passed one is still here -- and no longer true.
              new Date(state.ticket.affinity_until).getTime() > now && (
                <p className="ticket-detail__affinity-note">
                  <AgentName id={state.ticket.resume_agent_id} /> has first
                  claim on this ticket until{" "}
                  {new Date(state.ticket.affinity_until).toLocaleTimeString()}.
                </p>
              )}

            <BodyEditor ticket={state.ticket} editor={editor} />

            <ExternalContent ticket={state.ticket} />

            <TicketPaths ticket={state.ticket} editor={pathsEditor} />

            <Blockers ticket={state.ticket} blockers={blockers} />

            <TicketUsage ticketId={ticketId} />

            {!several && state.contexts.length > 0 && (
              <section className="ticket-detail__handoffs">
                <h2>Latest handoff</h2>
                {(() => {
                  // Oldest first off the wire; the last one is the current
                  // handoff -- what a fresh agent (itself, or another one)
                  // would pick up from right now. Reading back through the
                  // rest is what the agent context tab is for.
                  const latest = state.contexts[state.contexts.length - 1];
                  return (
                    <>
                      <div className="ticket-detail__handoff ticket-detail__handoff--latest">
                        <div className="ticket-detail__handoff-meta">
                          <AgentName id={latest.agent_id} />
                          <Timestamp at={latest.created_at} />
                        </div>
                        <RichText className="ticket-detail__handoff-body" text={latest.body} />
                      </div>
                      {state.contexts.length > 1 && (
                        <button
                          type="button"
                          className="ticket-detail__all-handoffs"
                          onClick={() => {
                            onViewChange("agent-context");
                            // This button is about to be hidden with its
                            // panel; the tab it switched to is where focus
                            // belongs, not back at the top of the page.
                            document
                              .getElementById(`${tabsId}-agent-context-tab`)
                              ?.focus();
                          }}
                        >
                          See all {state.contexts.length} handoffs
                        </button>
                      )}
                    </>
                  );
                })()}
              </section>
            )}

            <TicketHistory revisions={state.revisions} statusChanges={statusChanges} />

            <Comments
              ticket={state.ticket}
              comments={several ? commentsOfAttempt(state.comments, null) : state.comments}
              form={commentForm}
              attemptsSplit={several}
              audience={
                several
                  ? "The ticket's own conversation. A comment here reaches every live attempt; each attempt's own is on its tab."
                  : undefined
              }
            />

            <AddAttemptForm variants={variants} form={addAttempts} />
          </div>
            </>
          )}
        </>
      )}
    </div>
    </RichTextMode>
  );
}
