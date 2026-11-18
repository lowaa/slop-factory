import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { useEnterSends } from "./use_enter_sends";
import { PromptHistory, type PromptHistoryAction } from "./prompt_history";
import PromptHistoryList from "./prompt_history_list";
import { AttachmentField, type AttachmentUploader } from "@/components/attachments";
import "./message_composer.scss";

interface MessageComposerProps {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  // Why Send is off, beyond an empty box (a send in flight, no machine).
  sendDisabled?: boolean;
  placeholder?: string;
  // What the person has sent in this conversation, oldest first: Up from the
  // top line of the box offers the last few to pick from.
  sentPrompts?: readonly string[];
  // How tall the empty box is, in lines. Desktop gives it more than the phone.
  rows?: number;
  // Focus the box, cursor after its text, when it first appears: for a box
  // that has been given a prompt to edit and send. Read once, at mount.
  focusOnMount?: boolean;
  // Given while a reply is running: Stop appears with Send, and Esc in the
  // box does the same.
  stop?: StopControl;
  // Given where files can be staged (an open conversation): the box takes
  // pasted images and dropped files, and offers a picker, listing them above
  // the box. The new-session form leaves it out as it has no conversation yet.
  attachments?: AttachmentUploader;
}

export interface StopControl {
  onStop: () => void;
  // The owner has asked and the turn has not ended yet.
  stopping: boolean;
  // Why Stop cannot be used just now (the host is not running), if so.
  unavailable?: string;
}

// A dialog open anywhere on the page owns Esc.
const DIALOG = '[role="dialog"], [role="alertdialog"], [aria-modal="true"]';

// The tallest the box grows before it scrolls, in px. Mirrored by the
// max-height in message_composer.scss.
const MAX_HEIGHT = 200;

// Some browsers report the Enter that confirms an IME composition with
// isComposing already false but keyCode 229, so both are checked.
const IME_KEY_CODE = 229;

function Attachable({ attachments, children }: { attachments?: AttachmentUploader; children: ReactNode }) {
  if (!attachments) return <>{children}</>;
  return (
    <AttachmentField uploader={attachments} className="message-composer-attachments">
      {children}
    </AttachmentField>
  );
}

