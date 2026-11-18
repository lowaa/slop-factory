import { Children, cloneElement, createContext, isValidElement, useContext } from "react";
import type { ComponentProps, ReactElement, ReactNode } from "react";
import "./legal_table.scss";

// react-markdown hands its overrides the hast node the element came from.
interface HastNode {
  type: string;
  tagName?: string;
  value?: string;
  children?: HastNode[];
}

type NodeProps<T extends keyof React.JSX.IntrinsicElements> = ComponentProps<T> & { node?: HastNode };

function textOf(n: HastNode): string {
  if (n.type === "text") return n.value ?? "";
  return (n.children ?? []).map(textOf).join("");
}

// Labels of the header row, empty strings included, so column indexes line up.
function headerLabels(node: HastNode | undefined): string[] {
  const head = node?.children?.find((c) => c.tagName === "thead");
  const row = head?.children?.find((c) => c.tagName === "tr");
  return (row?.children ?? [])
    .filter((c) => c.tagName === "th" || c.tagName === "td")
    .map((c) => textOf(c).replace(/\s+/g, " ").trim());
}

const HeadersContext = createContext<string[]>([]);

// The cells of a row learn their column from their position, since
// react-markdown gives an override no index.
function withColumns(children: ReactNode): ReactNode {
  let col = 0;
  return Children.map(children, (child) =>
    isValidElement(child) ? cloneElement(child as ReactElement<{ "data-col"?: number }>, { "data-col": col++ }) : child,
  );
}

export function LegalTr({ node: _node, children, ...rest }: NodeProps<"tr">) {
  return (
    <tr role="row" {...rest}>
      {withColumns(children)}
    </tr>
  );
}

export function LegalTh({ node: _node, children, ...rest }: NodeProps<"th">) {
  return (
    <th role="columnheader" scope="col" {...rest}>
      {children}
    </th>
  );
}

export function LegalTd({ node: _node, children, ...rest }: NodeProps<"td"> & { "data-col"?: number }) {
  const label = useContext(HeadersContext)[rest["data-col"] ?? -1];
  return (
    <td role="cell" {...rest}>
      {/* The cell already has its column header for a screen reader; the
          visible label is for the stacked layout, where the header row is
          hidden. */}
      {label ? (
        <span className="legal-table-label" aria-hidden="true">
          {label}:{" "}
        </span>
      ) : null}
      {children}
    </td>
  );
}

// A table in a legal document: it scrolls sideways in a wide container, and
// each row becomes a card in a narrow one.
export function LegalTable({ node, children }: NodeProps<"table">) {
  const headers = headerLabels(node);
  // Without a header row there is nothing to label a card's lines with, so
  // the table only scrolls.
  const labelled = headers.some((h) => h !== "");
  const name = labelled ? `Table: ${headers.filter(Boolean).join(", ")}` : "Table";
  return (
    <div className="legal-table">
      <div
        className={`legal-table-scroll${labelled ? " legal-table-scroll-stackable" : ""}`}
        role="region"
        aria-label={name}
        // A scroll region must be reachable by keyboard.
        tabIndex={0}
      >
        <HeadersContext.Provider value={labelled ? headers : []}>
          <table className="legal-table-table" role="table">
            {children}
          </table>
        </HeadersContext.Provider>
      </div>
    </div>
  );
}

export function LegalThead({ node: _node, ...rest }: NodeProps<"thead">) {
  return <thead role="rowgroup" {...rest} />;
}

export function LegalTbody({ node: _node, ...rest }: NodeProps<"tbody">) {
  return <tbody role="rowgroup" {...rest} />;
}

export const legalTableComponents = {
  table: LegalTable,
  thead: LegalThead,
  tbody: LegalTbody,
  tr: LegalTr,
  th: LegalTh,
  td: LegalTd,
};
