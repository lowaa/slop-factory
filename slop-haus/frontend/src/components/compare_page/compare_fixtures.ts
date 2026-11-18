import type { CompareResult, CompareSide, CompareVerdicts } from "@/api";

const verdicts = (v: Partial<CompareVerdicts> = {}): CompareVerdicts => ({
  would_merge: 0,
  would_merge_with_changes: 0,
  would_not_merge: 0,
  unjudged: 0,
  ...v,
});

const side = (attempts: number[]): CompareSide => ({
  runs: attempts.length,
  attempts,
  measures: {},
  verdicts: verdicts(),
  run_with: { variant_versions: [], harness_versions: [], models: [], bundle_hashes: [], fleet_instructions: [] },
});

// A comparison of "Careful" with the default variant over `tickets` tickets,
// the first of which ran with a different model.
export function makeCompareResult(tickets = 6, withSecondChallenger = false): CompareResult {
  const ids = Array.from({ length: tickets }, (_, i) => 100 + i);
  const summary = (median: number) => ({ n: tickets, median, q1: median - 1, q3: median + 1, min: median - 3, max: median + 3 });
  return {
    baseline: "",
    max_tickets: 200,
    tickets_in_set: tickets,
    size_filtered: 0,
    unfinished_attempts: 0,
    measures: [
      { key: "cost_usd", group: "cost", unit: "USD", doc: "Priced LLM usage." },
      { key: "human_messages", group: "human_cost", unit: "messages", doc: "Messages a person sent the attempt." },
      { key: "review_rounds", group: "quality", unit: "rounds", doc: "Review rounds before merge." },
      { key: "checks_failed", group: "signals", unit: "", doc: "Red checks." },
      { key: "remote_control", group: "not_counted", unit: "", doc: "Remote Control guidance is general overhead and not counted." },
    ],
    pairs: [
      ...(withSecondChallenger
        ? [{ challenger: "Fast", tickets: 1, flagged_tickets: 0, measures: {}, verdicts: { baseline: verdicts(), challenger: verdicts() } }]
        : []),
      {
        challenger: "Careful",
        tickets,
        flagged_tickets: 1,
        measures: { cost_usd: summary(0.25), human_messages: summary(-2), review_rounds: summary(0) },
        verdicts: {
          baseline: verdicts({ would_merge: tickets - 1, unjudged: 1 }),
          challenger: verdicts({ would_merge: tickets - 2, would_not_merge: 1, unjudged: 1 }),
        },
      },
    ],
    rows: [...ids.map((id, i) => ({
      ticket_id: id,
      challenger: "Careful",
      baseline: side([1]),
      challenger_side: side([2]),
      difference: { cost_usd: 0.25 },
      run_with_flags: i === 0 ? ["models"] : [],
    })), ...(withSecondChallenger ? [{ ticket_id: 900, challenger: "Fast", baseline: side([1]), challenger_side: side([3]), difference: {}, run_with_flags: [] }] : [])],
    notes: ["Remote Control guidance is general overhead and is not counted."],
  };
}
