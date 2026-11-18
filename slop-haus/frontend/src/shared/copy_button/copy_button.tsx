import { useEffect, useRef, useState } from "react";
import "./copy_button.scss";

// A button that puts a string on the clipboard and says so.
//
// It exists because the things this UI hands a human -- a setup command, a
// token shown exactly once -- are things they have to get into a terminal
// intact. Selecting a wrapped multi-line command with a mouse is how you
// end up pasting half of it.

type Outcome = "idle" | "copied" | "failed";

// copyBySelecting is the pre-Clipboard-API way, and it is not a
// belt-and-braces flourish: navigator.clipboard exists only in a secure
// context, which means https or localhost. slop-haus runs on a home
// cluster over plain http, where the modern API is simply absent and the
// button would otherwise never work anywhere except the developer's own
// machine. document.execCommand is deprecated and still the only thing
// that copies there.
function copyBySelecting(value: string): boolean {
  const area = document.createElement("textarea");
  area.value = value;
  area.setAttribute("readonly", "");
  // Off-screen rather than hidden: a display:none element cannot be
  // selected, and selection is the whole mechanism.
  area.style.position = "fixed";
  area.style.top = "-1000px";
  document.body.appendChild(area);

  const selection = document.getSelection();
  const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;

  let copied = false;
  try {
    area.select();
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  }

  area.remove();
  // Put back whatever the human had selected before pressing the button.
  if (selection && previous) {
    selection.removeAllRanges();
    selection.addRange(previous);
  }
  return copied;
}

export interface CopyButtonProps {
  value: string;
  // Label names what is being copied, for the accessible name: several of
  // these on one page are otherwise all called "Copy".
  label: string;
}

export default function CopyButton({ value, label }: CopyButtonProps) {
  const [outcome, setOutcome] = useState<Outcome>("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  function report(next: Outcome) {
    setOutcome(next);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setOutcome("idle"), 2000);
  }

  async function handleCopy() {
    try {
      // The modern API, where it exists. It can still reject -- a denied
      // permission, a page that isn't focused -- so this falls through
      // rather than treating a rejection as the end of it.
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
        report("copied");
        return;
      }
    } catch {
      // fall through to the older path
    }

    report(copyBySelecting(value) ? "copied" : "failed");
  }

  return (
    <button
      type="button"
      className={`copy-button copy-button--${outcome}`}
      onClick={handleCopy}
      aria-label={`Copy ${label}`}
    >
      {outcome === "copied" ? "Copied" : outcome === "failed" ? "Select and copy" : "Copy"}
    </button>
  );
}
