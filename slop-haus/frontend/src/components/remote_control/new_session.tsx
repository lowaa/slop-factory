import { useEffect, useMemo, useRef, useState } from "react";
import { apiClient, type Conductor, type Conversation } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { newUuid } from "@/lib/new_uuid";
import { drafts, newSessionDraftKey } from "./drafts";
import MessageComposer from "./message_composer";
import MachinePicker from "./machine_picker";
import SessionSwitchError from "./session_switch_error";
import { useSessionSwitch } from "./use_session_switch";
import { useUsage } from "@/usage/use_usage";
import "./new_session.scss";

interface NewSessionProps {
  machines: Conductor[];
  conductorId: string;
  // What the message box starts with, from a shortcut such as a ticket's.
  // Nothing is sent until the person sends it.
  draft?: string;
  // The draft came from a shortcut, so the box takes focus when it opens.
  focusDraft?: boolean;
  // Called with the box's text as it changes.
  onDraftChange?: (draft: string) => void;
  onPick: (conductorId: string) => void;
  composerRows?: number;
  onStarted: (conductor: Conductor, conversation: Conversation) => void;
}

// NewSession is the page's right side for a session that does not exist yet:
// a message box, and Send starts it on the chosen machine. Starting one while
// another is live there replaces it, which asks first if it is mid-reply.
export default function NewSession({ machines, conductorId, draft = "", focusDraft, onDraftChange, onPick, onStarted, composerRows }: NewSessionProps) {
  const draftKey = newSessionDraftKey(conductorId);
  // A draft from the address (a shortcut) or carried from a session wins over
  // what was left in this box.
  const [message, setMessage] = useState(() => draft || drafts.get(draftKey));
  // What the box holds when a confirmation is answered, not when it opened.
  const messageNow = useRef(message);
  messageNow.current = message;
  useEffect(() => {
    onDraftChange?.(message);
  }, [message, onDraftChange]);
  useEffect(() => {
    drafts.set(draftKey, message);
  }, [draftKey, message]);
  const action = useAsyncAction();
  const usage = useUsage();
  const sessionSwitch = useSessionSwitch(conductorId);
  // One id for the start, kept until it succeeds: a start repeated after a
  // lost reply returns the conversation the first made rather than a second.
  const clientId = useMemo(() => newUuid(), []);
  const conductor = machines.find((m) => m.id === conductorId);

  function start() {
    if (!conductor) return;
    sessionSwitch.guard("new", begin);
  }

  function begin() {
    if (!conductor) return;
    const text = messageNow.current.trim();
    void action.run(async () => {
      const conversation = await apiClient.startConversation(conductor.id, {
        message: text === "" ? undefined : text,
        clientId,
      });
      usage.event("remote_control", { action: "start_session" });
      drafts.set(draftKey, "");
      onStarted(conductor, conversation);
    }, "Failed to start the session.");
  }

  return (
    <section className="new-session" aria-label="New session">
      <h1>New session</h1>
      {machines.length > 1 && <MachinePicker conductors={machines} value={conductorId} onChange={onPick} />}
      {conductor && machines.length === 1 && (
        <p className="new-session__machine">
          On <strong>{conductor.name}</strong>
        </p>
      )}
      {action.error ? (
        <p role="alert" className="new-session__notice">
          {action.error}
        </p>
      ) : (
        <SessionSwitchError sessionSwitch={sessionSwitch} className="new-session__notice" />
      )}
      {sessionSwitch.dialog}
      {/* No `attachments`: files are staged in a conversation, and this form
          has none until Start creates it. */}
      <MessageComposer
        value={message}
        onChange={setMessage}
        onSubmit={start}
        rows={composerRows}
        focusOnMount={focusDraft}
        sendDisabled={action.busy || sessionSwitch.busy || !conductor}
        placeholder="What should the session do? Leave it empty to just connect."
      />
    </section>
  );
}
