import type { ReactNode, Ref } from "react";
import { Link } from "@slop-factory/router";
import { useAuth } from "@/context/auth";
import type { TabName } from "@/tabs";

interface TicketLinkProps {
  ticketId: number;
  // The tab the link is on: where the ticket's Back link returns to.
  from: TabName;
  // The attempt's tab to open rather than the ticket's own view.
  attempt?: number;
  className?: string;
  // For a caller that opens the ticket some other way than a click on the
  // link (ticket_jump's Enter): clicking it keeps this the one place a
  // ticket's address is made.
  ref?: Ref<HTMLAnchorElement>;
  children: ReactNode;
}

// TicketLink is how every ticket is opened: a real link, so middle-click
// and copy-link work. It always names the session's tenant, because a
// ticket's number is only unique within one -- a copied link opened by
// someone working in another tenant must not show them that tenant's
// ticket of the same number.
export default function TicketLink({ ticketId, from, attempt, className, ref, children }: TicketLinkProps) {
  const { profile } = useAuth();
  const search = { from, tenant: profile?.tenant_id, view: undefined };
  if (attempt !== undefined) {
    return (
      <Link to="/tickets/$ticketId/attempts/$n" params={{ ticketId, n: attempt }} search={search} className={className} ref={ref}>
        {children}
      </Link>
    );
  }
  return (
    <Link
      to="/tickets/$ticketId"
      params={{ ticketId }}
      // Always on the ticket's main tab: a link from a list is to the ticket.
      search={search}
      className={className}
      ref={ref}
    >
      {children}
    </Link>
  );
}
