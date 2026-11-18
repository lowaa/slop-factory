import { useId, useRef, type KeyboardEvent, type ReactNode } from "react";
import "./tabs.scss";

export interface TabItem<Id extends string> {
  id: Id;
  label: string;
}

interface TabsProps<Id extends string> {
  items: TabItem<Id>[];
  value: Id;
  onChange: (id: Id) => void;
  // Names the tab list for assistive technology.
  label: string;
  // The selected tab's panel.
  children: ReactNode;
}

// Tabs shows one panel at a time. Only the selected panel is rendered, so a
// hidden tab's commands are never copied by accident. Arrow keys, Home and End
// move between tabs, which is the keyboard model of the ARIA tabs pattern.
export default function Tabs<Id extends string>({ items, value, onChange, label, children }: TabsProps<Id>) {
  const base = useId();
  const tabRefs = useRef(new Map<Id, HTMLButtonElement>());
  const tabId = (id: Id) => `${base}-tab-${id}`;
  const panelId = `${base}-panel`;

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = items.length - 1;
    const target =
      e.key === "ArrowRight" ? (index === last ? 0 : index + 1)
      : e.key === "ArrowLeft" ? (index === 0 ? last : index - 1)
      : e.key === "Home" ? 0
      : e.key === "End" ? last
      : null;
    if (target === null) return;
    e.preventDefault();
    const next = items[target].id;
    onChange(next);
    tabRefs.current.get(next)?.focus();
  }

  return (
    <div className="tabs">
      <div className="tabs__list" role="tablist" aria-label={label}>
        {items.map((item, index) => {
          const selected = item.id === value;
          return (
            <button
              key={item.id}
              ref={(el) => {
                if (el) tabRefs.current.set(item.id, el);
                else tabRefs.current.delete(item.id);
              }}
              type="button"
              role="tab"
              id={tabId(item.id)}
              aria-selected={selected}
              aria-controls={selected ? panelId : undefined}
              tabIndex={selected ? 0 : -1}
              className={selected ? "is-selected" : ""}
              onClick={() => onChange(item.id)}
              onKeyDown={(e) => handleKeyDown(e, index)}
            >
              {item.label}
            </button>
          );
        })}
      </div>
      <div className="tabs__panel" role="tabpanel" id={panelId} aria-labelledby={tabId(value)}>
        {children}
      </div>
    </div>
  );
}
