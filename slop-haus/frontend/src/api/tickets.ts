import type { components } from "@/api_generated/schema";
import {
  client,
  unwrap,
} from "./client";
import type {
  Priority,
  RoleMatch,
  Status,
  AgentContext,
  BlockedTicketsQuery,
  BlockerCandidate,
  BlockerList,
  BlockingGraph,
  Comment,
  CreateTicketInput,
  EditTicketInput,
  ExternalEvent,
  ListTicketsFilter,
  Revision,
  ReviewRun,
  StatusChange,
  Ticket,
  TicketPage,
  UpdateTicketInput,
  VariantOption,
  AttemptVerdict,
  AttemptVerdictValue,
  MergeFacts,
  CloseAsMergedResult,
} from "./types";

export type ReplayBatchLimits = components["schemas"]["ReplayBatchLimitsResponseBody"];
export type ReplayBatchResult = components["schemas"]["ReplayBatchResponseBody"];

function ticketsQuery({
  statuses = [],
  awaitingPostReview = false,
  text = "",
  closedFrom,
  closedTo,
  cursor,
  sort,
  limit,
}: ListTicketsFilter = {}) {
  return {
    status: statuses.length ? statuses : undefined,
    awaiting_post_review: awaitingPostReview || undefined,
    q: text.trim() ? text : undefined,
    closed_from: closedFrom || undefined,
    closed_to: closedTo || undefined,
    cursor: cursor || undefined,
    sort: sort || undefined,
    limit: limit || undefined,
  };
}

