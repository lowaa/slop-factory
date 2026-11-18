import * as fx from "@/test/fixtures";
import { describeOperations, type Operation } from "./operations_harness";

const p1 = fx.makeProposal({ id: "p1" });
const approved = { id: "p1", state: "approved", outcome: "", error: "" };

const operations: Record<string, Operation> = {
  listProposals: {
    call: (c) => c.listProposals("pending", { q: "login", priority: ["high"] }),
    method: "GET",
    url: "/api/proposals?q=login&priority=high&state=pending&limit=100",
    response: { proposals: [p1] },
    result: [p1],
  },
  listPendingProposalsPage: {
    call: (c) => c.listPendingProposalsPage({ q: "login" }, { limit: 50, cursor: "c1" }),
    method: "GET",
    url: "/api/proposals?q=login&state=pending&order=oldest&limit=50&cursor=c1",
    response: { proposals: [p1], next_cursor: "c2" },
    result: { proposals: [p1], nextCursor: "c2" },
  },
  listDecidedProposalsPage: {
    call: (c) => c.listDecidedProposalsPage({ q: "login" }, "rejected", { limit: 50, cursor: "c1" }),
    method: "GET",
    url: "/api/proposals?q=login&state=rejected&order=newest&limit=50&cursor=c1",
    response: { proposals: [p1], next_cursor: "c2" },
    result: { proposals: [p1], nextCursor: "c2" },
  },
  approveProposal: {
    call: (c) => c.approveProposal("p1"),
    method: "POST",
    url: "/api/proposals/p1/approve",
    response: fx.makeProposal({ id: "p1", state: "approved" }),
  },
  rejectProposal: {
    call: (c) => c.rejectProposal("p1", "not yet"),
    method: "POST",
    url: "/api/proposals/p1/reject",
    body: { reason: "not yet" },
    response: fx.makeProposal({ id: "p1", state: "rejected" }),
  },
  approveAllProposals: {
    call: (c) => c.approveAllProposals("ready", ["p1"]),
    method: "POST",
    url: "/api/proposals/approve_all",
    body: { kind: "ready", ids: ["p1"] },
    response: { results: [approved] },
    result: [approved],
  },
  rejectAllProposals: {
    call: (c) => c.rejectAllProposals("clearing", ["p1"]),
    method: "POST",
    url: "/api/proposals/reject_all",
    body: { reason: "clearing", ids: ["p1"] },
    response: { results: [] },
    result: [],
  },
};

describeOperations("proposals operations", () => import("./proposals").then((m) => m.proposalsApi), operations);
