import { useId, useState } from "react";
import { Link } from "@slop-factory/router";
import { Button } from "@slop-factory/ui";
import type { SwitchConfirmation } from "./setting_labels";
import "./setting_switch_row.scss";
import { errorMessage } from "@/lib/use_async_action";

interface SettingSwitchRowProps {
  label: string;
  description?: string;
  checked: boolean;
  // Resolves once the new value is stored; a rejection is shown against the
  // row and the switch keeps the value it had.
  onWrite: (next: boolean) => Promise<void>;
  link?: { text: string; to: string };
  // Asked before the write; cancelling changes nothing.
  confirm?: { on: SwitchConfirmation; off: SwitchConfirmation };
  // Shown instead of letting the switch be used.
  lockedNote?: string;
  children?: React.ReactNode;
}

// The switch shows the stored value, not the one just clicked: it stays on
// what it had while the write is in flight and after a failed one, so what
// is on screen is never ahead of the server.
export default function SettingSwitchRow({
  label,
  description,
  checked,
  onWrite,
  link,
  confirm,
  lockedNote,
  children,
}: SettingSwitchRowProps) {
  const [asking, setAsking] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const descriptionId = useId();

  function click() {
    if (confirm) setAsking(true);
    else void toggle();
  }

  async function toggle() {
    if (saving) return;
    setAsking(false);
    setSaving(true);
    setError(null);
    try {
      await onWrite(!checked);
    } catch (err: unknown) {
      setError(errorMessage(err, `Failed to change ${label}.`));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="setting-switch-row">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        aria-describedby={description ? descriptionId : undefined}
        disabled={saving || asking || !!lockedNote}
        className={`setting-switch-row__switch setting-switch-row__switch--${checked ? "on" : "off"}`}
        onClick={click}
      >
        <span className="setting-switch-row__track" aria-hidden="true">
          <span className="setting-switch-row__thumb" />
        </span>
        <span className="setting-switch-row__state">{checked ? "On" : "Off"}</span>
      </button>
      <div className="setting-switch-row__body">
        <span className="setting-switch-row__label" aria-hidden="true">
          {label}
        </span>
        {description && (
          <p id={descriptionId} className="setting-switch-row__description">
            {renderDescription(description, link)}
          </p>
        )}
        {lockedNote && <p className="setting-switch-row__description">{lockedNote}</p>}
        {asking && confirm && (
          <div role="alertdialog" aria-label={`Confirm ${label}`} className="setting-switch-row__confirm">
            {(() => {
              const c = checked ? confirm.off : confirm.on;
              return (
                <>
                  <p>{c.lead}</p>
                  {c.consequences && (
                    <ul>
                      {c.consequences.map((line) => (
                        <li key={line}>{line}</li>
                      ))}
                    </ul>
                  )}
                  <Button variant={checked ? "danger" : "primary"} onClick={() => void toggle()}>
                    {c.action}
                  </Button>
                  <Button onClick={() => setAsking(false)}>Cancel</Button>
                </>
              );
            })()}
          </div>
        )}
        {children}
        {error && (
          <p role="alert" className="setting-switch-row__error">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

function renderDescription(description: string, link?: { text: string; to: string }) {
  const at = link ? description.indexOf(link.text) : -1;
  if (!link || at < 0) return description;
  return (
    <>
      {description.slice(0, at)}
      <Link to={link.to}>{link.text}</Link>
      {description.slice(at + link.text.length)}
    </>
  );
}
