import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { Button } from "@slop-factory/ui";
import { isCookieCategory } from "./consent_store";
import {
  useConsentActions,
  useConsentSnapshot,
  useDocumentHref,
  usePurposeCopy,
} from "./consent_context";
import { EQUAL_ACTION_PROPS } from "./equal_actions";
import GpcNotice from "./gpc_notice";
import { focusPreferencesTrigger, focusableIn } from "./focus";
import type { PurposeState } from "./types";
import "./preferences_dialog.scss";

// One switch per cookie purpose, with its description; the necessary ones
// shown on and fixed. Reached from the banner's Manage button, a "Cookie
// preferences" link in the footer and the settings page, so withdrawing is
// as easy as giving.
export default function PreferencesDialog() {
  const { preferencesOpen } = useConsentActions();
  return preferencesOpen ? <DialogBody /> : null;
}

function DialogBody() {
  const snapshot = useConsentSnapshot();
  const actions = useConsentActions();
  const copyOf = usePurposeCopy();
  const href = useDocumentHref();
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const purposes = snapshot.purposes.filter(isCookieCategory);
  // Only what the person has toggled. Everything else shows what the server
  // says now, so a decision made in another tab while this is open isn't
  // overwritten by a stale copy of the state.
  const [edits, setEdits] = useState<Record<string, boolean>>({});
  // An undecided purpose is off, so nothing is ticked that wasn't chosen.
  const valueOf = (p: PurposeState) => !p.gpcLocked && (edits[p.id] ?? p.granted);

  // Focus moves in on open and back to wherever it was on close.
  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialogRef.current?.focus();
    return () => {
      // The banner's Manage button is gone once the dialog's decision
      // completes the banner's job; the footer entry is the next best place.
      if (opener?.isConnected) opener.focus();
      else focusPreferencesTrigger();
    };
  }, []);

  const close = () => actions.closePreferences();

  const save = async () => {
    const decisions = purposes
      .filter((p) => !p.necessary)
      .map((p) => ({ purpose: p.id, granted: valueOf(p), p }))
      .filter(({ granted, p }) => p.needsDecision || p.granted !== granted)
      .map(({ purpose, granted }) => ({ purpose, granted }));
    if (decisions.length === 0 || (await actions.decide(actions.preferencesSource, decisions))) close();
  };

  const decideAll = async (grant: boolean) => {
    const ok = await (grant ? actions.acceptAll(actions.preferencesSource) : actions.rejectAll(actions.preferencesSource));
    if (ok) close();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      close();
      return;
    }
    if (e.key !== "Tab" || !dialogRef.current) return;
    // Modal: Tab stays inside until the dialog is closed.
    const items = focusableIn(dialogRef.current);
    const first = items[0];
    const last = items[items.length - 1];
    if (!first || !last) return;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === dialogRef.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <div className="consent-dialog__backdrop" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className="consent-dialog"
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId} className="consent-dialog__title">
          Cookie preferences
        </h2>
        <p className="consent-dialog__intro">
          Choose which optional cookies and similar storage we may use. You can change this at any time.{" "}
          {href && <a href={href("cookie_policy")}>Read the cookie policy</a>}
        </p>
        <GpcNotice />
        {snapshot.error && (
          <p role="alert" className="consent-dialog__error">
            Your choice couldn't be saved. Please try again.
          </p>
        )}
        <ul className="consent-dialog__list">
          {purposes.map((p) => (
            <PurposeRow
              key={p.id}
              purpose={p}
              name={copyOf(p).name}
              description={copyOf(p).description}
              checked={p.necessary || valueOf(p)}
              onChange={(v) => setEdits((e) => ({ ...e, [p.id]: v }))}
            />
          ))}
        </ul>
        <div className="consent-dialog__actions">
          <Button {...EQUAL_ACTION_PROPS} disabled={snapshot.saving} onClick={() => void decideAll(false)}>
            Reject all
          </Button>
          <Button {...EQUAL_ACTION_PROPS} disabled={snapshot.saving} onClick={() => void decideAll(true)}>
            Accept all
          </Button>
          <Button variant="primary" disabled={snapshot.saving} onClick={() => void save()}>
            Save preferences
          </Button>
          <Button variant="ghost" onClick={close}>
            Close
          </Button>
        </div>
      </div>
    </div>
  );
}

interface PurposeRowProps {
  purpose: PurposeState;
  name: string;
  description: string;
  checked: boolean;
  onChange: (value: boolean) => void;
}

function PurposeRow({ purpose, name, description, checked, onChange }: PurposeRowProps) {
  const id = useId();
  const locked = purpose.necessary || purpose.gpcLocked;
  return (
    <li className="consent-dialog__row">
      <div className="consent-dialog__row-text">
        <label htmlFor={id} className="consent-dialog__row-name">
          {name}
        </label>
        <p id={`${id}-desc`} className="consent-dialog__row-description">
          {description}
          {purpose.necessary && <strong> Always on.</strong>}
          {purpose.gpcLocked && <strong> Off: your browser's Global Privacy Control signal is honoured.</strong>}
        </p>
      </div>
      <input
        id={id}
        type="checkbox"
        role="switch"
        className="consent-dialog__switch"
        checked={checked}
        disabled={locked}
        aria-describedby={`${id}-desc`}
        onChange={(e) => onChange(e.target.checked)}
      />
    </li>
  );
}
