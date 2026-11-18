import * as fx from "@/test/fixtures";
import { describeOperations, type Operation } from "./operations_harness";

const operations: Record<string, Operation> = {
  getReport: {
    call: (c) => c.getReport({ days: 7, priority: "high" }),
    method: "GET",
    url: "/api/reports?days=7&priority=high",
    response: fx.makeReport({ states: [] }),
  },
  getReportWaiting: {
    call: (c) => c.getReportWaiting({ status: "discussion", limit: 10, offset: 20 }),
    method: "GET",
    url: "/api/reports/waiting?status=discussion&limit=10&offset=20",
    response: { tickets: [], total: 0 },
  },
  getLlmUsageReport: {
    call: (c) => c.getLlmUsageReport({ days: 90 }),
    method: "GET",
    url: "/api/reports/llm-usage?days=90",
    response: fx.makeLlmUsageReport(),
  },
  getScopeCostReport: {
    call: (c) => c.getScopeCostReport({ days: 7 }),
    method: "GET",
    url: "/api/reports/scope-cost?days=7",
    response: fx.makeScopeCostReport(),
  },
  getLockHistoryReport: {
    call: (c) => c.getLockHistoryReport({ days: 7 }),
    method: "GET",
    url: "/api/reports/lock-history?days=7",
    response: fx.makeLockHistoryReport(),
  },
  getShadowDecisionsReport: {
    call: (c) => c.getShadowDecisionsReport({ days: 30 }),
    method: "GET",
    url: "/api/reports/shadow-decisions?days=30",
    response: fx.makeShadowDecisionsReport(),
  },
  getTicketLlmUsage: {
    call: (c) => c.getTicketLlmUsage(5),
    method: "GET",
    url: "/api/tickets/5/llm-usage",
    response: { worker: [], guidance: [] },
  },
  getLlmUsageTickets: {
    call: (c) => c.getLlmUsageTickets({ days: 7, status: ["open", "done"], priority: ["high"], q: "a b", created_from: "2026-01-02", limit: 10, cursor: "c2" }),
    method: "GET",
    url: "/api/reports/llm-usage/tickets?days=7&status=open&status=done&priority=high&q=a+b&created_from=2026-01-02&limit=10&cursor=c2",
    response: { tickets: [], total: 0 },
  },
};

describeOperations("reports operations", () => import("./reports").then((m) => m.reportsApi), operations);
