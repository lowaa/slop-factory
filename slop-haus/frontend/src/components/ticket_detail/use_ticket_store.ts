import { useEffect, useRef, useState } from "react";
import {
  apiClient,
  type AgentContext,
  type Comment,
  type Priority,
  type Revision,
  type Status,
  type Ticket,
} from "@/api";
import { ticketTopic, liveEvents } from "@/lib/live";
import { invalidate, mutateResource, resourceKeys, updateResource, useResource } from "@/lib/resource";
import { useResourceClient } from "@/lib/resource/client";
import { errorMessage, type AsyncAction } from "@/lib/use_async_action";

export type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; ticket: Ticket; comments: Comment[]; contexts: AgentContext[]; revisions: Revision[] };

export type LoadedState = Extract<LoadState, { status: "loaded" }>;

export type PatchInput =
  | { status: Status }
  | { priority: Priority }
  | { needs_discussion: boolean }
  | { requires_approval: boolean };

// timestampsFrom is what every partial merge of a server ticket carries
// along besides the fields it was for. Responses can land out of order, so
// updated_at only ever moves forward -- an older reply must not make the
// ticket look less recently touched than it is. First claim (a move back to
// the queue grants it, to preparing or parked drops it), closed_at, pr_url
// and the review outcome are the server's account of the status -- what
// closed it and when, what is under review and how the review went (moving
// a ticket into review clears the outcome) -- so they travel with the
// status and only when the status itself is being merged. So does the
// post-merge review, which taking the ticket out of done clears, and the
// moves the status allows. So do the attempts: an approval, a request for
// changes or an answered discussion moves one attempt's status with it.
export function timestampsFrom(prev: Ticket, next: Ticket, statusMerged: boolean): Partial<Ticket> {
  const later = Date.parse(next.updated_at) > Date.parse(prev.updated_at) ? next.updated_at : prev.updated_at;
  return statusMerged
    ? {
        updated_at: later,
        closed_at: next.closed_at,
        affinity_until: next.affinity_until,
        resume_agent_id: next.resume_agent_id,
        pr_url: next.pr_url,
        approved_at: next.approved_at,
        approved_by_user_id: next.approved_by_user_id,
        approved_by_user_id_deleted: next.approved_by_user_id_deleted,
        changes_requested_at: next.changes_requested_at,
        post_reviewed_at: next.post_reviewed_at,
        post_reviewed_by_user_id: next.post_reviewed_by_user_id,
        post_reviewed_by_user_id_deleted: next.post_reviewed_by_user_id_deleted,
        allowed_moves: next.allowed_moves,
        can_ready: next.can_ready,
        attempts: next.attempts,
        // Any status write ends a release cooldown, even when the ticket
        // stays blocked by its dependencies.
        blocked_until: next.blocked_until,
        cooldown_releases: next.cooldown_releases,
        cooldown_since: next.cooldown_since,
      }
    : { updated_at: later };
}

// TicketStore is the one ticket on the page and every write to it. Each
// section of the page sends its own requests, but they all merge into this
// one state, and they all take turns on the same per-field generation
// counters -- that shared bookkeeping is why the merging lives here rather
// than in the sections.
export interface TicketStore {
  state: LoadState;
  // Applies an update only while a ticket is loaded: a reply that lands
  // after the page has moved to an error has nothing to merge into.
  updateLoaded: (update: (prev: LoadedState) => LoadedState) => void;
  // Starts a write of a field, returning its generation; a write's reply is
  // merged only while isCurrent says no later write of that field started.
  beginFieldWrite: (field: string) => number;
  isCurrent: (field: string, generation: number) => boolean;
  // The field's current generation, for a request that doesn't write the
  // field but must not overwrite a write of it made while it was in flight.
  generationOf: (field: string) => number;
  patchField: (input: PatchInput) => void;
  statusButton: (action: AsyncAction, move: () => Promise<Ticket>, failure: string) => Promise<void>;
  applyStatusWrite: (updated: Ticket, generation: number) => void;
  // Best-effort: whatever prompted the refetch already landed, and a failed
  // one only leaves the list behind until the next load.
  refreshComments: () => Promise<void>;
}

