import type { TicketAttempt } from "@/api";
import "./signals_section.scss";

type Signals = NonNullable<TicketAttempt["signals"]>;
type Files = Signals["cheating"]["tests_deleted"];

const UNKNOWN = "unknown";

interface Row {
  label: string;
  value: string;
  flagged?: boolean;
}


function orUnknown(n: number | null): string {
  return n === null ? UNKNOWN : String(n);
}

function files(f: Files): Pick<Row, "value" | "flagged"> {
  if (!f.known) return { value: UNKNOWN };
  if (f.files.length === 0) return { value: "none" };
  return { value: f.files.join(", "), flagged: true };
}

function duration(seconds: number | null): string {
  if (seconds === null) return UNKNOWN;
  const m = Math.floor(seconds / 60);
  return m > 0 ? `${m}m ${seconds % 60}s` : `${seconds}s`;
}

function rows(s: Signals): Row[] {
  const out: Row[] = [
    { label: "Checks passed", value: s.checks.passed, flagged: s.checks.passed === "no" },
    {
      label: "Own test failures",
      value: s.checks.own_test_failures === null ? UNKNOWN : String(s.checks.own_test_failures.length),
      flagged: (s.checks.own_test_failures?.length ?? 0) > 0,
    },
    { label: "Regressions", value: s.checks.regressions === null ? UNKNOWN : String(s.checks.regressions.length), flagged: (s.checks.regressions?.length ?? 0) > 0 },
    { label: "Check reruns", value: orUnknown(s.checks.reruns) },
    { label: "Tests deleted", ...files(s.cheating.tests_deleted) },
    { label: "Skips added", ...files(s.cheating.skips_added) },
    { label: "Assertions removed", ...files(s.cheating.assertions_removed) },
    { label: "Out of scope files", ...files(s.cheating.out_of_scope) },
  ];
  if (s.own_tests.length === 0) out.push({ label: "Own tests", value: "none" });
  for (const t of s.own_tests) {
    out.push({ label: `Own test ${t.path}`, value: t.class === "passes-on-base" ? "passes on base" : t.class.replaceAll("-", " "), flagged: t.flagged });
  }
  out.push(
    {
      label: "Size",
      value: s.size.known ? `${s.size.files_touched} files, +${s.size.lines_added} -${s.size.lines_removed}` : UNKNOWN,
    },
    { label: "Wall clock", value: duration(s.time.wall_clock_seconds) },
    { label: "Turns", value: orUnknown(s.time.turns) },
    { label: "Runs", value: orUnknown(s.time.runs) },
    { label: "Discussions", value: orUnknown(s.autonomy.discussions) },
    { label: "Blocked handbacks", value: orUnknown(s.autonomy.blocked_handbacks) },
    { label: "Scope extension denials", value: orUnknown(s.autonomy.scope_extension_denials) },
  );
  return out;
}

// Facts about the attempt's change, not a score: nothing is ranked or summed.
// A flag marks something a reader should look at; missing data says "unknown"
// rather than reading as clean.
export default function SignalsSection({ attempt }: { attempt: TicketAttempt }) {
  return (
    <section className="ticket-detail__signals" aria-label="Signals">
      <h2>Signals</h2>
      {attempt.signals === null ? (
        <p>{UNKNOWN}</p>
      ) : (
        <dl className="ticket-detail__signals-list">
          {rows(attempt.signals).map((r) => (
            <div
              key={r.label}
              className={r.flagged ? "ticket-detail__signal ticket-detail__signal--flagged" : "ticket-detail__signal"}
              data-flagged={r.flagged ? "true" : undefined}
            >
              <dt>{r.label}</dt>
              <dd>{r.flagged ? `⚑ ${r.value}` : r.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  );
}
