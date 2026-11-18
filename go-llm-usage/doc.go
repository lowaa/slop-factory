// Package llmusage holds the types and logic every app that records LLM
// usage shares: the Record wire format and its validation, tool
// categories, the bash command label, the delta between two reports, the
// Prometheus collector, and the ingest Service that ties them together over
// an app-owned Store.
//
// The module owns no tables, migrations or network access. Each app stores
// usage in its own database under its own tenancy, implements Store, and
// checks that implementation with llmusagetest.StoreSuite. Grafana gets one
// view because every app exports the same metric names and labels, built
// here.
//
// # What cost means
//
// Money is integer micro-dollars throughout (1 USD = 1_000_000).
//
// A record's BillingMode says what its cost is:
//
//   - BillingMetered: pay per token (OpenRouter). The cost is money spent.
//     ReportedCostMicros is what the provider charged, when it says.
//   - BillingSubscription: a flat-rate plan (Claude Code on a Max
//     subscription, a ChatGPT-subscription provider). Nothing is
//     charged per token, so EstimatedCostMicros is what the same tokens
//     would have cost at API prices: comparable between pieces of work, but
//     not money spent.
//
// Dashboards must never add the two modes together; every cost metric
// carries the billing_mode label so they can be kept apart.
//
// Estimated cost is worked out when usage arrives, with the price table of
// the day, and stored with that table's version (PriceVersion), so history
// doesn't shift when prices change. Tokens are kept so it can be
// recalculated.
package llmusage
