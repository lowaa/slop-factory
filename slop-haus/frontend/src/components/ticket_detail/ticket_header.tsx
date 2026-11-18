import type { Ticket, TicketAttempt } from "@/api";
import { RichTextToggle } from "@/shared/rich_text/rich_text";
import type { RenderMarkdown } from "@/lib/markdown_setting";
import Timestamp from "@/shared/timestamp/timestamp";
import PrLink, { commitUrl } from "@/shared/pr_link/pr_link";
import AttemptChip from "./attempt_chip";

// The main commit the ticket's work started from, linked to the code host
// where the PR's link says the repository is.
// A ref the ticket was filed with (a tag, or the hash as typed) is named
// before the resolved hash, as that is what the filer meant.
function BaseCommit({
  sha,
  baseRef,
  prUrl,
}: {
  sha: string;
  baseRef: string | null;
  prUrl: string | null;
}) {
  const short = <code title={sha}>{sha.slice(0, 7)}</code>;
  const href = commitUrl(prUrl, sha);
  const commit = href ? <PrLink url={href}>{short}</PrLink> : short;
  if (!baseRef || sha.startsWith(baseRef)) return commit;
  return (
    <>
      <code>{baseRef}</code> ({commit})
    </>
  );
}

interface TicketHeaderProps {
  ticket: Ticket;
  // The attempt on show, if any. The header is the ticket's, and on an
  // attempt's page its fields would read as the attempt's: say whose they are.
  shown: TicketAttempt | undefined;
  markdown: RenderMarkdown;
}

export default function TicketHeader({
  ticket,
  shown,
  markdown,
}: TicketHeaderProps) {
  return (
    <header className="ticket-detail__header">
      <h1>
        #{ticket.id} {ticket.title}
        {shown && <AttemptChip attempt={shown} />}
      </h1>
      <RichTextToggle markdown={markdown} />
      <dl className="ticket-detail__timestamps">
        <div>
          <dt>Created</dt>
          <dd>
            <Timestamp at={ticket.created_at} />
          </dd>
        </div>
        <div>
          <dt>Updated</dt>
          <dd>
            <Timestamp at={ticket.updated_at} />
          </dd>
        </div>
        {ticket.closed_at && (
          <div>
            <dt>{shown ? "Ticket closed" : "Closed"}</dt>
            <dd>
              <Timestamp at={ticket.closed_at} />
            </dd>
          </div>
        )}
        {ticket.pr_url && (
          <div>
            <dt>{shown ? "Ticket PR" : "PR"}</dt>
            <dd>
              <PrLink url={ticket.pr_url} />
            </dd>
          </div>
        )}
        {ticket.base_commit && (
          <div>
            <dt>{shown ? "Ticket base" : "Base"}</dt>
            <dd>
              <BaseCommit
                sha={ticket.base_commit}
                baseRef={ticket.base_ref}
                prUrl={ticket.pr_url}
              />
            </dd>
          </div>
        )}
      </dl>
    </header>
  );
}
