import { useEffect, useRef, useState } from "react";
import { Button } from "@slop-factory/ui";
import "./session_switch_dialog.scss";

interface SessionSwitchDialogProps {
  // The session that is mid-reply and would be stopped, when there is one:
  // then this is the confirmation, with the explanation inside it.
  replying: string | null;
  explain: boolean;
  onAnswer: (proceed: boolean, doNotShowAgain: boolean) => void;
}

// SessionSwitchDialog is the one question a switch can ask. It is always a
// confirmation when a reply would be lost, and has no "don't ask again":
// that is the guard against losing work. The explanation of what a switch
// carries over can be dismissed for good, and rides inside the confirmation
// when both apply, so a switch never asks twice. Everything is plain text.
export default function SessionSwitchDialog({ replying, explain, onAnswer }: SessionSwitchDialogProps) {
  const [dismiss, setDismiss] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  // The message box is stuck to the bottom of the page and would cover the
  // buttons of a dialog that opens just above it on a short screen.
  useEffect(() => {
    root.current?.scrollIntoView?.({ block: "center" });
  }, []);
  const confirming = replying !== null;
  const title = confirming ? `Session "${replying}" is replying. Stop it and continue here?` : "Switching sessions";

  return (
    <div ref={root} className="session-switch" role="alertdialog" aria-label={confirming ? "Confirm switch" : "Switching sessions"}>
      <p className="session-switch__title">{title}</p>
      {explain && (
        <dl className="session-switch__explanation">
          <dt>Carried over</dt>
          <dd>
            The working copy (files, the branch it is on, uncommitted changes, stashes) and the machine&apos;s environment, plus the remote
            control&apos;s own login and settings (its Claude Code config directory, or its pi agent directory). Nothing is moved, stashed or reset when you switch.
          </dd>
          <dt>Not carried over</dt>
          <dd>
            The conversation. Each session has its own history, and the model in this session does not know what another one did unless it
            looks (for example with git status).
          </dd>
          <dt>What happens</dt>
          <dd>
            The session that was running stops, and this one resumes from its saved history. The first reply after a switch can be slower
            and cost more, because the model&apos;s cache has likely expired.
          </dd>
        </dl>
      )}
      {explain && (
        <label className="session-switch__dismiss">
          <input type="checkbox" checked={dismiss} onChange={(e) => setDismiss(e.target.checked)} />
          Do not show again
        </label>
      )}
      <div className="session-switch__actions">
        <Button variant={confirming ? "danger" : "primary"} onClick={() => onAnswer(true, dismiss)}>
          {confirming ? "Stop it and continue" : "Continue"}
        </Button>
        <Button onClick={() => onAnswer(false, dismiss)}>{confirming ? "Keep it" : "Cancel"}</Button>
      </div>
    </div>
  );
}
