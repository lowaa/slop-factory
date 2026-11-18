import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Conductor } from "@/api";
import RichText, { RichTextMode, RichTextToggle } from "@/shared/rich_text/rich_text";
import { useRenderMarkdown } from "@/lib/markdown_setting";
import { useAsyncAction } from "@/lib/use_async_action";
import { newUuid } from "@/lib/new_uuid";
import MessageComposer from "../message_composer";
import { useStagedAttachments } from "../use_staged_attachments";
import SessionBar from "../session_bar";
import SessionTitle from "../session_title";
import { sessionName } from "../session_name";
import SessionSwitchError from "../session_switch_error";
import { useSessionSwitch } from "../use_session_switch";
import { useProposals } from "@/components/proposals";
import ConversationProposals from "./conversation_proposals";
import ContextMeter from "./context_meter";
import RepoState from "./repo_state";
import { proposalsForConversation } from "./proposals_for";
import { isLiveState, sessionLabel } from "@/domain/state_labels";
import { buildTranscript, currentActivity, isReplying, isStopping, runningTurn, turnStartedAt, type TranscriptItem } from "@/domain/transcript";
import AssistantReply from "./assistant_reply";
import WorkingIndicator from "./working_indicator";
import { useConversation } from "./use_conversation";
import { useTranscriptScroll } from "./use_transcript_scroll";
import { useUsage } from "@/usage/use_usage";
import "./conversation_view.scss";

// An advanced-fallback host is restarted by rerunning run_remote_control.sh, which updates first; a supervisor-run
// one by the supervisor's restart.
const STOP_NEEDS_NEWER_HOST =
  "This Remote Control host is on an older version; restart it to enable Stop (rerun run_remote_control.sh, or restart the supervisor)";

interface ConversationViewProps {
  conductor: Conductor;
  conversationId: string;
  // Called once what the list of sessions shows of this one may have changed:
  // a message sent (which reconnects a session with no process, replacing the
  // live one) or a disconnect.
  onChanged?: () => void;
  // What the message box starts with, from a shortcut such as a ticket's.
  // Nothing is sent until the person sends it.
  initialDraft?: string;
  // The draft came from a shortcut, so the box takes focus when it opens.
  focusDraft?: boolean;
  // Called with the box's text as it changes.
  onDraftChange?: (draft: string) => void;
  // Given on a phone, where the chat is a screen of its own: a bar with a way
  // back to the list of sessions replaces the header, and the session's facts
  // and Disconnect move into the bar's menu.
  onBack?: () => void;
  // Called once the session is deleted, for the page to leave it.
  onDeleted?: () => void;
  composerRows?: number;
}

