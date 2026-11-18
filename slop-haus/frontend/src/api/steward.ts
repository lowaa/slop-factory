import {
  client,
  unwrap,
} from "./client";
import type { DiscussionDecision, DiscussionDraft, DiscussionRound, Proposal, StewardDigest, StewardDigestWindow, StewardNotices } from "./types";

export const stewardApi = {
  // What the steward did over the window, per category, with how often the
  // owner decided as it would have.
  async getStewardDigest(window: StewardDigestWindow): Promise<StewardDigest> {
    return await unwrap(client.GET("/api/steward/digest", { params: { query: { window } } }));
  },

  // What it will apply and has applied lately, for the owner to veto or undo.
  async getStewardNotices(): Promise<StewardNotices> {
    return await unwrap(client.GET("/api/steward/notices"));
  },

  // Owner only: cancels a decision the steward has not applied yet.
  async vetoStewardDecision(proposalId: string): Promise<Proposal> {
    return await unwrap(client.POST("/api/proposals/{id}/steward/veto", { params: { path: { id: proposalId } } }));
  },

  // Owner only: reverts what the steward applied.
  async undoStewardDecision(proposalId: string): Promise<Proposal> {
    return await unwrap(client.POST("/api/proposals/{id}/steward/undo", { params: { path: { id: proposalId } } }));
  },

  // Owner only: approves the proposal with the steward's draft, after the
  // server checks it again against what it reads now.
  async applyStewardDraft(proposalId: string): Promise<Proposal> {
    return await unwrap(client.POST("/api/proposals/{id}/steward/apply", { params: { path: { id: proposalId } } }));
  },

  // Owner only: the steward's label and templated draft for one discussion round.
  async getDiscussionDraft(r: DiscussionRound): Promise<DiscussionDraft> {
    return await unwrap(client.GET("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward", { params: { path: r } }));
  },

  // Owner only: runs the classification of a not-assessed round now.
  async assessDiscussionNow(r: DiscussionRound): Promise<DiscussionDraft> {
    return await unwrap(client.POST("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward/assess", { params: { path: r } }));
  },

  // Owner only: posts exactly the category's template as the owner's answer.
  async approveDiscussionDraft(r: DiscussionRound): Promise<DiscussionDecision> {
    return await unwrap(client.POST("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward/approve", { params: { path: r } }));
  },

  // Owner only: posts the owner's own text instead of the draft.
  async editDiscussionDraft(r: DiscussionRound, text: string): Promise<DiscussionDecision> {
    return await unwrap(client.POST("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward/edit", { params: { path: r }, body: { text } }));
  },

  // Owner only: dismisses the draft; nothing is posted.
  async rejectDiscussionDraft(r: DiscussionRound, reason: string): Promise<DiscussionDecision> {
    return await unwrap(client.POST("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward/reject", { params: { path: r }, body: { reason } }));
  },

  // Owner only: stops the steward posting a scheduled answer inside its window.
  async vetoDiscussionApply(r: DiscussionRound): Promise<DiscussionDecision> {
    return await unwrap(client.POST("/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward/veto", { params: { path: r } }));
  },
};
