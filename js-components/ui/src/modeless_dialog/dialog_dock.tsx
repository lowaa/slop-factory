import { Children, Fragment, isValidElement, type ReactNode } from "react";
import "./dialog_dock.scss";

export interface DialogDockProps {
  // Names the landmark, e.g. "Open invoices".
  label: string;
  className?: string;
  // ModelessDialogs, stacked in the order given.
  children?: ReactNode;
}

// DialogDock stacks modeless dialogs in a column the host lays out beside
// its content, so they never cover it. It owns no placement of its own
// beyond that stack: a host that later wants a dialog floating free renders
// it outside the dock instead. Renders nothing when empty, so an empty dock
// takes no room in the host's layout.
export default function DialogDock({ label, className, children }: DialogDockProps) {
  if (!hasContent(children)) return null;
  const classes = ["dialog-dock", className].filter(Boolean).join(" ");
  return (
    <aside className={classes} aria-label={label}>
      {children}
    </aside>
  );
}

// Children.toArray drops null/false but keeps a fragment as one child, so a
// host's `<>{open && <ModelessDialog/>}</>` would otherwise count as content.
function hasContent(children: ReactNode): boolean {
  return Children.toArray(children).some((child) =>
    isValidElement<{ children?: ReactNode }>(child) && child.type === Fragment ? hasContent(child.props.children) : true,
  );
}