export const ticketsApi = {
  // The collection endpoints wrap their array in an object -- a bare
  // top-level JSON array has nowhere to grow a cursor or a count without
  // breaking every existing client. Single-resource endpoints return the
  // object directly. The envelope is unwrapped here so callers only ever
  // see arrays.
  //
  // One page of the server's bounded ticket list, and where the next page
  // starts.
  async listTicketsPage(filter: ListTicketsFilter): Promise<TicketPage> {
    const body = await unwrap(
      client.GET("/api/tickets", { params: { query: ticketsQuery(filter) } }),
    );
    return { tickets: body.tickets, nextCursor: body.next_cursor };
  },

  // The first page of one board column. A work column (In progress,
  // Discussion, Review) is attempt cards here; GET /api/tickets lists its
  // tickets, and only continues a list the board began as cards (by the
  // cursor). filter names one status and carries no cursor.
  async listBoardColumn(filter: ListTicketsFilter): Promise<TicketPage> {
    const body = await unwrap(
      client.GET("/api/board", { params: { query: ticketsQuery(filter) } }),
    );
    const column = body.columns[0];
    return { tickets: column?.tickets ?? [], nextCursor: column?.next_cursor };
  },

  async createTicket(input: CreateTicketInput): Promise<Ticket> {
    return await unwrap(client.POST("/api/tickets", { body: input }));
  },

  async getTicket(id: number): Promise<Ticket> {
    return await unwrap(
      client.GET("/api/tickets/{id}", { params: { path: { id } } }),
    );
  },

  async updateTicket(id: number, input: UpdateTicketInput): Promise<Ticket> {
    return await unwrap(
      client.PATCH("/api/tickets/{id}", {
        params: { path: { id } },
        body: input,
      }),
    );
  },

  async editTicket(id: number, input: EditTicketInput): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/revisions", {
        params: { path: { id } },
        body: input,
      }),
    );
  },

  // setTicketRoles replaces a ticket's whole role requirement -- an empty
  // roleIds clears it, opening the ticket back up to any agent. Its own
  // route rather than a field on updateTicket's PATCH, because that PATCH's
  // "omitted means unchanged" convention has no way to say "clear" (mirrors
  // server/internal/ticket/handler.go's SetTicketRoles).
  async setTicketRoles(
    id: number,
    roleIds: string[],
    roleMatch: RoleMatch = "any",
  ): Promise<Ticket> {
    return await unwrap(
      client.PUT("/api/tickets/{id}/roles", {
        params: { path: { id } },
        body: { role_ids: roleIds, role_match: roleMatch },
      }),
    );
  },

  // setTicketVariant routes the ticket's attempt to a variant version, or
  // clears it with null. Refused (409) once the attempt has had a lease.
  async setTicketVariant(id: number, versionId: string | null): Promise<Ticket> {
    return await unwrap(
      client.PUT("/api/tickets/{id}/variant", {
        params: { path: { id } },
        body: { version_id: versionId },
      }),
    );
  },

  // setAttemptVerdict gives a finished attempt the signed-in person's
  // verdict, replacing their current one; the reply is the verdict with the
  // ones it replaced.
  async setAttemptVerdict(
    id: number,
    attempt: number,
    verdict: AttemptVerdictValue,
    note: string,
  ): Promise<AttemptVerdict> {
    return await unwrap(
      client.PUT("/api/tickets/{id}/attempts/{n}/verdict", {
        params: { path: { id, n: attempt } },
        body: { verdict, note },
      }),
    );
  },

  // setAttemptVariant is setTicketVariant for the numbered attempt.
  async setAttemptVariant(
    id: number,
    attempt: number,
    versionId: string | null,
  ): Promise<Ticket> {
    return await unwrap(
      client.PUT("/api/tickets/{id}/attempts/{n}/variant", {
        params: { path: { id, n: attempt } },
        body: { version_id: versionId },
      }),
    );
  },

  // addTicketAttempts adds one attempt for each row (a variant version id,
  // or null for none), all or none: the server refuses the whole batch if it
  // would pass its cap of live attempts. A 404 with code
  // feature_disabled means the server does not serve adding attempts at all;
  // any other 404 is a ticket or version that is not there. With replay the
  // rows are replays of a done ticket, which the server takes only for one.
  async addTicketAttempts(
    id: number,
    versionIds: (string | null)[],
    replay = false,
  ): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/attempts", {
        params: { path: { id } },
        body: {
          attempts: versionIds.map((v) => (v === null ? {} : { version_id: v })),
          ...(replay ? { replay } : {}),
        },
      }),
    );
  },

  // replayBatchLimits is what the replay panel shows before a batch starts.
  // A 404 with code feature_disabled means the server does not serve replays.
  async replayBatchLimits(): Promise<ReplayBatchLimits> {
    return await unwrap(client.GET("/api/replay_batches/limits"));
  },

  // addReplayBatch adds runs replays per variant (a version id, or null for
  // the default) to each done ticket. confirmAttempts is the number the
  // person confirmed; the server refuses (422) a batch it does not equal.
  // Tickets the server could not add to come back in refused, not as an error.
  async addReplayBatch(
    ticketIds: number[],
    versionIds: (string | null)[],
    runs: number,
    confirmAttempts: number,
  ): Promise<ReplayBatchResult> {
    return await unwrap(
      client.POST("/api/replay_batches", {
        body: { ticket_ids: ticketIds, version_ids: versionIds, runs, confirm_attempts: confirmAttempts },
      }),
    );
  },

  // closeTicketAttempt ends one unpromoted attempt: its agent is told to
  // stop and its open change is closed. A 404 with code
  // feature_disabled means the server does not serve it; any other 404 is a
  // ticket or attempt that is not there.
  async closeTicketAttempt(id: number, attempt: number): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/attempts/{n}/close", {
        params: { path: { id, n: attempt } },
      }),
    );
  },

  // promoteTicketAttempt makes one attempt the ticket's result. A 404 with
  // code feature_disabled means the server does not serve it; any other 404
  // is a ticket or attempt that is not there.
  async promoteTicketAttempt(id: number, attempt: number): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/attempts/{n}/promote", {
        params: { path: { id, n: attempt } },
      }),
    );
  },

  // listVariantOptions is the unarchived variant versions, for any member.
  async listVariantOptions(): Promise<VariantOption[]> {
    return (await unwrap(client.GET("/api/variants/options"))).options;
  },

  // listRevisions returns a ticket's edits, oldest first.
  async listRevisions(ticketId: number): Promise<Revision[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/revisions", {
          params: { path: { id: ticketId } },
        }),
      )
    ).revisions;
  },

  // listAttemptReviewRuns returns the slop-review runs posted for one attempt,
  // newest first.
  async listAttemptReviewRuns(ticketId: number, attempt: number, limit?: number): Promise<ReviewRun[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/attempts/{n}/review-runs", {
          params: { path: { id: ticketId, n: attempt }, query: { limit } },
        }),
      )
    ).runs;
  },

  // listStatusHistory returns every status change of a ticket, oldest first.
  async listStatusHistory(ticketId: number): Promise<StatusChange[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/status_history", {
          params: { path: { id: ticketId } },
        }),
      )
    ).changes;
  },

  // markTicketReady is the one way out of Preparing: it lands the ticket on "open",
  // or "blocked" if its dependencies aren't finished yet, rather than
  // leaving a free-form status PATCH to reproduce that rule client-side.
  async markTicketReady(id: number): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/ready", { params: { path: { id } } }),
    );
  },

  // The bulk actions apply the single ones' rules to several tickets at
  // once, all or nothing: a refusal of any throws a BulkRefusedError naming
  // every refused ticket, and changes none. They resolve to the changed
  // tickets, in the order asked for.
  async bulkSetTicketStatus(ids: number[], status: Status): Promise<Ticket[]> {
    return (
      await unwrap(
        client.POST("/api/tickets/bulk/status", { body: { ids, status } }),
      )
    ).tickets;
  },

  async bulkMarkTicketsReady(ids: number[]): Promise<Ticket[]> {
    return (
      await unwrap(client.POST("/api/tickets/bulk/ready", { body: { ids } }))
    ).tickets;
  },

  async bulkSetTicketPriority(
    ids: number[],
    priority: Priority,
  ): Promise<Ticket[]> {
    return (
      await unwrap(
        client.POST("/api/tickets/bulk/priority", { body: { ids, priority } }),
      )
    ).tickets;
  },

  // approveTicket/requestTicketChanges are a human's two answers to a
  // ticket in "review" (an agent's PR waiting on them) -- both return it to
  // the queue, the same way markTicketReady leaves preparing, rather than a
  // free-form status PATCH reproducing that rule client-side. The server
  // refuses either with a 409 when the ticket isn't in review.
  //
  // attempt names the attempt answered: the server refuses to guess when
  // the ticket has several live ones, and leaves it out otherwise.
  async approveTicket(id: number, attempt?: number): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/approve", {
        params: { path: { id }, query: { attempt } },
      }),
    );
  },

  async requestTicketChanges(
    id: number,
    comment: string,
    attempt?: number,
  ): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/request_changes", {
        params: { path: { id }, query: { attempt } },
        body: { comment },
      }),
    );
  },

  // markTicketPostReviewed records that the signed-in person has looked
  // over a done ticket's merged change. The comment is optional; one that
  // is sent is posted as theirs on the ticket.
  async markTicketPostReviewed(id: number, comment = ""): Promise<Ticket> {
    return await unwrap(
      client.POST("/api/tickets/{id}/post_review", {
        params: { path: { id } },
        body: comment ? { comment } : {},
      }),
    );
  },

  // getTicketMergeFacts is owner-only: anyone else is refused with a 403.
  async getTicketMergeFacts(id: number): Promise<MergeFacts> {
    return await unwrap(
      client.GET("/api/tickets/{id}/merge_facts", { params: { path: { id } } }),
    );
  },

  // closeTicketAsMerged closes a ticket as done on the code host's word that
  // its change is merged. Without confirm, a head whose checks did not pass or
  // that was never approved comes back as needs_confirmation, closing nothing.
  async closeTicketAsMerged(id: number, confirm = false): Promise<CloseAsMergedResult> {
    return await unwrap(
      client.POST("/api/tickets/{id}/close_as_merged", {
        params: { path: { id } },
        body: confirm ? { confirm } : {},
      }),
    );
  },

  async listComments(ticketId: number): Promise<Comment[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/comments", {
          params: { path: { id: ticketId } },
        }),
      )
    ).comments;
  },

  // attempt addresses the comment to one attempt of a ticket with several:
  // only that attempt's agent is sent it, and it answers only that attempt's
  // discussion. Left out, the comment goes to every live attempt.
  async addComment(
    ticketId: number,
    body: string,
    attempt?: number,
  ): Promise<Comment> {
    return await unwrap(
      client.POST("/api/tickets/{id}/comments", {
        params: { path: { id: ticketId }, query: { attempt } },
        body: { body },
      }),
    );
  },

  // addCommentSettingNeedsDiscussion posts the comment and sets or clears
  // needs_discussion in one server transaction -- two requests would leave
  // an agent a window to claim the ticket between them. The ticket comes
  // back as that write left it: flagged, and requeued if the comment
  // answered a discussion or took the ticket from an agent working it.
  async addCommentSettingNeedsDiscussion(
    ticketId: number,
    body: string,
    needsDiscussion: boolean,
    attempt?: number,
  ): Promise<{ comment: Comment; ticket: Ticket }> {
    const { ticket, ...comment } = await unwrap(
      client.POST("/api/tickets/{id}/comments", {
        params: { path: { id: ticketId }, query: { attempt } },
        body: { body, needs_discussion: needsDiscussion },
      }),
    );
    // The server leaves the ticket out only when needs_discussion wasn't
    // sent, and this always sends it.
    if (!ticket)
      throw new Error(
        "the comment was posted, but the server did not return its ticket",
      );
    return { comment, ticket };
  },

  // listAgentContexts returns a ticket's handoffs, oldest first -- the
  // last one is the current one. Read-only: only an agent entering a
  // discussion writes one, which is what keeps a handoff trustworthy as
  // the agent's own account.
  async listAgentContexts(ticketId: number): Promise<AgentContext[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/contexts", {
          params: { path: { id: ticketId } },
        }),
      )
    ).contexts;
  },

  // One page of the blocked tickets, the ones holding up the most first. The
  // list is the server's to rank and page; a ticket's own graph is loaded
  // from getTicketBlockers when a person opens it.
  async listBlockedPage(query: BlockedTicketsQuery): Promise<TicketPage> {
    const body = await unwrap(client.GET("/api/tickets/blocked", { params: { query } }));
    return { tickets: body.tickets, nextCursor: body.next_cursor };
  },

  // The graph of one ticket and what it waits on, drawn when a row of the
  // Blocked page is opened. Returns the Graph object directly (not an array
  // envelope) -- same single-resource convention as getTicket above.
  async getTicketBlockers(ticketId: number): Promise<BlockingGraph> {
    return await unwrap(
      client.GET("/api/tickets/{id}/blockers", {
        params: { path: { id: ticketId } },
      }),
    );
  },

  // addBlocker/removeBlocker both return the ticket's resulting blocked_by
  // list (server/internal/ticket/graph_handler.go's blockerListResponse),
  // not the whole graph -- cheap enough to just refetch the graph after,
  // rather than trying to patch one node's derived fields (layer,
  // transitive_blockers, unblocks) by hand on the client.
  async getBlockerCandidates(ticketId: number): Promise<BlockerCandidate[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/blocker_candidates", {
          params: { path: { id: ticketId } },
        }),
      )
    ).tickets;
  },

  async addBlocker(ticketId: number, blockerId: number): Promise<BlockerList> {
    return await unwrap(
      client.POST("/api/tickets/{id}/blockers", {
        params: { path: { id: ticketId } },
        body: { blocker_id: blockerId },
      }),
    );
  },

  async removeBlocker(
    ticketId: number,
    blockerId: number,
  ): Promise<BlockerList> {
    return await unwrap(
      client.DELETE("/api/tickets/{id}/blockers/{blockerId}", {
        params: { path: { id: ticketId, blockerId } },
      }),
    );
  },

  async listTicketExternal(ticketId: number): Promise<ExternalEvent[]> {
    return (
      await unwrap(
        client.GET("/api/tickets/{id}/external", {
          params: { path: { id: ticketId } },
        }),
      )
    ).events;
  },
};
