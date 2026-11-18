import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useLocation } from "@slop-factory/router";
import "./header_menu.scss";

// HeaderMenu is a header button and the panel it opens. On a phone it is the
// one menu, holding what the desktop header lays out in a row (the tabs, the
// tenant switcher, the account links); on a desktop it is the More tabs and
// the account links, which no longer fit in that row.
//
// Without a trigger it is the phone's icon button, named "Menu", and its
// panel spans the header. With one, the trigger is the button's visible text
// and accessible name, and the panel drops from the button, lined up with its
// start or end edge (align). active marks the button as holding the page on
// show, as a tab is.
//
// It closes on a navigation (or a link chosen in it), a press outside it,
// or Escape. The navigation
// case is tracked by which URL it was opened at rather than an effect
// resetting state: the menu is simply not open at any other URL.
//
// badge is shown on the button, for what the closed menu would hide.
interface HeaderMenuProps {
  children: ReactNode;
  badge?: ReactNode;
  trigger?: ReactNode;
  align?: "start" | "end";
  active?: boolean;
}

export default function HeaderMenu({ children, badge, trigger, align = "start", active = false }: HeaderMenuProps) {
  const panelId = useId();
  const href = useLocation({ select: (location) => location.href });
  const [openAt, setOpenAt] = useState<string | null>(null);
  const open = openAt === href;
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpenAt(null);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOpenAt(null);
      // Back to the button, so a keyboard user is not left focused on
      // something that just disappeared.
      buttonRef.current?.focus();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <div className={`header-menu${trigger === undefined ? "" : ` header-menu--dropdown header-menu--${align}`}`} ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className={`header-menu__button${active ? " header-menu__button--active" : ""}`}
        aria-label={trigger === undefined ? "Menu" : undefined}
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpenAt(open ? null : href)}
      >
        {trigger === undefined ? (
          <span className="header-menu__icon" aria-hidden="true" />
        ) : (
          <>
            {trigger}
            <span className="header-menu__caret" aria-hidden="true" />
          </>
        )}
        {badge}
      </button>
      {/* Hidden rather than unmounted, so aria-controls always names an
          element that exists. */}
      {/* A link to the page already on show changes no URL, so choosing
          one closes the menu here too. */}
      <div
        id={panelId}
        className="header-menu__panel"
        hidden={!open}
        onClick={(e) => {
          if ((e.target as Element).closest("a")) setOpenAt(null);
        }}
      >
        {children}
      </div>
    </div>
  );
}
