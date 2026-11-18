import { useCallback, useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import DropdownPanel from "./dropdown_panel";
import { useActiveOption, useCloseOnOutside, useScrollActiveIntoView, usePanelPlacement } from "./hooks";
import {
  firstEnabledIndex,
  lastEnabledIndex,
  nextEnabledIndex,
  optionId,
  type DropdownOption,
} from "./option";
import ValidationInput from "./validation_input";
import type { PlacementPreference } from "./placement";
import "./dropdown.scss";

export interface FilterableDropdownProps<T extends string> {
  label: string;
  value: T | null;
  options: DropdownOption<T>[];
  onChange: (value: T) => void;
  placeholder?: string;
  disabled?: boolean;
  emptyMessage?: string;
  // See Dropdown's labelHidden -- visually hidden, still announced.
  labelHidden?: boolean;
  // See Dropdown's required.
  required?: boolean;
  // "auto" opens upwards when the menu does not fit below but fits above;
  // either way its height is capped to the room on the side it opens.
  placement?: PlacementPreference;
}

// Case-insensitive substring over label *and* hint, so a measurement type
// can be found by its category as well as its name. Matches anywhere
// rather than by prefix -- "cell" should find "White cell count".
function optionMatchesFilter<T extends string>(option: DropdownOption<T>, filter: string): boolean {
  const needle = filter.trim().toLowerCase();
  if (needle === "") return true;
  return `${option.label} ${option.hint ?? ""}`.toLowerCase().includes(needle);
}

export default function FilterableDropdown<T extends string>({
  label,
  value,
  options,
  onChange,
  placeholder = "Type to filter…",
  disabled = false,
  emptyMessage = "No matches.",
  labelHidden = false,
  required = false,
  placement = "auto",
}: FilterableDropdownProps<T>) {
  const labelId = useId();
  const inputId = useId();
  const listId = useId();

  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  const selected = options.find((o) => o.value === value) ?? null;

  const close = useCallback(() => setOpen(false), []);
  useCloseOnOutside(containerRef, open, close);

  // Only filter while open; closed, the panel isn't rendered and a stale
  // filter must not affect anything.
  const filtered = useMemo(
    () => (open ? options.filter((o) => optionMatchesFilter(o, filter)) : options),
    [open, options, filter],
  );
  const { activeIndex, activateIndex, setActiveValue } = useActiveOption(filtered);
  useScrollActiveIntoView(listRef, activeIndex);
  usePanelPlacement(containerRef, listRef, open, placement);

  function openList() {
    if (disabled) return;
    // Always reopen on the full list. Reopening to a leftover filter hides
    // most options with no visible cause.
    setFilter("");
    activateIndex(firstEnabledIndex(options));
    setOpen(true);
  }

  // The caret and the trigger's padding are outside the input, so without
  // this a click there did nothing -- users had to find the text area. On
  // mousedown, with the default prevented, so focus goes straight to the
  // input instead of blurring it first. Toggles like the plain Dropdown's
  // caret: a second click closes. openList is called directly because
  // focus() fires no focus event when the input already has focus (after
  // Escape, say).
  function handleTriggerMouseDown(e: MouseEvent<HTMLDivElement>) {
    if (disabled || e.button !== 0 || e.target === inputRef.current) return;
    e.preventDefault();
    if (open) {
      setOpen(false);
      return;
    }
    inputRef.current?.focus();
    openList();
  }

  function select(option: DropdownOption<T>) {
    onChange(option.value);
    setOpen(false);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (disabled) return;

    if (!open) {
      // Only the arrows open. Enter must stay available to the surrounding
      // form, and Space has to type a space.
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        openList();
      }
      return;
    }

    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        activateIndex(activeIndex < 0 ? firstEnabledIndex(filtered) : nextEnabledIndex(filtered, activeIndex, 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        activateIndex(activeIndex <= 0 ? firstEnabledIndex(filtered) : nextEnabledIndex(filtered, activeIndex, -1));
        break;
      case "Home":
        e.preventDefault();
        activateIndex(firstEnabledIndex(filtered));
        break;
      case "End":
        e.preventDefault();
        activateIndex(lastEnabledIndex(filtered));
        break;
      case "Enter": {
        // Prevented unconditionally, even with nothing selectable: an open
        // panel must never let Enter submit the form behind it.
        e.preventDefault();
        const option = filtered[activeIndex];
        if (option && !option.disabled) select(option);
        break;
      }
      case "Escape":
        e.preventDefault();
        setOpen(false);
        break;
      case "Tab":
        setOpen(false);
        break;
    }
  }

  const activeDescendant =
    open && activeIndex >= 0 && activeIndex < filtered.length ? optionId(listId, activeIndex) : undefined;

  return (
    <div className="dropdown dropdown--filterable" ref={containerRef}>
      <span className={labelHidden ? "dropdown__label dropdown__label--hidden" : "dropdown__label"} id={labelId}>
        {label}
      </span>
      <div
        className={`dropdown__trigger${open ? " dropdown__trigger--open" : ""}`}
        onMouseDown={handleTriggerMouseDown}
      >
        <input
          ref={inputRef}
          id={inputId}
          type="text"
          className="dropdown__input"
          role="combobox"
          aria-labelledby={labelId}
          aria-autocomplete="list"
          aria-expanded={open}
          aria-controls={listId}
          aria-activedescendant={activeDescendant}
          aria-required={required || undefined}
          autoComplete="off"
          disabled={disabled}
          // Showing the selected label as placeholder keeps the current
          // selection readable in the one state where the box is empty:
          // just-opened, before the user has typed.
          placeholder={selected ? selected.label : placeholder}
          value={open ? filter : (selected?.label ?? "")}
          onChange={(e) => {
            setFilter(e.target.value);
            setOpen(true);
            // By value: `filtered` still holds the pre-keystroke list here,
            // so an index into it would point at the wrong row.
            const matches = options.filter((o) => optionMatchesFilter(o, e.target.value));
            setActiveValue(matches[firstEnabledIndex(matches)]?.value ?? null);
          }}
          onFocus={openList}
          onClick={() => {
            if (!open) openList();
          }}
          onKeyDown={handleKeyDown}
        />
        <span className="dropdown__caret" aria-hidden="true" />
      </div>
      {open && (
        <DropdownPanel
          listId={listId}
          labelId={labelId}
          options={filtered}
          activeIndex={activeIndex}
          selectedValue={value}
          emptyMessage={emptyMessage}
          onPick={select}
          onHover={activateIndex}
          listRef={listRef}
        />
      )}
      {/* A disabled control is exempt, as a disabled <select required> is:
          otherwise a dropdown with nothing to pick would block the form
          with no way out. */}
      <ValidationInput value={selected?.value ?? null} required={required && !disabled} focusTarget={inputRef} />
    </div>
  );
}
