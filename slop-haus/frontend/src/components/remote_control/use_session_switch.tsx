import { useState, type ReactNode } from "react";
import { apiClient, REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, type Conversation } from "@/api";
import { sessionName } from "./session_name";
import { isLiveState } from "@/domain/state_labels";
import { buildTranscript, isReplying } from "@/domain/transcript";
import { useAsyncAction } from "@/lib/use_async_action";
import { useBoolUserSetting } from "@/lib/use_user_setting";
import SessionSwitchDialog from "./session_switch_dialog";
import { CheckStepError, describeCheckFailure, runCheckStep } from "./session_check_failure";

// The longest a session's name is in the question.
const NAME_LENGTH = 40;

// How the send starts or reconnects a session:
//   resume: a message to the open session, which reconnects it if it has no
//     process by the time the server sees it
//   new: a session that does not exist yet
// Either takes the machine's one process from whichever session has it.
export type SwitchKind = "resume" | "new";

export interface SessionSwitch {
  // Runs go now, or after the person has answered what a switch to this
  // session needs them to: that it would stop a running reply, and (once)
  // what a switch carries over. Keep it, or Cancel, runs nothing.
  // go is called when the person has answered, so it must read what is in the
  // message box then, not when they pressed Send: the box stays editable.
  guard: (kind: SwitchKind, go: () => void) => void;
  // Reading the machine to find out; Send should wait.
  busy: boolean;
  error: string | null;
  // Run the failed check again, or put its message away.
  retry: () => void;
  dismiss: () => void;
  // Rendered by the caller where the question belongs, above its message box.
  dialog: ReactNode;
}

interface Pending {
  go: () => void;
  // Name of the session mid-reply that going on would stop, if there is one.
  replying: string | null;
  explain: boolean;
}

function questionName(session: Conversation): string {
  const text = sessionName(session).replace(/\s+/g, " ");
  return text.length > NAME_LENGTH ? `${text.slice(0, NAME_LENGTH)}…` : text;
}

// useSessionSwitch guards the sends that take the machine's one live session
// over. The page cannot know the live session is mid-reply from the session
// it has open (the one it reads is the one being sent to), so the guard reads
// the machine's sessions and the live one's transcript at the moment of the
// send, rather than trusting a list that is up to 20s old.
//
// currentSessionId is the open session, which is never "the other one".
export interface SessionSwitchOptions {
  // Where a failed check is reported (the client error reporting seam).
  onFailure?: (err: CheckStepError) => void;
}

export function useSessionSwitch(
  conductorId: string,
  currentSessionId?: string,
  { onFailure }: SessionSwitchOptions = {},
): SessionSwitch {
  const action = useAsyncAction();
  const [last, setLast] = useState<{ kind: SwitchKind; go: () => void } | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  // Not true until the saved value arrives: showing the explanation to
  // someone who dismissed it, in the instant before that, costs less than
  // withholding it from someone who has not.
  const { value: explained, change: setExplained } = useBoolUserSetting(REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED, false);

  function guard(kind: SwitchKind, go: () => void) {
    setLast({ kind, go });
    void action.run(async () => {
      const sessions = await runCheckStep("list", () => apiClient.listConversations(conductorId));
      // Read now rather than from what the page last showed: the open session
      // may have lost its process since, to another tab.
      const self = sessions.find((s) => s.id === currentSessionId);
      const switching = kind === "resume" ? !(self && isLiveState(self.state)) : sessions.length > 0;
      // A session with a process is stopped by any other's start or reconnect.
      const other = switching ? sessions.find((s) => s.id !== currentSessionId && isLiveState(s.state)) : undefined;
      let replying: string | null = null;
      if (other) {
        // Whether it is mid-reply is in its latest turn alone.
        const latest = await runCheckStep("read", () => apiClient.listRecentConversationEvents(conductorId, other.id, { before: 0, turns: 1 }));
        if (isReplying(buildTranscript(latest.events))) replying = questionName(other);
      }
      const explain = switching && !explained;
      if (replying === null && !explain) {
        go();
        return;
      }
      setPending({ go, replying, explain });
    }, (err) => {
      if (!(err instanceof CheckStepError)) return "Could not check whether another session is replying.";
      onFailure?.(err);
      return describeCheckFailure(err.step, err.cause);
    });
  }

  function answer(proceed: boolean, doNotShowAgain: boolean) {
    const current = pending;
    setPending(null);
    // Ticking the box is the person's answer whichever button they pressed.
    // A save that fails only means the explanation shows again.
    if (doNotShowAgain) {
      setExplained(true);
    }
    if (proceed && current) current.go();
  }

  return {
    guard,
    busy: action.busy || pending !== null,
    error: action.error,
    retry: () => {
      if (last) guard(last.kind, last.go);
    },
    dismiss: () => action.setError(null),
    dialog: pending && <SessionSwitchDialog replying={pending.replying} explain={pending.explain} onAnswer={answer} />,
  };
}
