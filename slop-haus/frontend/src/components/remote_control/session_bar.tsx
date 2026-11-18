import { useId, type ReactNode } from "react";
import SessionTitle from "./session_title";
import { useDismissMenu } from "./use_dismiss_menu";
import "./session_bar.scss";

interface SessionBarProps {
  // The machine the session runs on.
  title: string;
  onBack: () => void;
  // Given, the title is the session's name with a control to rename it.
  rename?: { name: string; onSave: (title: string | null) => Promise<void> };
  // What the ⋯ menu holds. Without any there is no menu button.
  children?: ReactNode;
  // Always on the line, beside the menu button.
  tools?: ReactNode;
}

// SessionBar is the one line over a chat on a phone: back to the list of
// sessions, which session this is, and a menu for what would not fit on the
// line (the session's facts, Disconnect).
//
// The menu closes on a press outside it or Escape. Its content is only
// mounted while it is open, so a closed menu costs the chat nothing.
export default function SessionBar({ title, onBack, rename, children, tools }: SessionBarProps) {
  const panelId = useId();
  const { open, setOpen, rootRef, buttonRef } = useDismissMenu();

  return (
    <div className="session-bar" ref={rootRef}>
      <button type="button" className="session-bar__back" onClick={onBack}>
        ‹ Sessions
      </button>
      {rename ? <SessionTitle name={rename.name} onSave={rename.onSave} /> : <h1 className="session-bar__title">{title}</h1>}
      {tools}
      {children && (
        <>
          <button
            ref={buttonRef}
            type="button"
            className="session-bar__menu-button"
            aria-label="Session menu"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={() => setOpen(!open)}
          >
            ⋯
          </button>
          {open && (
            <div id={panelId} className="session-bar__panel">
              {children}
            </div>
          )}
        </>
      )}
    </div>
  );
}
