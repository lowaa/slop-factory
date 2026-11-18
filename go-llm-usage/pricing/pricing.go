// Package pricing estimates what LLM usage costs at API prices, from a
// price table built into the module: generated from OpenRouter's model list
// by `go generate` and committed, so each price change is a reviewed diff
// and no server needs network access to price usage.
package pricing

//go:generate go run ../cmd/genprices -out prices_gen.go

import (
	"maps"
	"math/big"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

// Price is a model's price per token type, in micro-dollars per million
// tokens (so $0.10 per million is 100_000). The unit keeps OpenRouter's
// per-token decimal prices integers.
type Price struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Table prices records. It implements llmusage.Pricer.
type Table struct {
	version string
	prices  map[string]Price
	aliases map[string]string
}

// NewTable builds a table. aliases map a model name as a harness reports it
// to its entry in prices. The maps are copied, so a table never changes
// under the requests using it.
func NewTable(version string, prices map[string]Price, aliases map[string]string) *Table {
	return &Table{version: version, prices: maps.Clone(prices), aliases: maps.Clone(aliases)}
}

// Default is the generated table with the module's aliases.
func Default() *Table {
	return NewTable(generatedVersion, generatedPrices, aliases)
}

// Aliases returns a copy of the module's model aliases.
func Aliases() map[string]string { return maps.Clone(aliases) }

// Version is the table's version: its generation date and a hash of its
// prices.
func (t *Table) Version() string { return t.version }

// Known reports whether the table can price model, so the collector can
// give it a model label of its own (llmusage.ModelSet).
func (t *Table) Known(model string) bool {
	_, _, ok := t.Lookup(model)
	return ok
}

// Lookup finds model's price, by its own name or its alias, and returns the
// table entry it resolved to.
func (t *Table) Lookup(model string) (entry string, price Price, ok bool) {
	if p, ok := t.prices[model]; ok {
		return model, p, true
	}
	if target, ok := t.aliases[model]; ok {
		if p, ok := t.prices[target]; ok {
			return target, p, true
		}
	}
	return "", Price{}, false
}

var perMillion = big.NewInt(1_000_000)

// Estimate sets the record's estimated cost and price version. A model the
// table doesn't know gets no estimate and is marked Unpriced -- never a
// guess from a similar name.
//
// Reasoning tokens are part of output tokens, so they are not priced again.
func (t *Table) Estimate(r *llmusage.Record) {
	r.PriceVersion = t.version
	r.EstimatedCostMicros = nil
	r.Unpriced = true
	_, p, ok := t.Lookup(r.Model)
	if !ok {
		return
	}
	// big.Int because a large token count times a dear price overflows
	// int64 before the division.
	total := new(big.Int)
	for _, term := range [][2]int64{
		{r.InputTokens, p.Input},
		{r.OutputTokens, p.Output},
		{r.CacheReadTokens, p.CacheRead},
		{r.CacheWriteTokens, p.CacheWrite},
	} {
		total.Add(total, new(big.Int).Mul(big.NewInt(term[0]), big.NewInt(term[1])))
	}
	// Round half up to whole micro-dollars.
	total.Add(total, big.NewInt(500_000))
	total.Quo(total, perMillion)
	// Past the record's cost limit the estimate means nothing (counts that
	// large are a client bug), so it is left unpriced, not stored.
	if !total.IsInt64() || total.Int64() > llmusage.MaxCostMicros {
		return
	}
	cost := total.Int64()
	r.EstimatedCostMicros = &cost
	r.Unpriced = false
}

var (
	_ llmusage.Pricer   = (*Table)(nil)
	_ llmusage.ModelSet = (*Table)(nil)
)
