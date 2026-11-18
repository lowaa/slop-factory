import { useCallback, useId, useRef, useState, type KeyboardEvent } from "react";
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

export interface DropdownProps<T extends string> {
  label: string;
  value: T | null;
  options: DropdownOption<T>[];
  onChange: (value: T) => void;
  placeholder?: string;
  disabled?: boolean;
  emptyMessage?: string;
  // Keeps the label in the accessibility tree but out of the layout, for
  // grids that already name the column in a header row. Never drop the
  // label itself -- the combobox would lose its accessible name.
  labelHidden?: boolean;
  // Blocks the surrounding form's submit while nothing is selected, the
  // way <select required> does.
  required?: boolean;
  // "auto" opens upwards when the menu does not fit below but fits above;
  // either way its height is capped to the room on the side it opens.
  placement?: PlacementPreference;
  // Lets a wrapper that describes the control (a Tooltip clones this onto
  // its child) reach the combobox, which is the element that is announced.
  "aria-describedby"?: string;
}

// Which option to land on when the panel opens. 'selected' is the normal
// case; 'last' exists so ArrowUp on a closed dropdown opens at the bottom
// of the list the way a native <select> does.
type OpenMode = "selected" | "last" | "first";

export default function Dropdown<T extends string>({
  label,
  value,
  options,
  onChange,
  placeholder = "Choose…",
  disabled = false,
  emptyMessage = "Nothing to choose from.",
  labelHidden = false,
  required = false,
  placement = "auto",
  "aria-describedby": describedBy,
}: DropdownProps<T>) {
  const labelId = useId();
  const triggerId = useId();
  const listId = useId();

  const [open, setOpen] = useState(false);
  const { activeIndex, activateIndex } = useActiveOption(options);

  const containerRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  const selected = options.find((o) => o.value === value) ?? null;

  const close = useCallback(() => setOpen(false), []);
  useCloseOnOutside(containerRef, open, close);
  useScrollActiveIntoView(listRef, activeIndex);
  usePanelPlacement(containerRef, listRef, open, placement);

  function openAt(mode: OpenMode) {
    if (disabled) return;
    // A selected-but-disabled option is treated as no selection for
    // seeding -- landing the cursor somewhere Enter can't commit is worse
    // than starting at the top.
    const selectedIndex = options.findIndex((o) => o.value === value && !o.disabled);
    const seed =
      mode === "last"
        ? lastEnabledIndex(options)
        : mode === "selected" && selectedIndex >= 0
          ? selectedIndex
          : firstEnabledIndex(options);
    activateIndex(seed);
    setOpen(true);
  }

  function select(option: DropdownOption<T>) {
    onChange(option.value);
    setOpen(false);
  }

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>) {
    if (disabled) return;

    if (!open) {
      if (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openAt("selected");
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        openAt("last");
      }
      return;
    }

    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        activateIndex(activeIndex < 0 ? firstEnabledIndex(options) : nextEnabledIndex(options, activeIndex, 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        activateIndex(activeIndex <= 0 ? firstEnabledIndex(options) : nextEnabledIndex(options, activeIndex, -1));
        break;
      case "Home":
        e.preventDefault();
        activateIndex(firstEnabledIndex(options));
        break;
      case "End":
        e.preventDefault();
        activateIndex(lastEnabledIndex(options));
        break;
      // Space commits here, unlike FilterableDropdown where it has to type
      // a space into the filter.
      case "Enter":
      case " ": {
        e.preventDefault();
        const option = options[activeIndex];
        if (option && !option.disabled) select(option);
        break;
      }
      case "Escape":
        e.preventDefault();
        setOpen(false);
        break;
      // Not preventDefault'd -- closing and letting focus move on is the
      // expected Tab behaviour.
      case "Tab":
        setOpen(false);
        break;
    }
  }

  const activeDescendant =
    open && activeIndex >= 0 && activeIndex < options.length ? optionId(listId, activeIndex) : undefined;

  return (
    <div className="dropdown" ref={containerRef}>
      <span className={labelHidden ? "dropdown__label dropdown__label--hidden" : "dropdown__label"} id={labelId}>
        {label}
      </span>
      <button
        ref={triggerRef}
        type="button"
        id={triggerId}
        className={`dropdown__trigger${open ? " dropdown__trigger--open" : ""}`}
        role="combobox"
        aria-labelledby={labelId}
        aria-expanded={open}
        aria-controls={listId}
        aria-activedescendant={activeDescendant}
        aria-haspopup="listbox"
        aria-required={required || undefined}
        aria-describedby={describedBy}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : openAt("selected"))}
        onKeyDown={handleKeyDown}
      >
        {selected?.leading && (
          <span className="dropdown__leading" aria-hidden="true">
            {selected.leading}
          </span>
        )}
        <span className={selected ? "dropdown__value" : "dropdown__value dropdown__value--placeholder"}>
          {selected ? selected.label : placeholder}
        </span>
        <span className="dropdown__caret" aria-hidden="true" />
      </button>
      {open && (
        <DropdownPanel
          listId={listId}
          labelId={labelId}
          options={options}
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
      <ValidationInput value={selected?.value ?? null} required={required && !disabled} focusTarget={triggerRef} />
    </div>
  );
}
