import {
  cloneElement,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactElement,
} from "react";
import { viewportShift } from "./viewport_shift";
import "./tooltip.scss";

// Tooltip wraps a single trigger element (a button, usually) and shows a
// short explanation on hover or keyboard focus.
//
// The tooltip bubble is only mounted while shown, rather than always
// present and hidden by CSS -- the same choice dropdown.tsx makes for its
// panel/aria-controls. That keeps the trigger's aria-describedby pointing
// at a real element whenever it's set, and absent (not dangling) the rest
// of the time.

// Hover waits this long before showing, so a pointer passing over a row of
// buttons doesn't flash a bubble on each. Focus never waits.
export const TOOLTIP_HOVER_DELAY_MS = 1500;

type TriggerProps = {
  "aria-describedby"?: string;
};

export interface TooltipProps {
  text: string;
  // Overrides TOOLTIP_HOVER_DELAY_MS for this tooltip; 0 shows on hover at once.
  hoverDelayMs?: number;
  children: ReactElement<TriggerProps>;
}

// Kept clear of the screen edge so the bubble never sits flush against it.
const VIEWPORT_MARGIN_PX = 8;

export default function Tooltip({ text, hoverDelayMs = TOOLTIP_HOVER_DELAY_MS, children }: TooltipProps) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const bubbleRef = useRef<HTMLSpanElement>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const cancelHover = () => {
    clearTimeout(hoverTimer.current);
    hoverTimer.current = undefined;
  };
  useEffect(() => cancelHover, []);

  const show = () => {
    cancelHover();
    setOpen(true);
  };
  const hide = () => {
    cancelHover();
    setOpen(false);
  };
  const showAfterDelay = () => {
    if (hoverDelayMs <= 0) return show();
    cancelHover();
    hoverTimer.current = setTimeout(show, hoverDelayMs);
  };

  // The bubble is centred over its trigger, so a trigger near the screen
  // edge (the new-ticket form's left-aligned Preparing button) pushes half
  // of it off-screen. Measured after layout and before paint, so the
  // human never sees it jump. Written straight to the element rather than
  // through state, which would cost a second render for a value only CSS
  // reads. Cleared first so a re-measure (new text, a resize while open)
  // starts from the centred position, not from the previous shift.
  useLayoutEffect(() => {
    const bubble = bubbleRef.current;
    if (!open || !bubble) return;
    const place = () => {
      bubble.style.removeProperty("--tooltip-shift");
      const rect = bubble.getBoundingClientRect();
      const shift = viewportShift(rect.left, rect.right, document.documentElement.clientWidth, VIEWPORT_MARGIN_PX);
      bubble.style.setProperty("--tooltip-shift", `${shift}px`);
    };
    place();
    window.addEventListener("resize", place);
    return () => window.removeEventListener("resize", place);
  }, [open, text]);

  const trigger = cloneElement(children, { "aria-describedby": open ? id : undefined });

  return (
    // The handlers sit on the wrapper, not the trigger: a disabled button
    // fires no mouse events of its own, and focus and keydown bubble up to
    // here. Escape dismisses without moving focus off the trigger --
    // closing on blur alone would need the human to tab away first, which
    // is not how Escape behaves anywhere else in the app (see dropdown.tsx).
    <span
      className="tooltip"
      onMouseEnter={showAfterDelay}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
      onKeyDown={(e) => {
        if (e.key === "Escape") hide();
      }}
    >
      {trigger}
      {open && (
        <span role="tooltip" id={id} className="tooltip__bubble" ref={bubbleRef}>
          {text}
        </span>
      )}
    </span>
  );
}
