package reports

// UsageTicketsSQL is the per-ticket list's page query for a filter, for the
// volume test to EXPLAIN.
func UsageTicketsSQL(f *UsageFilter, q *UsageTicketQuery) (string, []any) {
	sql, args, _, _ := usageTicketsSQL(f, q)
	return sql, args
}

// Queries are the report queries by name, for the volume test to EXPLAIN.
var Queries = map[string]string{
	"history_from":  queryHistoryFrom,
	"states":        queryStates,
	"lead":          queryLead,
	"throughput":    queryThroughput,
	"now":           queryNow,
	"waiting":       queryWaiting,
	"waiting_total": queryWaitingTotal,

	"usage_totals":       queryUsageTotals,
	"usage_days":         queryUsageDays,
	"usage_agents":       queryUsageAgents,
	"usage_models":       queryUsageModels,
	"usage_tools":        queryUsageTools,
	"usage_unreliable":   queryUsageUnreliable,
	"usage_ticket_count": queryUsageTicketCount,
	"usage_done":         queryUsageDone,
}
