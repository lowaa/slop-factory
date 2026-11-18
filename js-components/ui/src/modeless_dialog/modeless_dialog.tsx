import { useEffect, useId, useRef, type FocusEvent, type KeyboardEvent, type ReactNode } from "react";
import Button from "../button/button";
import "./modeless_dialog.scss";

export interface ModelessDialogProps {
  // Names the dialog (aria-labelledby) as well as heading it.
  title: ReactNode;
  // Level of the title's heading, to fit the page's outline it sits in.
  headingLevel?: 2 | 3 | 4;
  onClose: () => void;
  // Accessible name of the close button, which only shows an ×.
  closeLabel?: string;
  // A small swatch in the top-left corner, for tying the dialog to whatever
  // opened it (a mark of the same colour elsewhere on the page). Decorative:
  // it carries nothing a screen reader needs.
  pipColor?: string;
  // Each change moves focus into the dialog, including the first value it
  // mounts with. A counter rather than a boolean, so asking again for a
  // dialog that is already "the focused one" -- after focus has wandered
  // off it -- still moves focus back.
  focusRequest?: number;
  // Focus entered the dialog from outside it: a click inside, a Tab into it,
  // or a focusRequest. Not fired as focus moves between its own controls.
  onFocusEnter?: () => void;
  className?: string;
  children?: ReactNode;
}

// ModelessDialog is a dialog that leaves the rest of the page usable:
// nothing behind it is inert, focus is not trapped, and several can be open
// at once. Escape closes the one focus is in -- only it sees the key, so the
// others stay open.
export default function ModelessDialog({
  title,
  headingLevel = 2,
  onClose,
  closeLabel = "Close",
  pipColor,
  focusRequest,
  onFocusEnter,
  className,
  children,
}: ModelessDialogProps) {
  const ref = useRef<HTMLElement>(null);
  const titleId = useId();
  const Heading = `h${headingLevel}` as const;

  useEffect(() => {
    if (focusRequest !== undefined) ref.current?.focus();
  }, [focusRequest]);

  function handleKeyDown(e: KeyboardEvent<HTMLElement>) {
    if (e.key !== "Escape" || e.defaultPrevented) return;
    // Handled here, so nothing further up (another dialog, a page-level
    // shortcut) acts on the same press.
    e.preventDefault();
    e.stopPropagation();
    onClose();
  }

  function handleFocus(e: FocusEvent<HTMLElement>) {
    if (e.currentTarget.contains(e.relatedTarget)) return;
    onFocusEnter?.();
  }

  const classes = ["modeless-dialog", className].filter(Boolean).join(" ");
  return (
    <section
      ref={ref}
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      // Focusable itself, so a click on its plain text lands focus inside
      // it (and Escape then reaches it) without a control to click.
      tabIndex={-1}
      className={classes}
      onKeyDown={handleKeyDown}
      onFocus={handleFocus}
    >
      <header className="modeless-dialog__header">
        {pipColor && (
          <span
            className="modeless-dialog__pip"
            data-testid="modeless-dialog-pip"
            aria-hidden="true"
            style={{ backgroundColor: pipColor }}
          />
        )}
        <Heading id={titleId} className="modeless-dialog__title">
          {title}
        </Heading>
        <Button variant="ghost" size="sm" className="modeless-dialog__close" aria-label={closeLabel} onClick={onClose}>
          ×
        </Button>
      </header>
      <div className="modeless-dialog__body">{children}</div>
    </section>
  );
}