// ConversationView is the chat with one session on a remote control: the
// transcript as it streams in, which machine it runs on and whether its host
// is there, where its working copy stands, and what the remote control has
// proposed. The messages are markdown unless the person turned that off; tool output and
// repo state stay as they are.
//
// Disconnect is at the top right and the message box below the transcript,
// which is the part that scrolls, so neither scrolls away with a long
// transcript and the box never covers it.
export default function ConversationView({
  conductor,
  conversationId,
  onChanged,
  initialDraft = "",
  focusDraft,
  onDraftChange,
  onBack,
  onDeleted,
  composerRows,
}: ConversationViewProps) {
  const { state, addEvent, reloadDetail, loadOlder } = useConversation(conductor.id, conversationId);
  const { proposals } = useProposals();
  const action = useAsyncAction();
  const usage = useUsage();
  const markdown = useRenderMarkdown();
  const sessionSwitch = useSessionSwitch(conductor.id, conversationId);
  const [draft, setDraft] = useState(initialDraft);
  // Taken when the view opens: the box appears only once the session has
  // loaded, by which time the page has dropped the draft from the address.
  const [focusComposer] = useState(focusDraft);
  useEffect(() => {
    onDraftChange?.(draft);
  }, [draft, onDraftChange]);
  // What the box holds when a confirmation is answered, not when it opened.
  const draftNow = useRef(draft);
  draftNow.current = draft;
  const [confirming, setConfirming] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [confirmingContext, setConfirmingContext] = useState<"clear" | "compact" | null>(null);
  // The turn a Stop request is out for, until its event arrives: a second
  // click or Esc before then must not send another.
  const [stopAsked, setStopAsked] = useState<number | null>(null);
  // The id of the send in flight, kept until it is stored: sending the same
  // text again after a lost reply is then one message, not two.
  const sendId = useRef<{ key: string; id: string } | null>(null);
  const staged = useStagedAttachments(conversationId);

  const loaded = state.status === "loaded" ? state : null;
  const events = loaded ? loaded.events : null;
  const { listRef, onScroll, hasNewOutput, jumpToLatest } = useTranscriptScroll(events, !!loaded && loaded.older.loading);

  // The top of the transcript asks for older turns when it comes into view.
  // Watched afresh after each read, so a read that leaves the top still in
  // view (a tall screen) goes on to the next, until the screen is full.
  const startRef = useRef<HTMLLIElement>(null);
  // Moves with each page, so the top is watched afresh even for a page that
  // came back before the spinner was ever drawn.
  const olderCursor = loaded?.older.nextBefore;
  const canLoadOlder = !!loaded && loaded.older.hasMore && !loaded.older.loading && !loaded.older.error;
  useEffect(() => {
    const target = startRef.current;
    if (!canLoadOlder || !target || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadOlder();
      },
      { root: listRef.current, rootMargin: "100px 0px 0px 0px" },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [canLoadOlder, olderCursor, loadOlder, listRef]);

  // The composer's history: the prompts the first read brought, then those
  // sent since. The same array until an event arrives.
  const sentPrompts = useMemo(
    () =>
      loaded
        ? [...loaded.prompts.recent, ...loaded.events.filter((e) => e.kind === "user" && e.seq > loaded.prompts.coveredSeq).map((e) => e.body)]
        : [],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [events],
  );

  // Until the session is read there is no menu, but the way back is there.
  const barOnly = (content: ReactNode) =>
    onBack ? (
      <section className="conversation conversation--screen" aria-label="Session">
        <SessionBar title={conductor.name} onBack={onBack} />
        {content}
      </section>
    ) : (
      content
    );
  if (state.status === "loading") return barOnly(<p>Loading conversation…</p>);
  if (state.status === "error") {
    return barOnly(
      <p role="alert" className="conversation__notice">
        {state.message}
      </p>,
    );
  }

  const { detail } = state;
  const name = sessionName(detail);
  const rename = {
    name,
    onSave: async (title: string | null) => {
      await apiClient.renameConversation(conductor.id, conversationId, title);
      await reloadDetail();
      onChanged?.();
    },
  };
  const live = isLiveState(detail.state);
  const items = buildTranscript(state.events);
  const replying = live && isReplying(items);
  // From the events, so another tab or a reload shows it too.
  const stopping = replying && isStopping(state.events);
  const turn = runningTurn(state.events);
  const mine = proposalsForConversation(proposals, conductor.name);
  // Turns read after opening sit above what the person opened to; the
  // browser skips their rendering until they are near the view. What they
  // opened to is rendered whole, so that going to the end lands on the end:
  // skipped items are laid out at a guessed height, and a jump over many of
  // them stops short by the guess's error.
  const deferred = (item: TranscriptItem) => (item.seq < state.openedFromSeq ? " conversation__deferred" : "");

  // A message to a session that has no process reconnects it, which takes the
  // machine's process from whichever session has it.
  // Files alone are a message too, once they have all uploaded.
  const sendable = staged.ready && (draft.trim() !== "" || staged.uploader.items.length > 0);

  function send() {
    if (!sendable) return;
    sessionSwitch.guard("resume", deliver);
  }

  function deliver() {
    const text = draftNow.current.trim();
    const attachmentIds = staged.ids();
    if (text === "" && attachmentIds.length === 0) return;
    const key = JSON.stringify([text, attachmentIds]);
    if (sendId.current?.key !== key) sendId.current = { key, id: newUuid() };
    const { id } = sendId.current;
    void action.run(async () => {
      const event = await apiClient.sendConversationMessage(conductor.id, conversationId, { text, clientId: id, ...(attachmentIds.length > 0 ? { attachmentIds } : {}) });
      usage.event("remote_control", { action: "send_message" });
      // Your own message is where you want to be, wherever you had scrolled to.
      jumpToLatest();
      addEvent(event);
      sendId.current = null;
      setDraft("");
      staged.sent(attachmentIds);
      // A session with no process is connecting again now.
      await reloadDetail();
      onChanged?.();
    }, "Failed to send the message.");
  }

  // Names the turn the person saw, so a request that lands after it ended
  // (the server refuses it) cannot stop the next one.
  function stop() {
    if (stopAsked === turn) return;
    setStopAsked(turn);
    void action.run(async () => {
      try {
        await apiClient.interruptConversation(conductor.id, conversationId, turn);
      } catch (err) {
        setStopAsked(null);
        throw err;
      }
    }, "Failed to stop the reply.");
  }

  function disconnect() {
    setConfirming(false);
    void action.run(async () => {
      await apiClient.disconnectConversation(conductor.id, conversationId);
      await reloadDetail();
      onChanged?.();
    }, "Failed to disconnect.");
  }

  // Only a turn in progress has anything to lose: an idle session is
  // disconnected at once.
  const askToDisconnect = () => (replying ? setConfirming(true) : disconnect());

  // A refusal (409: the host has not caught up yet) stays in the action's
  // error, and the confirmation closes so the person can try again.
  function deleteSession() {
    setConfirmingDelete(false);
    void action.run(async () => {
      await apiClient.deleteConversation(conductor.id, conversationId);
      onChanged?.();
      onDeleted?.();
    }, "Failed to delete the session.");
  }

  // A running reply is stopped first (interrupt), which the confirmation says.
  function changeContext(kind: "clear" | "compact") {
    setConfirmingContext(null);
    void action.run(async () => {
      const opts = { interrupt: replying };
      if (kind === "clear") await apiClient.clearConversation(conductor.id, conversationId, opts);
      else await apiClient.compactConversation(conductor.id, conversationId, opts);
      await reloadDetail();
      onChanged?.();
    }, kind === "clear" ? "Failed to clear the context." : "Failed to compact the context.");
  }

  const contextControl = confirmingContext ? (
    <div className="conversation__confirm" role="alertdialog" aria-label={`Confirm ${confirmingContext}`}>
      <span>
        {confirmingContext === "clear"
          ? "Clear the context? The session forgets the conversation so far."
          : "Compact the context? The session keeps a summary of the conversation so far."}
        {replying && " This will interrupt the reply in progress."}
      </span>
      <Button variant="danger" disabled={action.busy} onClick={() => changeContext(confirmingContext)}>
        {confirmingContext === "clear" ? "Clear" : "Compact"}
      </Button>
      <Button onClick={() => setConfirmingContext(null)}>Keep it</Button>
    </div>
  ) : (
    <>
      <Button disabled={action.busy} onClick={() => setConfirmingContext("clear")}>
        Clear
      </Button>
      {detail.can_compact && (
        <Button disabled={action.busy} onClick={() => setConfirmingContext("compact")}>
          Compact
        </Button>
      )}
    </>
  );

  const deleteControl = confirmingDelete ? (
    <div className="conversation__confirm" role="alertdialog" aria-label="Confirm delete">
      <span>
        Delete <strong>{name}</strong>? Its transcript is gone for good.
      </span>
      <Button variant="danger" disabled={action.busy} onClick={deleteSession}>
        Delete
      </Button>
      <Button onClick={() => setConfirmingDelete(false)}>Keep it</Button>
    </div>
  ) : (
    <Button disabled={action.busy} onClick={() => setConfirmingDelete(true)}>
      Delete
    </Button>
  );

  const disconnectControl = (
    <>
      {confirming ? (
        <div className="conversation__confirm" role="alertdialog" aria-label="Confirm disconnect">
          <span>Disconnect and stop the current reply? To stop just this reply, use Stop.</span>
          <Button variant="danger" disabled={action.busy} onClick={disconnect}>
            Disconnect
          </Button>
          <Button onClick={() => setConfirming(false)}>Keep it</Button>
        </div>
      ) : (
        <Button disabled={action.busy} onClick={askToDisconnect}>
          Disconnect
        </Button>
      )}
    </>
  );
  const sessionActions = (
    <div className="conversation__disconnect">
      {live && contextControl}
      {live && disconnectControl}
      {deleteControl}
    </div>
  );
  const facts = (
    <>
      <dl className="conversation__facts">
        <div>
          <dt>Machine</dt>
          <dd>{conductor.name}</dd>
        </div>
        <div>
          <dt>Host</dt>
          <dd className={detail.host_online ? "conversation__online" : "conversation__offline"}>
            {detail.host_online ? "Online" : "Offline"}
          </dd>
        </div>
        <div>
          <dt>State</dt>
          <dd>{sessionLabel(detail.state, detail.host_online)}</dd>
        </div>
      </dl>
      <RepoState detail={detail} />
    </>
  );
  const notices = (
    <>
      {!detail.host_online && (
        <p className="conversation__notice" role="status">
          The host is not running, so nothing will answer until it is started again.
        </p>
      )}
      {!live && (
        <p className="conversation__notice" role="status">
          This session has no connected harness process. Your next message reconnects it and carries on where it left off. Note that you
          inherit the harness&apos;s previous working directory state.
        </p>
      )}
      {detail.state === "failed" && detail.failure && (
        <p className="conversation__notice" role="alert">
          Could not start: {detail.failure}
        </p>
      )}
      {detail.truncated && (
        <p className="conversation__notice" role="status">
          This session is at its size limit: later replies were not kept.
        </p>
      )}
    </>
  );

  return (
    <RichTextMode render={markdown.value}>
    <section className={`conversation${onBack ? " conversation--screen" : ""}`} aria-label="Session">
      {onBack ? (
        <>
          <SessionBar title={name} rename={rename} onBack={onBack} tools={<><ContextMeter tokens={detail.context_tokens} /><RichTextToggle markdown={markdown} /></>}>
            {facts}
            {sessionActions}
          </SessionBar>
          <div className="conversation__chrome conversation__notices">{notices}</div>
        </>
      ) : (
        <>
          {sessionActions}
          <header className="conversation__header conversation__chrome">
            <SessionTitle name={rename.name} onSave={rename.onSave} />
            <ContextMeter tokens={detail.context_tokens} />
            <RichTextToggle markdown={markdown} />
            {facts}
            {notices}
          </header>
        </>
      )}

      {/* Focusable so a keyboard can scroll it: it is the one region that scrolls. */}
      <div className="conversation__area">
        <ol ref={listRef} onScroll={onScroll} className="conversation__transcript" aria-label="Transcript" aria-live="polite" tabIndex={0}>
          <li ref={startRef} className="conversation__start">
            {state.older.loading && (
              <span className="conversation__replying" role="status">
                <span className="conversation__spinner" aria-hidden="true" />
                Loading earlier messages…
              </span>
            )}
            {state.older.error && (
              <span role="alert">
                {state.older.error} <Button onClick={loadOlder}>Retry</Button>
              </span>
            )}
            {!state.older.hasMore && items.length > 0 && "Start of session"}
          </li>
          {items.length === 0 && <li className="conversation__empty">Nothing said yet.</li>}
        {items.map((item) =>
          item.type === "note" ? (
            <li key={item.seq} className={`conversation__note conversation__note--${item.kind}${deferred(item)}`}>
              {item.text}
            </li>
          ) : (
            <li key={item.seq} className={`conversation__message conversation__message--${item.type}${deferred(item)}`}>
              <span className="conversation__who">{item.type === "user" ? "You" : conductor.name}</span>
              {item.type === "user" ? <RichText className="conversation__text" text={item.text} /> : <AssistantReply parts={item.parts} done={item.done} />}
            </li>
          ),
        )}
        {replying && (
<WorkingIndicator since={turnStartedAt(items)} activity={currentActivity(items)} stopping={stopping} />
        )}
        </ol>
        {hasNewOutput && (
          <Button className="conversation__jump" onClick={jumpToLatest}>
            Jump to latest
          </Button>
        )}
      </div>

      <ConversationProposals proposals={mine} />

      {action.error ? (
        <p role="alert" className="conversation__notice">
          {action.error}
        </p>
      ) : (
        <SessionSwitchError sessionSwitch={sessionSwitch} className="conversation__notice" />
      )}

      {sessionSwitch.dialog}
      <MessageComposer
        value={draft}
        onChange={setDraft}
        onSubmit={send}
        rows={composerRows}
        focusOnMount={focusComposer}
        sentPrompts={sentPrompts}
        sendDisabled={action.busy || sessionSwitch.busy || !sendable}
        attachments={staged.uploader}
        stop={
          replying
            ? { onStop: stop, stopping: stopping || stopAsked === turn, unavailable: !detail.host_online
              ? "The host isn't running"
              : detail.host_capabilities?.includes("interrupt")
                ? undefined
                : STOP_NEEDS_NEWER_HOST }
            : undefined
        }
      />
    </section>
    </RichTextMode>
  );
}
