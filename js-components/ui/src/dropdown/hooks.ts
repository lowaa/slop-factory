import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { DropdownOption } from "./option";
import { decidePlacement, visibleBounds, type PlacementPreference } from "./placement";

// Closes on pointerdown/focusin rather than click: pointerdown fires
// before the option's own mousedown-select can be mistaken for an outside
// click, and focusin is what makes tabbing away close the panel (a plain
// click listener leaves it open for keyboard users).
export function useCloseOnOutside(
  containerRef: RefObject<HTMLDivElement | null>,
  active: boolean,
  onClose: () => void,
): void {
  useEffect(() => {
    if (!active) return;

    function handle(event: Event) {
      const target = event.target as Node | null;
      if (target && containerRef.current && !containerRef.current.contains(target)) {
        onClose();
      }
    }

    document.addEventListener("pointerdown", handle);
    document.addEventListener("focusin", handle);
    return () => {
      document.removeEventListener("pointerdown", handle);
      document.removeEventListener("focusin", handle);
    };
  }, [containerRef, active, onClose]);
}

export function useScrollActiveIntoView(
  listRef: RefObject<HTMLUListElement | null>,
  activeIndex: number,
): void {
  // Tracks the <ul> alongside the index, not the index alone. The panel
  // unmounts on close, so a fresh element needs scrolling even when the
  // index is unchanged -- keying on the index only meant reopening at the
  // same option (the common case: reopen what you just picked) rendered
  // the list scrolled to the top with the active option offscreen.
  const previous = useRef<{ list: HTMLUListElement | null; index: number }>({ list: null, index: -1 });

  useEffect(() => {
    const list = listRef.current;
    // Skip when nothing moved, so the effect doesn't fight the user's own
    // scrolling on unrelated re-renders.
    if (list === previous.current.list && activeIndex === previous.current.index) return;
    previous.current = { list, index: activeIndex };

    if (!list || activeIndex < 0) return;
    // scrollIntoView is absent in jsdom, so the optional call keeps unit
    // tests from throwing rather than needing a stub in every test file.
    list.children[activeIndex]?.scrollIntoView?.({ block: "nearest" });
  });
}

// The keyboard cursor is held as an option *value* and only turned into an
// index against whatever list is on screen right now. Holding the index
// itself meant a parent re-rendering the options while the panel was open
// (a catalog reload, say) silently moved the cursor onto a neighbour, and
// Enter then committed an option the user never highlighted. A value that
// has vanished resolves to -1, so Enter does nothing instead.
export function useActiveOption<T extends string>(visibleOptions: DropdownOption<T>[]) {
  const [activeValue, setActiveValue] = useState<T | null>(null);
  const activeIndex = visibleOptions.findIndex((o) => o.value === activeValue && !o.disabled);

  const activateIndex = useCallback(
    (index: number) => setActiveValue(visibleOptions[index]?.value ?? null),
    [visibleOptions],
  );

  return { activeIndex, activateIndex, setActiveValue };
}

const PANEL_GAP = 4;
// Keeps a flipped or capped panel off the very edge of the screen.
const EDGE_MARGIN = 8;

// Places the already-rendered panel by writing styles straight onto it in
// a layout effect, so it never paints in the wrong spot and a scroll
// doesn't cost a React render. Runs after every render while open because
// filtering changes the panel's natural height. Re-checks on resize and on
// scroll anywhere (capture) rather than closing: a phone's keyboard or
// address bar moving shouldn't dismiss a menu the user is mid-way through.
export function usePanelPlacement(
  containerRef: RefObject<HTMLDivElement | null>,
  listRef: RefObject<HTMLUListElement | null>,
  open: boolean,
  preference: PlacementPreference,
): void {
  const place = useCallback(() => {
    const container = containerRef.current;
    const list = listRef.current;
    if (!container || !list) return;
    const anchor = container.querySelector(".dropdown__trigger") ?? container;
    const containerRect = container.getBoundingClientRect();
    const anchorRect = anchor.getBoundingClientRect();
    const bounds = visibleBounds(container);

    const borders = list.offsetHeight - list.clientHeight;
    const { side, maxHeight } = decidePlacement({
      preference,
      spaceBelow: bounds.bottom - containerRect.bottom - PANEL_GAP - EDGE_MARGIN,
      spaceAbove: anchorRect.top - bounds.top - PANEL_GAP - EDGE_MARGIN,
      contentHeight: list.scrollHeight + borders,
    });

    list.style.maxHeight = `${maxHeight}px`;
    if (side === "top") {
      list.style.top = "auto";
      list.style.bottom = `${containerRect.bottom - anchorRect.top + PANEL_GAP}px`;
    } else {
      list.style.top = "";
      list.style.bottom = "";
    }
  }, [containerRef, listRef, preference]);

  useLayoutEffect(() => {
    if (open) place();
  });

  useEffect(() => {
    if (!open) return;
    function onChange(event: Event) {
      // The list scrolling itself changes nothing about where it sits.
      if (event.target === listRef.current) return;
      place();
    }
    window.addEventListener("resize", onChange);
    window.addEventListener("scroll", onChange, true);
    return () => {
      window.removeEventListener("resize", onChange);
      window.removeEventListener("scroll", onChange, true);
    };
  }, [open, place, listRef]);
}