// MessageComposer is the message box of a Remote Control page, shared by the
// open session and the new-session form: the box takes the row and Send sits
// at its right, bottom aligned as the box grows, with the person's "Enter
// sends" choice under them. It is stuck to the bottom of the page, so it is
// in reach however long what is above it gets.
export default function MessageComposer({ value, onChange, onSubmit, sendDisabled, placeholder, sentPrompts, rows = 2, focusOnMount, stop, attachments }: MessageComposerProps) {
  const box = useRef<HTMLTextAreaElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const [history] = useState(() => new PromptHistory());
  const { prompts, open, highlighted } = useSyncExternalStore(history.subscribe, history.getSnapshot);
  const listId = useId();
  const stopHintId = `${listId}-stop`;
  const optionId = (i: number) => `${listId}-option-${i}`;
  // Where the cursor goes once a chosen prompt has been put in the box.
  const cursorToEnd = useRef(false);
  const [enterSends, setEnterSends] = useEnterSends();

  // Sized from its content rather than with CSS field-sizing, which not every
  // browser has.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`;
    if (cursorToEnd.current) {
      cursorToEnd.current = false;
      el.setSelectionRange(el.value.length, el.value.length);
    }
  }, [value]);

  useEffect(() => {
    const el = box.current;
    if (!focusOnMount || !el) return;
    // Someone already typing elsewhere keeps their place.
    const active = document.activeElement;
    if (active instanceof HTMLElement && active !== document.body && active !== el && active.matches("input, textarea, select, [contenteditable]")) return;
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    // Mount only: a later render, or the same session coming back, must not
    // take focus again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    history.setPrompts(sentPrompts ?? []);
  }, [history, sentPrompts]);

  useEffect(() => {
    if (!open) return;
    function outside(e: MouseEvent) {
      // The box and the list are the widget; a click in the box only moves
      // the cursor.
      const inside = [box.current, root.current?.querySelector('[role="listbox"]')];
      if (e.target instanceof Node && !inside.some((el) => el?.contains(e.target as Node))) history.cancel();
    }
    document.addEventListener("mousedown", outside);
    return () => document.removeEventListener("mousedown", outside);
  }, [open, history]);

  // Returns whether the history took the key.
  function apply(action: PromptHistoryAction): boolean {
    if (action.kind === "pass") return false;
    if (action.kind === "fill") {
      cursorToEnd.current = true;
      onChange(action.text);
      box.current?.focus();
    }
    return true;
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    onSubmit();
  }

  // With the preference on, a plain Enter sends and Shift+Enter is the
  // newline; off, Enter is only ever a newline. An Enter that confirms an
  // IME composition (Japanese, say) is neither: it belongs to the IME.
  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.nativeEvent.isComposing || e.keyCode === IME_KEY_CODE) return;
    if (!(e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) && historyKey(e)) return;
    // Only the box's own Esc: the history list has taken it above when open.
    // The draft is left as it is.
    if (e.key === "Escape" && stop && !stop.stopping && !stop.unavailable && !document.querySelector(DIALOG)) {
      e.preventDefault();
      stop.onStop();
      return;
    }
    if (e.key !== "Enter" || !enterSends) return;
    if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return;
    // A send that cannot happen (one in flight, no machine) leaves the key
    // to its default rather than swallowing it.
    if (sendDisabled) return;
    e.preventDefault();
    onSubmit();
  }

  // Whether the key was the history's, which then owns it: the Enter that
  // chooses a prompt must not also send it.
  function historyKey(e: KeyboardEvent<HTMLTextAreaElement>): boolean {
    let action: PromptHistoryAction;
    switch (e.key) {
      case "ArrowUp": {
        const el = e.currentTarget;
        const onTopLine = !el.value.slice(0, el.selectionStart).includes("\n");
        action = history.keyUp({ onTopLine, draft: el.value });
        break;
      }
      case "ArrowDown":
        action = history.keyDown();
        break;
      case "Enter":
        action = history.select();
        break;
      case "Escape":
        action = history.cancel();
        break;
      default:
        return false;
    }
    if (!apply(action)) return false;
    e.preventDefault();
    return true;
  }

  return (
    <div className="message-composer" ref={root}>
      {open && <PromptHistoryList id={listId} prompts={prompts} highlighted={highlighted} optionId={optionId} onChoose={(i) => apply(history.select(i))} />}
      <Attachable attachments={attachments}>
        <form className="message-composer__row" onSubmit={submit}>
          <textarea
            ref={box}
            aria-label="Message"
            value={value}
            rows={rows}
            placeholder={placeholder}
            aria-controls={open ? listId : undefined}
            aria-activedescendant={open ? optionId(highlighted) : undefined}
            onChange={(e) => {
              history.cancel();
              onChange(e.target.value);
            }}
            onKeyDown={onKeyDown}
            // So an on-screen keyboard labels its key for what Enter does.
            enterKeyHint={enterSends ? "send" : "enter"}
          />
          <div className="message-composer-actions">
            {stop && (
              <Button
                aria-label={stop.stopping ? undefined : "Stop this reply"}
                aria-describedby={stop.unavailable ? stopHintId : undefined}
                disabled={stop.stopping || !!stop.unavailable}
                onClick={stop.onStop}
              >
                {stop.stopping ? "Stopping…" : <><span className="message-composer-stop-icon" aria-hidden="true" />Stop</>}
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={sendDisabled}>
              Send
            </Button>
          </div>
        </form>
      </Attachable>
      {stop?.unavailable && (
        <p id={stopHintId} className="message-composer-stop-hint">
          {stop.unavailable}
        </p>
      )}
      <div className="message-composer__options">
        <label>
          <input type="checkbox" checked={enterSends} onChange={(e) => setEnterSends(e.target.checked)} />
          Enter sends
        </label>
        <span className="message-composer__hint">{enterSends ? "Shift+Enter adds a new line" : "Enter adds a new line; use Send"}</span>
      </div>
    </div>
  );
}
