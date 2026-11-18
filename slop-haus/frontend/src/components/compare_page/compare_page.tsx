import { useMemo, useState } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type CompareRequest } from "@/api";
import { TicketFilter } from "@/components/ticket_filter";
import { ResourceView } from "@/lib/resource/resource_view";
import { useResource } from "@/lib/resource/use_resource";
import CompareResults from "./compare_results";
import {
  COMPARE_FILTER_MODEL,
  DEFAULT_VARIANT,
  blockedReason,
  buildRequest,
  variantLabel,
  type ComparePick,
  type SetSource,
} from "./compare_model";
import { useFilteredTickets } from "./use_filtered_tickets";
import "./compare_page.scss";

// What a comparison reads at most; the server answers 422 over it. Its
// response carries the bound too, but the set is picked before there is one.
const MAX_TICKETS = 200;

function toggle(list: string[], name: string): string[] {
  return list.includes(name) ? list.filter((n) => n !== name) : [...list, name];
}

// The Compare tab: pick a set of tickets, a baseline and challengers, and
// read each challenger's paired differences from the baseline, by group.
// The request is only sent when asked for, and what is shown is for the
// request as it was when asked.
export default function ComparePage() {
  const [source, setSource] = useState<SetSource>("batch");
  const [batchId, setBatchId] = useState("");
  const [filter, setFilter] = useState(() => COMPARE_FILTER_MODEL.defaults());
  const [minLines, setMinLines] = useState("");
  const [maxLines, setMaxLines] = useState("");
  const [baseline, setBaseline] = useState(DEFAULT_VARIANT);
  const [challengers, setChallengers] = useState<string[]>([]);
  const [asked, setAsked] = useState<CompareRequest | null>(null);

  const options = useResource(["compare", "variant-options"], () => apiClient.listVariantOptions(), {
    fallbackError: "Failed to load variants.",
  });
  const names = useMemo(
    () => [DEFAULT_VARIANT, ...[...new Set((options.data ?? []).map((o) => o.name))].sort()],
    [options.data],
  );
  const tickets = useFilteredTickets(filter, MAX_TICKETS, source === "filter");

  const pick: ComparePick = {
    source,
    batchId,
    ticketIds: tickets.data?.ids ?? [],
    ticketsTruncated: tickets.data?.truncated ?? false,
    minLines,
    maxLines,
    baseline,
    challengers,
  };
  const blocked = blockedReason(pick, MAX_TICKETS);

  const result = useResource(["compare", "result", asked], () => apiClient.compareVariants(asked as CompareRequest), {
    enabled: asked !== null,
    fallbackError: "Failed to compare.",
  });

  // The cache answers the same request from memory, so asking again for what
  // is already shown fetches afresh: the person wants what the server says now.
  function ask() {
    const next = buildRequest(pick);
    if (asked !== null && JSON.stringify(asked) === JSON.stringify(next)) void result.reload();
    else setAsked(next);
  }

  return (
    <div className="compare-page">
      <h2>Compare variants</h2>
      <fieldset className="compare-page__set">
        <legend>Tickets</legend>
        <label>
          <input type="radio" name="source" checked={source === "batch"} onChange={() => setSource("batch")} />
          A replay batch
        </label>
        <label>
          <input type="radio" name="source" checked={source === "filter"} onChange={() => setSource("filter")} />
          Tickets picked by filter
        </label>
        {source === "batch" ? (
          <label>
            Replay batch id
            <input type="text" value={batchId} onChange={(e) => setBatchId(e.target.value)} placeholder="Shown when the batch is added" />
          </label>
        ) : (
          <>
            <TicketFilter legend="Done tickets" model={COMPARE_FILTER_MODEL} values={filter} onChange={setFilter} />
            <p role="status">
              {tickets.status === "loading"
                ? "Loading tickets…"
                : tickets.status === "error"
                  ? tickets.error
                  : `${tickets.data?.ids.length ?? 0}${tickets.data?.truncated ? "+" : ""} ${tickets.data?.ids.length === 1 ? "ticket matches" : "tickets match"}`}
            </p>
          </>
        )}
        <label>
          Smallest original change (lines)
          <input type="text" inputMode="numeric" value={minLines} onChange={(e) => setMinLines(e.target.value)} />
        </label>
        <label>
          Largest original change (lines)
          <input type="text" inputMode="numeric" value={maxLines} onChange={(e) => setMaxLines(e.target.value)} />
        </label>
      </fieldset>
      <fieldset className="compare-page__variants">
        <legend>Variants</legend>
        {options.status === "error" && <p role="alert">{options.error}</p>}
        <Dropdown<string>
          label="Baseline"
          value={baseline}
          options={names.map((n) => ({ value: n, label: variantLabel(n) }))}
          onChange={(next) => {
            setBaseline(next);
            setChallengers((c) => c.filter((n) => n !== next));
          }}
        />
        <div role="group" aria-label="Challengers">
          {names
            .filter((n) => n !== baseline)
            .map((n) => (
              <label key={n}>
                <input type="checkbox" checked={challengers.includes(n)} onChange={() => setChallengers(toggle(challengers, n))} />
                {variantLabel(n)}
              </label>
            ))}
        </div>
      </fieldset>
      {blocked && <p role="note">{blocked}</p>}
      <Button variant="primary" disabled={blocked !== null} onClick={ask}>
        Compare
      </Button>
      {asked && (
        <ResourceView state={result} loading="Comparing…">
          {(data) => <CompareResults result={data} />}
        </ResourceView>
      )}
    </div>
  );
}
