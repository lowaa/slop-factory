import { useEffect, useRef, useState } from "react";
import { useAsyncAction } from "@/lib/use_async_action";
import "./session_title.scss";

interface SessionTitleProps {
  name: string;
  // Saves the name; null clears the custom one. Throws the server's refusal.
  onSave: (title: string | null) => Promise<void>;
}

// SessionTitle is the session's name as the page's heading, with a Rename
// button beside it. Editing is in place: Enter or leaving the box saves, Escape
// cancels, and an empty name clears the custom one. A refusal keeps the box open
// with the server's message.
export default function SessionTitle({ name, onSave }: SessionTitleProps) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(name);
  const action = useAsyncAction();
  // A ref, not action.busy: a second Enter, or the blur a disabled box causes,
  // arrives before a re-render has shown the first save is under way.
  const inFlight = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const renameRef = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef(false);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
      inputRef.current?.select();
    }
    else if (returnFocus.current) renameRef.current?.focus();
    returnFocus.current = false;
  }, [editing]);

  function begin() {
    action.setError(null);
    setValue(name);
    setEditing(true);
  }

  function cancel() {
    if (inFlight.current) return;
    returnFocus.current = true;
    action.setError(null);
    setEditing(false);
  }

  function save() {
    if (inFlight.current) return;
    const next = value.trim();
    if (next === name) {
      setEditing(false);
      return;
    }
    inFlight.current = true;
    void action
      .run(async () => {
        await onSave(next === "" ? null : next);
        returnFocus.current = true;
        setEditing(false);
      }, "Failed to rename the session.")
      .finally(() => {
        inFlight.current = false;
        // The box was disabled, which dropped focus; a refusal leaves it open.
        inputRef.current?.focus();
      });
  }

  if (!editing) {
    return (
      <div className="session-title">
        <h1 className="session-title__name" title={name}>
          {name}
        </h1>
        <button ref={renameRef} type="button" className="session-title__rename" aria-label="Rename session" onClick={begin}>
          ✎
        </button>
      </div>
    );
  }

  return (
    <div className="session-title session-title--editing">
      <input
        ref={inputRef}
        className="session-title__input"
        aria-label="Session name"
        aria-invalid={action.error ? true : undefined}
        aria-describedby={action.error ? "session-title-error" : undefined}
        value={value}
        disabled={action.busy}
        placeholder="Name this session"
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            save();
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            cancel();
          }
        }}
        onBlur={save}
      />
      {action.error && (
        <p id="session-title-error" role="alert" className="session-title__error">
          {action.error}
        </p>
      )}
    </div>
  );
}
