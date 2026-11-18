import { useState } from "react";
import { useNavigate } from "@slop-factory/router";
import { Button } from "@slop-factory/ui";
import { apiClient, type Conversation } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { CONTEXT_HINT_TOKENS } from "@/lib/context_level";
import { findLiveSession } from "@/lib/live_session";
import { useMachines } from "@/lib/use_machines";

// The prompt a ticket's "discuss" link puts in a Remote Control message box.
function discussPrompt(ticketId: number): string {
  return `discuss slop-haus #${ticketId}`;
}

// DiscussShortcut is a button (or, once opened on a large context, a few) for
// a flex row the caller provides. It opens the Remote Control tab with the
// prompt in a message box -- the connected session's, or a new session's when
// none is connected -- for the person to edit and send. Nothing is sent from
// here: the session is theirs, on a machine of theirs.
// When the connected session's context is already large, clicking it first
// offers to clear or compact it: a ticket discussion rarely needs the rest.
export default function DiscussShortcut({ ticketId }: { ticketId: number }) {
  const { state } = useMachines();
  const navigate = useNavigate();
  const action = useAsyncAction();
  const draft = discussPrompt(ticketId);

  const live = state.status === "loaded" ? findLiveSession(state.machines) : undefined;
  const session: Conversation | undefined = live
    ? state.status === "loaded"
      ? state.machines.find((m) => m.conductor.id === live.conductorId)?.sessions?.find((s) => s.id === live.sessionId)
      : undefined
    : undefined;
  const large = live !== undefined && session !== undefined && session.context_tokens > CONTEXT_HINT_TOKENS;

  const [choosing, setChoosing] = useState(false);

  function openDraft() {
    void navigate({ to: "/remote-control", search: { draft } });
  }

  function openSession() {
    if (!live) return;
    void navigate({ to: "/remote-control", search: { conductor: live.conductorId, session: live.sessionId, draft } });
  }

  // interrupt: the list does not say whether a reply is running, and the host
  // refuses (409) a clear or compact mid-reply otherwise.
  // The context is changed first and the session opened only once that has
  // succeeded, so the draft never lands in a session still holding the old one.
  function changeThenDiscuss(kind: "clear" | "compact") {
    if (!live) return;
    void action.run(async () => {
      if (kind === "clear") await apiClient.clearConversation(live.conductorId, live.sessionId, { interrupt: true });
      else await apiClient.compactConversation(live.conductorId, live.sessionId, { interrupt: true });
      openSession();
    }, kind === "clear" ? "Failed to clear the context." : "Failed to compact the context.");
  }

  if (!choosing) {
    return (
      // Disabled while the sessions load: a click before then could not know
      // whether to offer the clear/compact choice.
      <Button variant="tonal" disabled={state.status === "loading"} onClick={() => (large ? setChoosing(true) : openDraft())}>
        Discuss in Remote Control
      </Button>
    );
  }

  return (
    <>
      <span role="group" aria-label="Discuss in Remote Control">
        The connected session's context is large; clearing or compacting stops any reply in progress.
      </span>
      <Button disabled={action.busy} onClick={() => changeThenDiscuss("clear")}>
        Clear and discuss
      </Button>
      {session?.can_compact && (
        <Button disabled={action.busy} onClick={() => changeThenDiscuss("compact")}>
          Compact and discuss
        </Button>
      )}
      <Button disabled={action.busy} onClick={openSession}>
        Just discuss
      </Button>
      {action.error && <span role="alert">{action.error}</span>}
    </>
  );
}
