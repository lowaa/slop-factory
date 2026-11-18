import { useState } from "react";
import { Button } from "@slop-factory/ui";
import {
  apiClient,
  type StewardDigest,
  type StewardDigestCategory,
  type StewardDigestWindow,
} from "@/api";
import { ResourceView } from "@/lib/resource/resource_view";
import { usePolledResource } from "@/lib/use_polled_resource";
import StewardDiscussionsSection from "./steward_discussions";
import TicketLink from "@/shared/ticket_link/ticket_link";
import "./steward_digest.scss";

const WINDOWS: { window: StewardDigestWindow; label: string }[] = [
  { window: "24h", label: "Last 24 hours" },
  { window: "7d", label: "Last 7 days" },
];

function percent(rate: number | null): string {
  return rate === null ? "–" : `${Math.round(rate * 100)}%`;
}

function Row({ row, label }: { row: StewardDigestCategory; label: string }) {
  return (
    <tr>
      <th scope="row">{label}</th>
      <td>{row.assessed}</td>
      <td>{row.applied}</td>
      <td>{row.vetoed}</td>
      <td>{row.undone}</td>
      <td>{row.needs_human}</td>
      <td>{row.awaiting}</td>
      <td>{row.unclassified}</td>
      <td title={`${row.agreed} agreed, ${row.disagreed} disagreed`}>
        {percent(row.agreement_rate)}
      </td>
    </tr>
  );
}

function DigestBody({ digest }: { digest: StewardDigest }) {
  if (digest.total.assessed === 0) {
    return (
      <p className="steward-digest__note">
        The steward assessed nothing in this period.
      </p>
    );
  }
  return (
    <>
      <table className="steward-digest__table">
        <thead>
          <tr>
            <th scope="col">Category</th>
            <th scope="col">Assessed</th>
            <th scope="col">Applied</th>
            <th scope="col">Vetoed</th>
            <th scope="col">Undone</th>
            <th scope="col">Needs human</th>
            <th scope="col">Awaiting you</th>
            <th scope="col">Unclassified</th>
            <th scope="col">Agreement</th>
          </tr>
        </thead>
        <tbody>
          {digest.categories.map((c) => (
            <Row key={c.category} row={c} label={c.category} />
          ))}
          <Row row={digest.total} label="All" />
        </tbody>
      </table>
      <p className="steward-digest__note">
        Agreement is how often your decision matched what the steward would have
        done, over the proposals you have decided. A veto or an undo counts as a
        disagreement.
      </p>
      <ul className="steward-digest__decisions" aria-label="Decisions">
        {digest.decisions.map((d) => (
          <li key={d.proposal_id}>
            {d.ticket_id === null ? (
              "workspace"
            ) : (
              <TicketLink ticketId={d.ticket_id} from="steward">
                #{d.ticket_id}
              </TicketLink>
            )}{" "}
            {d.category}: {d.outcome.replace("_", " ")} (
            {new Date(d.at).toLocaleString()})
          </li>
        ))}
      </ul>
      {digest.decisions_cut && (
        <p className="steward-digest__note">
          Showing the newest {digest.decisions.length} decisions.
        </p>
      )}
    </>
  );
}

// StewardDigestPage is the daily digest, kept as a page rather than sent: the
// board has no email channel for it. It reads the same on demand for the last
// day or week.
export default function StewardDigestPage() {
  const [period, setPeriod] = useState<StewardDigestWindow>("24h");
  const state = usePolledResource(() => apiClient.getStewardDigest(period), {
    deps: [period],
    fallbackError: "Failed to load the steward digest.",
  });
  return (
    <div className="steward-digest">
      <p className="steward-digest__intro">
        What the steward did for you, per category.
      </p>
      <div className="steward-digest__windows" role="group" aria-label="Period">
        {WINDOWS.map((w) => (
          <Button
            key={w.window}
            variant={period === w.window ? "primary" : "secondary"}
            aria-pressed={period === w.window}
            onClick={() => setPeriod(w.window)}
          >
            {w.label}
          </Button>
        ))}
      </div>
      <ResourceView state={state} loading="Loading the steward digest…">
        {(digest) => (
          <>
            <DigestBody digest={digest} />
            <StewardDiscussionsSection discussions={digest.discussions} />
          </>
        )}
      </ResourceView>
    </div>
  );
}
