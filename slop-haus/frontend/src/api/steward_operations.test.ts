import * as fx from "@/test/fixtures";
import { describeOperations, type Operation } from "./operations_harness";

const category = {
  category: "ci_green", assessed: 4, applied: 2, vetoed: 1, undone: 0, needs_human: 1, awaiting: 0, unclassified: 0, agreed: 3, disagreed: 1, agreement_rate: 0.75,
};
const p1 = fx.makeProposal({ id: "p1" });

const round = { ticket_id: 5, attempt: 1, round: 2 };
const draft = { body: "t", label: null };
const decision = { action: "approve", created_at: "2026-01-01T00:00:00Z", final_body: "t", original_body: "t", reason: "" };

const operations: Record<string, Operation> = {
  getStewardDigest: {
    call: (c) => c.getStewardDigest("7d"),
    method: "GET",
    url: "/api/steward/digest?window=7d",
    response: {
      since: "2026-01-01T00:00:00Z", until: "2026-01-08T00:00:00Z", total: category, categories: [category],
      decisions: [{ proposal_id: "p1", ticket_id: 5, category: "ci_green", outcome: "applied", at: "2026-01-07T00:00:00Z" }],
      decisions_cut: false,
      discussions: {
        labels: [], labels_total: { category: "all", decisions: 0, agreed: 0, disagreed: 0, agreement_rate: null, warn_few_decisions: true, warn_low_agreement: false },
        answers: [], answers_total: { category: "all", decisions: 0, agreed: 0, disagreed: 0, agreement_rate: null, warn_few_decisions: true, warn_low_agreement: false },
        label_disagreements: [], answer_disagreements: [],
      },
    },
  },
  getStewardNotices: {
    call: (c) => c.getStewardNotices(),
    method: "GET",
    url: "/api/steward/notices",
    response: { mode: "live", pending: [], applied: [], cap_reached: false, daily_cap: 50 },
  },
  vetoStewardDecision: {
    call: (c) => c.vetoStewardDecision("p1"),
    method: "POST",
    url: "/api/proposals/p1/steward/veto",
    response: p1,
  },
  undoStewardDecision: {
    call: (c) => c.undoStewardDecision("p1"),
    method: "POST",
    url: "/api/proposals/p1/steward/undo",
    response: p1,
  },
  applyStewardDraft: {
    call: (c) => c.applyStewardDraft("p1"),
    method: "POST",
    url: "/api/proposals/p1/steward/apply",
    response: p1,
  },
  getDiscussionDraft: {
    call: (c) => c.getDiscussionDraft(round),
    method: "GET",
    url: "/api/tickets/5/attempts/1/discussions/2/steward",
    response: draft,
  },
  assessDiscussionNow: {
    call: (c) => c.assessDiscussionNow(round),
    method: "POST",
    url: "/api/tickets/5/attempts/1/discussions/2/steward/assess",
    response: draft,
  },
  approveDiscussionDraft: {
    call: (c) => c.approveDiscussionDraft(round),
    method: "POST",
    url: "/api/tickets/5/attempts/1/discussions/2/steward/approve",
    response: decision,
  },
  editDiscussionDraft: {
    call: (c) => c.editDiscussionDraft(round, "mine"),
    method: "POST",
    url: "/api/tickets/5/attempts/1/discussions/2/steward/edit",
    body: { text: "mine" },
    response: decision,
  },
  rejectDiscussionDraft: {
    call: (c) => c.rejectDiscussionDraft(round, "no"),
    method: "POST",
    url: "/api/tickets/5/attempts/1/discussions/2/steward/reject",
    body: { reason: "no" },
    response: decision,
  },
  vetoDiscussionApply: {
    call: (c) => c.vetoDiscussionApply(round),
    method: "POST",
    url: "/api/tickets/5/attempts/1/discussions/2/steward/veto",
    response: decision,
  },
};

describeOperations("steward operations", () => import("./steward").then((m) => m.stewardApi), operations);
