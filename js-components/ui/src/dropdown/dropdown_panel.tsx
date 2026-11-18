import type { RefObject } from "react";
import { optionId, type DropdownOption } from "./option";

interface DropdownPanelProps<T extends string> {
  listId: string;
  labelId: string;
  // Already filtered by the caller -- indices here are panel indices, and
  // are what the trigger's aria-activedescendant must point at.
  options: DropdownOption<T>[];
  activeIndex: number;
  selectedValue: T | null;
  emptyMessage: string;
  onPick: (option: DropdownOption<T>) => void;
  onHover: (index: number) => void;
  listRef: RefObject<HTMLUListElement | null>;
}

export default function DropdownPanel<T extends string>({
  listId,
  labelId,
  options,
  activeIndex,
  selectedValue,
  emptyMessage,
  onPick,
  onHover,
  listRef,
}: DropdownPanelProps<T>) {
  if (options.length === 0) {
    return (
      <ul className="dropdown__panel" id={listId} role="listbox" aria-labelledby={labelId} ref={listRef}>
        <li className="dropdown__empty" role="option" aria-selected={false} aria-disabled={true}>
          {emptyMessage}
        </li>
      </ul>
    );
  }

  return (
    <ul className="dropdown__panel" id={listId} role="listbox" aria-labelledby={labelId} ref={listRef}>
      {options.map((option, index) => (
        <li
          key={option.value}
          id={optionId(listId, index)}
          role="option"
          aria-selected={option.value === selectedValue}
          aria-disabled={option.disabled || undefined}
          className={[
            "dropdown__option",
            index === activeIndex ? "dropdown__option--active" : "",
            option.value === selectedValue ? "dropdown__option--selected" : "",
            option.disabled ? "dropdown__option--disabled" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          // mousedown, not click: preventDefault here stops the browser
          // moving focus to the <li>, which would fire the outside-close
          // focusin listener and steal the caret from the filter input.
          onMouseDown={(e) => {
            e.preventDefault();
            if (!option.disabled) onPick(option);
          }}
          // Disabled rows don't take the cursor: Enter on them does
          // nothing, so hovering one would strand the keyboard user.
          onMouseEnter={() => {
            if (!option.disabled) onHover(index);
          }}
        >
          {option.leading && <span className="dropdown__leading" aria-hidden="true">{option.leading}</span>}
          <span className="dropdown__option-label">{option.label}</span>
          {option.hint && <span className="dropdown__option-hint">{option.hint}</span>}
        </li>
      ))}
    </ul>
  );
}