export function useTicketStore(ticketId: number): TicketStore {
  const client = useResourceClient();
  // One counter per written field -- see patchTicket. Writes in flight are
  // kept beside it, with the value each is showing, so a refetch of the
  // ticket cannot take that value off the screen before the reply is in.
  const patchGenerations = useRef<Record<string, number>>({});
  const pendingWrites = useRef<Record<string, { generation: number; value: unknown }>>({});
  const [failure, setFailure] = useState<{ ticketId: number; message: string } | null>(null);
  // The ticket this store is on show for, or null once the page is gone. The
  // cache outlives the page, so a reply to a write made by a page that has
  // since moved on or unmounted must not be merged into it.
  const shownId = useRef<number | null>(null);
  useEffect(() => {
    shownId.current = ticketId;
    return () => {
      shownId.current = null;
    };
  }, [ticketId]);
  const trackedId = useRef(ticketId);
  if (trackedId.current !== ticketId) {
    trackedId.current = ticketId;
    // The generations carry over deliberately: a reply for a write made on
    // an earlier ticket must still find itself superseded by a later one.
    pendingWrites.current = {};
  }

  const keys = {
    ticket: resourceKeys.ticket(ticketId),
    comments: resourceKeys.ticketComments(ticketId),
    contexts: resourceKeys.ticketContexts(ticketId),
    revisions: resourceKeys.ticketRevisions(ticketId),
  };
  // The page reads the ticket afresh each time it opens: nothing else shares
  // these keys, and a stale copy left in the cache would be shown as current.
  const own = { keepUnused: false, fallbackError: "Failed to load ticket." };
  const ticket = useResource(
    keys.ticket,
    async () => {
      const fresh = await apiClient.getTicket(ticketId);
      const current = client.getQueryData<Ticket>(keys.ticket);
      const held = Object.entries(pendingWrites.current);
      if (!current || held.length === 0) return fresh;
      return { ...fresh, ...Object.fromEntries(held.map(([field, { value }]) => [field, value])) };
    },
    own,
  );
  const comments = useResource(keys.comments, () => apiClient.listComments(ticketId), own);
  const contexts = useResource(keys.contexts, () => apiClient.listAgentContexts(ticketId), own);
  const revisions = useResource(keys.revisions, () => apiClient.listRevisions(ticketId), own);

  // Live events for this ticket make its keys stale; the refetch lands in the
  // cache like any other, and a field being written is held (see above).
  useEffect(
    () =>
      liveEvents.subscribe(ticketTopic(ticketId), (event) => {
        if (event.kind !== "pause") void invalidate(resourceKeys.ticket(ticketId), client, { restart: true });
      }),
    [ticketId, client],
  );

  let state: LoadState;
  if (failure && failure.ticketId === ticketId) state = { status: "error", message: failure.message };
  else if (ticket.data && comments.data && contexts.data && revisions.data) {
    state = { status: "loaded", ticket: ticket.data, comments: comments.data, contexts: contexts.data, revisions: revisions.data };
  } else {
    const broken = [ticket, comments, contexts, revisions].find((r) => r.status === "error");
    state = broken ? { status: "error", message: broken.error ?? "Failed to load ticket." } : { status: "loading" };
  }

  // Reads and writes the four keys together, synchronously, so an update is
  // applied to the cache as it is now.
  function updateLoaded(update: (prev: LoadedState) => LoadedState) {
    if (shownId.current !== ticketId) return;
    const current = {
      ticket: client.getQueryData<Ticket>(keys.ticket),
      comments: client.getQueryData<Comment[]>(keys.comments),
      contexts: client.getQueryData<AgentContext[]>(keys.contexts),
      revisions: client.getQueryData<Revision[]>(keys.revisions),
    };
    if (!current.ticket || !current.comments || !current.contexts || !current.revisions) return;
    const prev: LoadedState = { status: "loaded", ticket: current.ticket, comments: current.comments, contexts: current.contexts, revisions: current.revisions };
    const next = update(prev);
    if (next.ticket !== prev.ticket) updateResource(keys.ticket, () => next.ticket, client);
    if (next.comments !== prev.comments) updateResource(keys.comments, () => next.comments, client);
    if (next.contexts !== prev.contexts) updateResource(keys.contexts, () => next.contexts, client);
    if (next.revisions !== prev.revisions) updateResource(keys.revisions, () => next.revisions, client);
  }

  function beginFieldWrite(field: string): number {
    const generation = (patchGenerations.current[field] ?? 0) + 1;
    patchGenerations.current[field] = generation;
    return generation;
  }

  function isCurrent(field: string, generation: number): boolean {
    return patchGenerations.current[field] === generation;
  }

  function generationOf(field: string): number {
    return patchGenerations.current[field] ?? 0;
  }

  // A field-level PATCH (not the whole form) each time status or priority
  // changes -- server/internal/ticket/service.go's UpdateParams already
  // treats an omitted field as "leave alone", so there's no reason to
  // batch these behind a separate save button the way a form would.
  //
  // It throws rather than handling the failure, so the caller decides what
  // a failure costs.
  async function patchTicket(input: PatchInput) {
    // Two rules, because two quick edits can leave two PATCHes in flight
    // and the responses can land in either order.
    //
    // A response is merged field by field, never wholesale: it carries a
    // whole ticket as of *its own* write, so applying all of it lets an
    // earlier PATCH put back a field a later one has already changed.
    //
    // And a field is only merged if this request is still the newest one
    // for it, so two changes to the *same* field settle on the later one
    // rather than on whichever reply happens to arrive last.
    const fields = Object.keys(input);
    const generations = fields.map(beginFieldWrite);
    fields.forEach((field, i) => {
      pendingWrites.current[field] = { generation: generations[i], value: input[field as keyof PatchInput] };
    });
    const settle = () =>
      fields.forEach((field, i) => {
        if (pendingWrites.current[field]?.generation === generations[i]) delete pendingWrites.current[field];
      });

    try {
      await mutateResource<Ticket, Ticket>(
        keys.ticket,
        {
          optimistic: (current) => ({ ...current, ...input }),
          request: () => apiClient.updateTicket(ticketId, input),
          // Merged field by field -- see above.
          commit: (current, reply) => {
            if (shownId.current !== ticketId) return current;
            const changed = Object.fromEntries(
              fields.filter((field, i) => isCurrent(field, generations[i])).map((field) => [field, reply[field as keyof Ticket]]),
            ) as Partial<Ticket>;
            return { ...current, ...changed, ...timestampsFrom(current, reply, "status" in changed) };
          },
          // Only the fields this write still owns go back; a later write of
          // the same field has already replaced what this one showed.
          rollback: (current, previous) => {
            if (shownId.current !== ticketId) return current;
            const restored = Object.fromEntries(
              fields.filter((field, i) => isCurrent(field, generations[i])).map((field) => [field, previous[field as keyof Ticket]]),
            );
            return { ...current, ...restored };
          },
        },
        client,
      );
    } catch (err) {
      settle();
      // A failure nobody is waiting on any more is not news. The value on
      // screen came from a later request for the same field, so reporting
      // this one would replace a correct view with an error about a value
      // that has already been overwritten.
      if (fields.every((field, i) => !isCurrent(field, generations[i]))) return;
      throw err;
    }
    settle();
  }

  // Showing a status the server rejected would be a lie, and there is
  // nothing typed to preserve, so a failure here replaces the view.
  function patchField(input: PatchInput) {
    patchTicket(input).catch((err: unknown) => {
      setFailure({ ticketId, message: errorMessage(err, "Failed to update ticket.") });
    });
  }

  // applyStatusWrite merges only the fields a status move can change, the
  // same way the post-comment refresh merges only what a human comment can
  // change: the rest of the ticket may have been edited while the request
  // was in flight, and a whole-ticket overwrite would put an older value
  // back.
  function applyStatusWrite(updated: Ticket, generation: number) {
    if (!isCurrent("status", generation)) return;
    updateLoaded((prev) => ({
      ...prev,
      ticket: { ...prev.ticket, status: updated.status, ...timestampsFrom(prev.ticket, updated, true) },
    }));
  }

  // statusButton runs a button -- Ready, Approve, Park, Unpark -- that has
  // nothing to do but the move itself. The move is a status write, so it
  // takes a turn on the status field's generation counter like a PATCH
  // does: a status chosen in the dropdown after the click must not be
  // overwritten by the button's reply arriving later. A refusal is reported
  // beside the button rather than replacing the view: the ticket on screen
  // is still the right one.
  async function statusButton(action: AsyncAction, move: () => Promise<Ticket>, failure: string) {
    if (state.status !== "loaded") return;
    let generation = 0;
    await action.run(
      async () => {
        generation = beginFieldWrite("status");
        applyStatusWrite(await move(), generation);
      },
      // A refusal of a move a later status write has overtaken is not
      // news -- the ticket is no longer where this button left it.
      (err) => (isCurrent("status", generation) ? errorMessage(err, failure) : null),
    );
  }

  async function refreshComments() {
    await invalidate(keys.comments, client);
  }

  return {
    state,
    updateLoaded,
    beginFieldWrite,
    isCurrent,
    generationOf,
    patchField,
    statusButton,
    applyStatusWrite,
    refreshComments,
  };
}
