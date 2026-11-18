import {
  client,
  unwrap,
} from "./client";
import type {
  LockHistoryQuery,
  LockHistoryReport,
  LlmUsageQuery,
  LlmUsageReport,
  LlmUsageTicketPage,
  LlmUsageTicketsQuery,
  Report,
  ReportQuery,
  ReportWaitingPage,
  ReportWaitingQuery,
  ScopeCostQuery,
  ScopeCostReport,
  ShadowDecisionsQuery,
  ShadowDecisionsReport,
  TicketLlmUsage,
} from "./types";

export const reportsApi = {
  // Both report calls return a single object, like getTicket.
  async getReport(query: ReportQuery): Promise<Report> {
    return await unwrap(client.GET("/api/reports", { params: { query } }));
  },

  async getReportWaiting(
    query: ReportWaitingQuery,
  ): Promise<ReportWaitingPage> {
    return await unwrap(
      client.GET("/api/reports/waiting", { params: { query } }),
    );
  },

  async getLlmUsageReport(query: LlmUsageQuery): Promise<LlmUsageReport> {
    return await unwrap(
      client.GET("/api/reports/llm-usage", { params: { query } }),
    );
  },

  async getScopeCostReport(query: ScopeCostQuery): Promise<ScopeCostReport> {
    return await unwrap(
      client.GET("/api/reports/scope-cost", { params: { query } }),
    );
  },

  async getLockHistoryReport(query: LockHistoryQuery): Promise<LockHistoryReport> {
    return await unwrap(
      client.GET("/api/reports/lock-history", { params: { query } }),
    );
  },

  async getShadowDecisionsReport(
    query: ShadowDecisionsQuery,
  ): Promise<ShadowDecisionsReport> {
    return await unwrap(
      client.GET("/api/reports/shadow-decisions", { params: { query } }),
    );
  },

  async getTicketLlmUsage(id: number): Promise<TicketLlmUsage> {
    return await unwrap(
      client.GET("/api/tickets/{id}/llm-usage", { params: { path: { id } } }),
    );
  },

  async getLlmUsageTickets(
    query: LlmUsageTicketsQuery,
  ): Promise<LlmUsageTicketPage> {
    return await unwrap(
      client.GET("/api/reports/llm-usage/tickets", { params: { query } }),
    );
  },
};
