import { useCallback } from "react";
import { Link, getRouteApi } from "@slop-factory/router";
import { useAuth } from "@/context/auth";
import { BackLink } from "@/components/back_link";
import { TenantMismatch } from "@/components/tenant_mismatch";
import { tabNamed } from "@/tabs";
import type { AttemptLink } from "./attempt_view";
import DiscussShortcut from "./discuss_shortcut";
import TicketDetail from "./ticket_detail";
import type { TicketView } from "./ticket_view";

// By id rather than importing the routes from routes.tsx, which imports
// this feature.
const ticketRouteApi = getRouteApi("/signed-in/tickets/$ticketId");

const ticketAttemptRouteApi = getRouteApi("/signed-in/tickets/$ticketId/attempts/$n");

export function TicketRoute() {
  const { ticketId } = ticketRouteApi.useParams();
  const { from, tenant, view } = ticketRouteApi.useSearch();
  const navigate = ticketRouteApi.useNavigate();
  return (
    <TicketPage
      ticketId={ticketId}
      attempt={undefined}
      search={{ from, tenant, view }}
      onViewChange={(next) =>
        navigate({ search: (prev) => ({ ...prev, view: next === "main" ? undefined : next }) })
      }
    />
  );
}

export function TicketAttemptRoute() {
  const { ticketId, n } = ticketAttemptRouteApi.useParams();
  const { from, tenant, view } = ticketAttemptRouteApi.useSearch();
  const navigate = ticketAttemptRouteApi.useNavigate();
  return (
    <TicketPage
      ticketId={ticketId}
      attempt={n}
      search={{ from, tenant, view }}
      onViewChange={(next) =>
        navigate({ search: (prev) => ({ ...prev, view: next === "main" ? undefined : next }) })
      }
    />
  );
}

// TicketPage opens a ticket only in the tenant its link names. A link
// without ?tenant= (typed by hand) means the session's own. The address of
// an attempt's tab is the ticket's with /attempts/N; the ticket's own tabs
// (?view=) stay on the ticket's address.
function TicketPage({
  ticketId,
  attempt,
  search: { from, tenant, view },
  onViewChange,
}: {
  ticketId: number;
  attempt: number | undefined;
  search: { from: ReturnType<typeof ticketRouteApi.useSearch>["from"]; tenant?: string; view?: TicketView };
  onViewChange: (view: TicketView) => void;
}) {
  const { profile } = useAuth();
  const back = <BackLink tab={tabNamed(from)} />;
  const attemptLink = useCallback<AttemptLink>(
    (target, { children, ...props }) =>
      target === null ? (
        <Link to="/tickets/$ticketId" params={{ ticketId }} search={{ from, tenant, view: undefined }} {...props}>
          {children}
        </Link>
      ) : (
        <Link
          to="/tickets/$ticketId/attempts/$n"
          params={{ ticketId, n: target }}
          search={{ from, tenant, view: undefined }}
          {...props}
        >
          {children}
        </Link>
      ),
    [ticketId, from, tenant],
  );

  if (tenant && tenant !== profile?.tenant_id) {
    return (
      <TenantMismatch
        key={`${tenant}/${ticketId}`}
        ticketId={ticketId}
        tenantId={tenant}
        back={back}
      />
    );
  }
  // Keyed on the id so moving from one ticket to another starts afresh
  // rather than showing the last one's state while the next loads.
  //
  // A tab change is a navigation of its own, so Back returns to the tab
  // before it rather than leaving the ticket.
  return (
    <TicketDetail
      key={ticketId}
      ticketId={ticketId}
      back={back}
      discuss={<DiscussShortcut ticketId={ticketId} />}
      view={view ?? "main"}
      attempt={attempt}
      attemptLink={attemptLink}
      onViewChange={onViewChange}
    />
  );
}
