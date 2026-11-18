# Ticket status history and flow metrics

Every change of a ticket's status is a row of `ticket_status_changes`:
tenant, ticket, `from_status` (null for the row recorded
at creation), `to_status`, the ticket's `priority` when it entered
`to_status`, `changed_at`, the actor (`agent` with its id, `user` with theirs
where the route knows it, or `system` for an automatic move) and a `cause`
where one is known (`created`, `claimed`, `blocked`, `unblocked`,
`lease_expired`, or the lease's release reason: `discussion`, `review`,
`done`, an agent's own words...).

A trigger on `tickets` writes it, in the transaction of the change, so a
change that rolls back leaves no row and no path can skip it. The code that
writes a status says who and why first, in transaction-local settings
(`status_mover.go`); a path that says nothing is recorded as `system`.

How long a ticket spent in a state is the gap to its next row; a ticket's
current state is the one with no next row.

There is no retention. A row is about 100 bytes and a ticket makes a handful,
and the time-in-state histogram is a running total over every row, so
pruning would shrink it. Rows go with their ticket (`ON DELETE CASCADE`);
slop-haus has no tenant deletion to add them to.

## Metrics

`BoardCollector` (`board_collector.go`) reads the table at scrape time and
sums over every tenant, on the metrics listener (`:9090`):

| metric | labels |
|---|---|
| `slophaus_tickets` (gauge) | `status`, `priority` |
| `slophaus_ticket_state_duration_seconds` (histogram) | `state`, `priority` |
| `slophaus_ticket_lead_time_seconds` (histogram) | `priority` |

No label names a tenant or a ticket, and nothing tells tenants apart: if that
is ever needed, add an opaque per-tenant id, never a name or a title.
They show data only where the env's `telemetry` value is on (it renders the
scrape).

## Before the table: approximating from what was kept

History from before status changes were recorded has no rows, and none are invented. What
can be said of it comes from `tickets` (`created_at`, `closed_at`) and
`leases` (every period an agent held a ticket, with its release reason).
Leases are pruned after 30 days (`prune.DefaultLeaseRetention`), so anything
from leases covers the last month at most; the first query does not need them.

They are tenant tables under row-level security, so run them as the app role
with `app.current_tenant` set to the tenant (as the stores' transactions do), or
as the owner, which sees every tenant at once.

Lead time, created to done, in hours (the p50 and p90 of finished tickets):

```sql
SELECT count(*) AS done,
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (
           ORDER BY extract(epoch FROM closed_at - created_at) / 3600) AS p50_p90_hours
  FROM tickets
 WHERE status = 'done' AND closed_at IS NOT NULL;
```

Created to first claimed, and first claimed to done, in hours:

```sql
WITH first_claim AS (
    SELECT ticket_id, min(acquired_at) AS at FROM leases GROUP BY ticket_id
)
SELECT percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (
           ORDER BY extract(epoch FROM c.at - t.created_at) / 3600) AS waiting_for_claim_p50_p90_hours,
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (
           ORDER BY extract(epoch FROM t.closed_at - c.at) / 3600) AS claimed_to_done_p50_p90_hours
  FROM tickets t JOIN first_claim c ON c.ticket_id = t.id
 WHERE t.status = 'done' AND t.closed_at IS NOT NULL AND c.at <= t.closed_at;
```

Time an agent held a ticket, per ticket, in hours:

```sql
SELECT percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (ORDER BY held / 3600) AS held_p50_p90_hours
  FROM (
    SELECT ticket_id, sum(extract(epoch FROM released_at - acquired_at)) AS held
      FROM leases WHERE released_at IS NOT NULL GROUP BY ticket_id
  ) per_ticket;
```

Time waiting on a person, in hours: from a lease that ended in discussion or
review to the ticket's next lease. A ticket never claimed again has no
answer yet, so it is left out, as is one whose answer came with the lease
pruned.

```sql
SELECT release_reason AS state,
       percentile_cont(ARRAY[0.5, 0.9]) WITHIN GROUP (ORDER BY waited / 3600) AS p50_p90_hours
  FROM (
    SELECT release_reason,
           extract(epoch FROM lead(acquired_at) OVER (PARTITION BY ticket_id ORDER BY id) - released_at) AS waited
      FROM leases
  ) gaps
 WHERE release_reason IN ('discussion', 'review') AND waited IS NOT NULL
 GROUP BY release_reason;
```

The time in preparing, open and blocked before status changes were recorded cannot be separated:
nothing recorded when a ticket was marked ready or unblocked.
