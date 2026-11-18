import {
  client,
  unwrap,
} from "./client";
import type {
  BatchProposalKind,
  DecidedProposalState,
  Proposal,
  ProposalPage,
  ProposalResult,
  ProposalState,
  ProposalTicketFilter,
} from "./types";

export const proposalsApi = {
  // -- Console proposals --
  //
  // A person's alone: no agent token reaches these routes, which is what
  // makes approving mean something.

  // The newest first page of at most 100, which is all the shared pending
  // list (the tab's badge, a ticket's strip) reads.
  async listProposals(
    state?: ProposalState,
    filter: ProposalTicketFilter = {},
  ): Promise<Proposal[]> {
    return (
      await unwrap(
        client.GET("/api/proposals", {
          params: { query: { ...filter, state, limit: 100 } },
        }),
      )
    ).proposals;
  },

  // Pending proposals, oldest first: the order to work through them in.
  async listPendingProposalsPage(
    filter: ProposalTicketFilter,
    page: { limit?: number; cursor?: string } = {},
  ): Promise<ProposalPage> {
    const body = await unwrap(
      client.GET("/api/proposals", {
        params: { query: { ...filter, state: "pending", order: "oldest", ...page } },
      }),
    );
    return { proposals: body.proposals, nextCursor: body.next_cursor };
  },

  // Proposals in one decided state, newest first: what has happened to them,
  // not what is left to do. Never "pending" -- that is the page above's.
  async listDecidedProposalsPage(
    filter: ProposalTicketFilter,
    state: DecidedProposalState,
    page: { limit?: number; cursor?: string } = {},
  ): Promise<ProposalPage> {
    const body = await unwrap(
      client.GET("/api/proposals", {
        params: { query: { ...filter, state, order: "newest", ...page } },
      }),
    );
    return { proposals: body.proposals, nextCursor: body.next_cursor };
  },

  async approveProposal(id: string): Promise<Proposal> {
    return await unwrap(
      client.POST("/api/proposals/{id}/approve", { params: { path: { id } } }),
    );
  },

  async rejectProposal(id: string, reason: string): Promise<Proposal> {
    return await unwrap(
      client.POST("/api/proposals/{id}/reject", {
        params: { path: { id } },
        body: { reason },
      }),
    );
  },

  async approveAllProposals(
    kind: BatchProposalKind,
    ids?: string[],
  ): Promise<ProposalResult[]> {
    return (
      await unwrap(
        client.POST("/api/proposals/approve_all", { body: { kind, ids } }),
      )
    ).results;
  },

  // ids limits it to those proposals; left out, it is every pending one.
  async rejectAllProposals(
    reason: string,
    ids?: string[],
  ): Promise<ProposalResult[]> {
    return (
      await unwrap(
        client.POST("/api/proposals/reject_all", { body: { reason, ids } }),
      )
    ).results;
  },
};
