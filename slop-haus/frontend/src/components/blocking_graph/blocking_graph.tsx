import { useMemo, useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type BlockingGraph as BlockingGraphData, type GraphNode, type Ticket } from "@/api";
import { useLogger } from "@/context/logger";
import { usePolledResource } from "@/lib/use_polled_resource";
import { usePagedCollection } from "@/lib/paged_collection/use_paged_collection";
import { TicketFilter } from "@/components/ticket_filter";
import type { FilterValues } from "@/domain/ticket_filter_model";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { PagedTicketList, CONTROL_CLASS, PagedTicketRow } from "@/components/paged_ticket_list";
import { BLOCKED_FILTER_MODEL, BLOCKED_QUERY_MODEL, fetchBlockedPage } from "./blocked_filter";
import "./blocking_graph.scss";

const ROW_HEIGHT = 56;

interface BlockingGraphViewProps {
  values: FilterValues;
  // A changed filter replaces the address entry, as on the other ticket lists.
  onFilterChange: (values: FilterValues) => void;
}

// The blocked tickets, the ones holding up the most first -- the server's
// filter, order and paging. A graph of the whole board is never drawn: a row
// opens to the graph of its own ticket, which is small.
export default function BlockingGraphView({ values, onFilterChange }: BlockingGraphViewProps) {
  const logger = useLogger();
  const query = useMemo(() => BLOCKED_QUERY_MODEL.initial(values), [values]);
  const deps = useMemo(
    () => ({ key: "blocked", queryModel: BLOCKED_QUERY_MODEL, fetchPage: fetchBlockedPage, logger }),
    [logger],
  );
  const list = usePagedCollection(deps, query);

  return (
    <PagedTicketList
      className="blocking-graph"
      list={list}
      filter={<TicketFilter model={BLOCKED_FILTER_MODEL} values={values} onChange={onFilterChange} />}
      filtered={BLOCKED_FILTER_MODEL.isFiltered(values)}
      matchesNothingMessage={
        BLOCKED_QUERY_MODEL.toApi(query).matchesNothing ? "Every status is hidden, so no tickets match." : undefined
      }
      loadingMessage="Loading blocked tickets…"
      emptyMessage={{ filtered: "No tickets match these filters.", unfiltered: "Nothing is blocked." }}
      label="Blocked tickets"
      estimateRowHeight={ROW_HEIGHT}
      renderRow={(ticket) => <BlockedRow ticket={ticket} />}
    />
  );
}

function BlockedRow({ ticket }: { ticket: Ticket }) {
  const [open, setOpen] = useState(false);
  const panelId = `blocked-graph-${ticket.id}`;
  return (
    <PagedTicketRow
      ticketId={ticket.id}
      from="blocked"
      link={
        <>
          <span className="blocking-graph__card-id">#{ticket.id}</span> {ticket.title}
        </>
      }
      detail={
        open && (
          <div id={panelId} className="blocking-graph__detail">
            <TicketGraph ticketId={ticket.id} />
          </div>
        )
      }
    >
      <span className="blocking-graph__card-status">{ticket.status}</span>
      {(ticket.unblocks ?? 0) > 0 && <span className="blocking-graph__card-unblocks">unblocks {ticket.unblocks}</span>}
      <Button
        className={CONTROL_CLASS}
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={`${open ? "Hide" : "Show"} graph for #${ticket.id}`}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? "Hide graph" : "Show graph"}
      </Button>
    </PagedTicketRow>
  );
}

// Loaded when the row is opened, and again each time it is: the list row can
// be recycled and the graph is cheap to fetch.
function TicketGraph({ ticketId }: { ticketId: number }) {
  const state = usePolledResource(() => apiClient.getTicketBlockers(ticketId), {
    deps: [ticketId],
    fallbackError: "Failed to load the graph.",
  });

  if (state.status === "loading") return <p>Loading graph…</p>;
  if (state.status === "error") {
    return (
      <p role="alert" className="blocking-graph__notice">
        {state.error}
      </p>
    );
  }
  return state.data && <Layers graph={state.data} />;
}

// nodesById turns the flat nodes array the server sends into a lookup --
// every layer entry resolves back to its node, and a ticket's graph is small
// enough that rebuilding this per render is simpler than caching it.
function nodesById(graph: BlockingGraphData): Map<number, GraphNode> {
  return new Map(graph.nodes.map((n) => [n.id, n]));
}

interface LayersProps {
  graph: BlockingGraphData;
}

// Layers renders graph.layers left to right as columns -- layer 0 (nothing
// blocking it) leftmost, each later layer one past its deepest unfinished
// blocker, exactly as server/internal/ticket/graph.go's Layer doc comment
// describes it. The ordering is the server's to make; this only draws it.
function Layers({ graph }: LayersProps) {
  const byId = nodesById(graph);

  return (
    <div className="blocking-graph__layers">
      {graph.layers.map((layerIds, layerIndex) => (
        <div className="blocking-graph__layer" key={layerIndex}>
          <div className="blocking-graph__layer-header">Layer {layerIndex}</div>
          <div className="blocking-graph__layer-cards">
            {layerIds.map((id) => {
              const node = byId.get(id);
              // Every id in `layers` names a node in `nodes` by construction
              // (buildGraph seeds both from the same ticket set) -- this
              // guard is only here so a malformed response can't crash the
              // page rather than something expected to trigger.
              if (!node) return null;
              return <GraphCard key={id} node={node} />;
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

interface GraphCardProps {
  node: GraphNode;
}

// A link to the ticket, as ticket_card.tsx's is. Clicking one opens
// that ticket, the same as clicking its card on the board -- the graph
// answers "what is holding this up", and the answer to "so fix it" is the
// ticket itself.
function GraphCard({ node }: GraphCardProps) {
  const classes = [
    "blocking-graph__card",
    node.blocked ? "blocking-graph__card--blocked" : "blocking-graph__card--clear",
    node.hidden && "blocking-graph__card--hidden",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <TicketLink ticketId={node.id} from="blocked" className={classes}>
      <div className="blocking-graph__card-header">
        <span className="blocking-graph__card-id">#{node.id}</span>
        {node.unblocks > 0 && <span className="blocking-graph__card-unblocks">unblocks {node.unblocks}</span>}
      </div>
      <p className="blocking-graph__card-title">{node.title}</p>
      <p className="blocking-graph__card-status">{node.status}</p>
      {/* Not matched by the filter but still holding a shown ticket up, so
          it is drawn rather than leaving that ticket looking free. */}
      {node.hidden && <p className="blocking-graph__card-hidden">hidden by filter, still blocking</p>}
      {/* Textual edges rather than a drawn SVG overlay -- per the task's own
          steer, readable beats clever here, and it also means every edge is
          legible without needing the page wide enough to see both ends of
          a line. */}
      {node.blocked_by.length > 0 && (
        <p className="blocking-graph__card-blockers">
          blocked by {node.blocked_by.map((id) => `#${id}`).join(", ")}
        </p>
      )}
    </TicketLink>
  );
}
