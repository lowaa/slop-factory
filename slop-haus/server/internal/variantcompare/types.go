// Package variantcompare answers whether a variant is good enough compared
// with a baseline variant across many tickets. It is read-only: it reads the
// attempts' cost, guidance, signals and verdicts through narrow ports and
// writes nothing.
package variantcompare

import (
	"errors"

	"github.com/google/uuid"
)

// MaxTickets bounds the set a comparison reads, so one request cannot fan out
// over the whole board.
const MaxTickets = 200

// MaxChallengers bounds the variants compared with the baseline in one call.
const MaxChallengers = 10

var (
	// ErrInvalid is a request that names no tickets, both a batch and a list,
	// no challengers, or the baseline as its own challenger.
	ErrInvalid = errors.New("variantcompare: the comparison is malformed")
	// ErrTooManyTickets is a set over MaxTickets.
	ErrTooManyTickets = errors.New("variantcompare: the set has too many tickets")
)

// Request is a comparison over tickets the person chose. A variant is named
// by its name; "" is the default variant, the attempts routed to none.
type Request struct {
	// ReplayBatchID names a replay batch, whose tickets are the set. Exactly
	// one of it and TicketIDs is given.
	ReplayBatchID *uuid.UUID
	TicketIDs     []int64
	// MinDiffLines and MaxDiffLines keep the tickets whose original (promoted)
	// attempt changed that many lines, added plus removed. A ticket whose
	// original size is not known is left out when either is set.
	MinDiffLines *int
	MaxDiffLines *int

	Baseline    string
	Challengers []string
}

// The groups the measures fall in. They are shown side by side and never
// combined into a score.
const (
	GroupCost     = "cost"
	GroupHuman    = "human_cost"
	GroupQuality  = "quality"
	GroupSignals  = "signals"
	GroupNotCount = "not_counted"
)

// MeasureInfo describes one measure.
type MeasureInfo struct {
	Key   string `json:"key"`
	Group string `json:"group" enum:"cost,human_cost,quality,signals,not_counted"`
	Unit  string `json:"unit"`
	Doc   string `json:"doc"`
}

// Verdicts counts the attempts' current verdicts.
type Verdicts struct {
	WouldMerge            int `json:"would_merge"`
	WouldMergeWithChanges int `json:"would_merge_with_changes"`
	WouldNotMerge         int `json:"would_not_merge"`
	Unjudged              int `json:"unjudged"`
}

// RunWith is what the attempts of one side ran with, each field the distinct
// values the runs recorded, sorted. A value no run recorded is absent.
type RunWith struct {
	VariantVersions   []string `json:"variant_versions"`
	HarnessVersions   []string `json:"harness_versions"`
	Models            []string `json:"models"`
	BundleHashes      []string `json:"bundle_hashes"`
	FleetInstructions []string `json:"fleet_instructions"`
}

// Side is one variant's attempts on a ticket.
type Side struct {
	Runs     int                `json:"runs" doc:"the finished attempts of the variant on the ticket"`
	Attempts []int              `json:"attempts"`
	Measures map[string]float64 `json:"measures" doc:"the median over the runs, for each measure known for at least one run"`
	Verdicts Verdicts           `json:"verdicts"`
	RunWith  RunWith            `json:"run_with"`
}

// Row is one ticket in one pair.
type Row struct {
	TicketID       int64  `json:"ticket_id"`
	Challenger     string `json:"challenger"`
	BaselineSide   Side   `json:"baseline"`
	ChallengerSide Side   `json:"challenger_side"`
	// Difference is the challenger's value minus the baseline's, for each
	// measure known on both sides.
	Difference map[string]float64 `json:"difference"`
	// RunWithFlags names what differed besides the variant, which makes the
	// row a less clean comparison.
	RunWithFlags []string `json:"run_with_flags"`
}

// Pair is the baseline against one challenger.
type Pair struct {
	Challenger string `json:"challenger"`
	// Tickets is the tickets both variants ran on: the n of every measure.
	Tickets  int                `json:"tickets"`
	Measures map[string]Summary `json:"measures" doc:"the spread of the per-ticket differences; a measure known for no ticket is absent"`
	Verdicts struct {
		Baseline   Verdicts `json:"baseline"`
		Challenger Verdicts `json:"challenger"`
	} `json:"verdicts" doc:"the verdicts of the attempts on the paired tickets, counted per verdict"`
	FlaggedTickets int `json:"flagged_tickets" doc:"the paired tickets with run_with_flags"`
}

// Result is a comparison.
type Result struct {
	Baseline string `json:"baseline"`
	// MaxTickets is the bound on a set.
	MaxTickets int `json:"max_tickets"`
	// TicketsInSet is the tickets the request named, or the batch held, after
	// the size filter.
	TicketsInSet int `json:"tickets_in_set"`
	// SizeFiltered is the tickets the size filter left out.
	SizeFiltered int `json:"size_filtered"`
	// UnfinishedAttempts is the attempts of the set left out because they have
	// not finished.
	UnfinishedAttempts int           `json:"unfinished_attempts"`
	Measures           []MeasureInfo `json:"measures"`
	Pairs              []Pair        `json:"pairs"`
	Rows               []Row         `json:"rows"`
	Notes              []string      `json:"notes"`
}
