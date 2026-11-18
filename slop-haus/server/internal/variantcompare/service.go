package variantcompare

import (
	"context"
	"fmt"
	"sort"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Service compares variants over a set of tickets.
type Service struct {
	source   Source
	signals  SignalsReader
	costs    ticket.AttemptCosts
	verdicts ticket.AttemptVerdicts
}

func NewService(source Source, signals SignalsReader, costs ticket.AttemptCosts, verdicts ticket.AttemptVerdicts) *Service {
	return &Service{source: source, signals: signals, costs: costs, verdicts: verdicts}
}

// attemptData is everything known of one finished attempt.
type attemptData struct {
	row          AttemptRow
	signals      *ticket.AttemptSignalsBody
	workerCost   []ticket.CostFigure
	guidanceCost []ticket.CostFigure
	verdict      string
}

func (a *attemptData) variantName() string {
	if a.row.Variant == nil {
		return ""
	}
	return a.row.Variant.Name
}

// Compare is for a signed-in person: it shows verdicts, which a worker never
// sees about its own work.
func (s *Service) Compare(ctx context.Context, who *authn.User, req *Request) (*Result, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	ids, err := s.ticketSet(ctx, req)
	if err != nil {
		return nil, err
	}
	res := &Result{Baseline: req.Baseline, MaxTickets: MaxTickets, Measures: measureInfos(),
		Pairs: []Pair{}, Rows: []Row{}, Notes: notes()}
	if len(ids) == 0 {
		return res, nil
	}
	rows, unfinished, err := s.source.FinishedAttempts(ctx, ids)
	if err != nil {
		return nil, err
	}
	res.UnfinishedAttempts = unfinished

	attempts, err := s.load(ctx, rows)
	if err != nil {
		return nil, err
	}
	kept := s.sizeFilter(req, ids, attempts)
	res.SizeFiltered = len(ids) - len(kept)
	res.TicketsInSet = len(kept)

	byTicket := map[int64]map[string][]*attemptData{}
	for _, a := range attempts {
		if !kept[a.row.Ref.TicketID] {
			continue
		}
		v := byTicket[a.row.Ref.TicketID]
		if v == nil {
			v = map[string][]*attemptData{}
			byTicket[a.row.Ref.TicketID] = v
		}
		v[a.variantName()] = append(v[a.variantName()], a)
	}
	ticketIDs := make([]int64, 0, len(byTicket))
	for id := range byTicket {
		ticketIDs = append(ticketIDs, id)
	}
	sort.Slice(ticketIDs, func(i, j int) bool { return ticketIDs[i] < ticketIDs[j] })

	for _, challenger := range req.Challengers {
		pair := Pair{Challenger: challenger, Measures: map[string]Summary{}}
		diffs := map[string][]float64{}
		for _, id := range ticketIDs {
			base, other := byTicket[id][req.Baseline], byTicket[id][challenger]
			// Pairing: a ticket counts only when both variants ran on it.
			if len(base) == 0 || len(other) == 0 {
				continue
			}
			row := buildRow(id, challenger, base, other)
			for key, d := range row.Difference {
				diffs[key] = append(diffs[key], d)
			}
			pair.Tickets++
			if len(row.RunWithFlags) > 0 {
				pair.FlaggedTickets++
			}
			addVerdicts(&pair.Verdicts.Baseline, row.BaselineSide.Verdicts)
			addVerdicts(&pair.Verdicts.Challenger, row.ChallengerSide.Verdicts)
			res.Rows = append(res.Rows, row)
		}
		for key, values := range diffs {
			pair.Measures[key] = *summarize(values)
		}
		res.Pairs = append(res.Pairs, pair)
	}
	return res, nil
}

func validate(req *Request) error {
	switch {
	case (req.ReplayBatchID == nil) == (len(req.TicketIDs) == 0):
		return fmt.Errorf("%w: give a replay batch or a list of tickets, not both and not neither", ErrInvalid)
	case len(req.Challengers) == 0 || len(req.Challengers) > MaxChallengers:
		return fmt.Errorf("%w: give between 1 and %d challenger variants", ErrInvalid, MaxChallengers)
	}
	seen := map[string]bool{req.Baseline: true}
	for _, c := range req.Challengers {
		if seen[c] {
			return fmt.Errorf("%w: variant %q is named twice, or is the baseline", ErrInvalid, c)
		}
		seen[c] = true
	}
	if req.MinDiffLines != nil && req.MaxDiffLines != nil && *req.MinDiffLines > *req.MaxDiffLines {
		return fmt.Errorf("%w: the smallest diff size is over the largest", ErrInvalid)
	}
	return nil
}

func (s *Service) ticketSet(ctx context.Context, req *Request) ([]int64, error) {
	ids := req.TicketIDs
	if req.ReplayBatchID != nil {
		var err error
		if ids, err = s.source.BatchTickets(ctx, *req.ReplayBatchID); err != nil {
			return nil, err
		}
	}
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > MaxTickets {
		return nil, fmt.Errorf("%w: %d tickets, at most %d", ErrTooManyTickets, len(out), MaxTickets)
	}
	return out, nil
}

// load gathers the other ports' figures for the attempts.
func (s *Service) load(ctx context.Context, rows []AttemptRow) ([]*attemptData, error) {
	refs := make([]ticket.AttemptRef, len(rows))
	for i := range rows {
		refs[i] = rows[i].Ref
	}
	if len(refs) == 0 {
		return nil, nil
	}
	signals, err := s.signals.Get(ctx, refs)
	if err != nil {
		return nil, err
	}
	worker, err := s.costs.AttemptCosts(ctx, refs)
	if err != nil {
		return nil, err
	}
	guidance, err := s.source.GuidanceCosts(ctx, refs)
	if err != nil {
		return nil, err
	}
	verdicts, err := s.verdicts.AttemptVerdicts(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := make([]*attemptData, len(rows))
	for i := range rows {
		ref := rows[i].Ref
		a := &attemptData{row: rows[i], signals: signals[ref], workerCost: worker[ref], guidanceCost: guidance[ref]}
		if v := verdicts[ref]; v != nil {
			a.verdict = v.Verdict
		}
		out[i] = a
	}
	return out, nil
}

// sizeFilter is the tickets that stay: all of them without a size filter,
// else those whose promoted attempt's size is known and within it.
func (s *Service) sizeFilter(req *Request, ids []int64, attempts []*attemptData) map[int64]bool {
	kept := map[int64]bool{}
	if req.MinDiffLines == nil && req.MaxDiffLines == nil {
		for _, id := range ids {
			kept[id] = true
		}
		return kept
	}
	for _, a := range attempts {
		if !a.row.Promoted || a.signals == nil || !a.signals.Size.Known {
			continue
		}
		lines := a.signals.Size.LinesAdded + a.signals.Size.LinesRemoved
		if (req.MinDiffLines == nil || lines >= *req.MinDiffLines) && (req.MaxDiffLines == nil || lines <= *req.MaxDiffLines) {
			kept[a.row.Ref.TicketID] = true
		}
	}
	return kept
}

func notes() []string {
	return []string{
		fmt.Sprintf("A set holds at most %d tickets.", MaxTickets),
		"Only tickets where both the baseline and the challenger have a finished attempt are compared; each difference is the challenger's value minus the baseline's, and a variant that ran several times on a ticket counts as the median of its runs.",
		"Measures are side by side and never combined into a score; a measure unknown for an attempt is left out, not counted as zero.",
		"Human guidance given through Remote Control is not counted: Remote Control usage is general overhead by decision, not attributed to attempts. Comments people wrote in the attempt's own conversation are counted.",
		"Guidance is counted only where a session declared it was guiding the attempt (working_on).",
		"Time waiting on a person is shown (human_wait_seconds) and is not counted as cost.",
		"A row with run_with_flags differs in more than the variant, which makes it a less clean comparison.",
		"Every attempt of the chosen tickets that finished counts, including attempts outside the replay batch when a batch chose the set.",
	}
}

func buildRow(ticketID int64, challenger string, base, other []*attemptData) Row {
	row := Row{TicketID: ticketID, Challenger: challenger, BaselineSide: buildSide(base), ChallengerSide: buildSide(other),
		Difference: map[string]float64{}}
	for key, c := range row.ChallengerSide.Measures {
		if b, ok := row.BaselineSide.Measures[key]; ok {
			row.Difference[key] = c - b
		}
	}
	row.RunWithFlags = runWithFlags(&row.BaselineSide.RunWith, &row.ChallengerSide.RunWith)
	return row
}

func buildSide(attempts []*attemptData) Side {
	side := Side{Runs: len(attempts), Attempts: []int{}, Measures: map[string]float64{}}
	for _, a := range attempts {
		side.Attempts = append(side.Attempts, a.row.Ref.Number)
		switch a.verdict {
		case attemptverdict.WouldMerge:
			side.Verdicts.WouldMerge++
		case attemptverdict.WouldMergeWithChanges:
			side.Verdicts.WouldMergeWithChanges++
		case attemptverdict.WouldNotMerge:
			side.Verdicts.WouldNotMerge++
		default:
			side.Verdicts.Unjudged++
		}
	}
	for i := range measures {
		var values []float64
		for _, a := range attempts {
			if v, ok := measures[i].read(a); ok {
				values = append(values, v)
			}
		}
		if m, ok := median(values); ok {
			side.Measures[measures[i].info.Key] = m
		}
	}
	side.RunWith = collectRunWith(attempts)
	return side
}

func addVerdicts(into *Verdicts, v Verdicts) {
	into.WouldMerge += v.WouldMerge
	into.WouldMergeWithChanges += v.WouldMergeWithChanges
	into.WouldNotMerge += v.WouldNotMerge
	into.Unjudged += v.Unjudged
}

func collectRunWith(attempts []*attemptData) RunWith {
	versions, harness, models, bundles, fleet := set{}, set{}, set{}, set{}, set{}
	for _, a := range attempts {
		if a.row.Variant != nil {
			versions.add(fmt.Sprintf("%s v%d", a.row.Variant.Name, a.row.Variant.Version))
		}
		for _, r := range a.row.Runs {
			harness.add(r.HarnessVersion)
			models.add(r.Model)
			bundles.add(r.BundleHash)
			fleet.add(r.FleetInstructions)
		}
	}
	return RunWith{VariantVersions: versions.sorted(), HarnessVersions: harness.sorted(), Models: models.sorted(),
		BundleHashes: bundles.sorted(), FleetInstructions: fleet.sorted()}
}

type set map[string]bool

func (s set) add(v string) {
	if v != "" {
		s[v] = true
	}
}

func (s set) sorted() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runWithFlags names what differs besides the variant. The harness version
// and the workspace bundle are not part of a variant, so a difference between
// the sides is a confound. The model and the fleet instructions are what a
// variant sets, so they differ between sides by design; only runs of one
// variant that differ among themselves are flagged. A field one side never
// recorded cannot be compared and is not flagged.
func runWithFlags(base, other *RunWith) []string {
	flags := []string{}
	for _, f := range []struct {
		name        string
		base, other []string
	}{
		{"harness_version", base.HarnessVersions, other.HarnessVersions},
		{"bundle_hash", base.BundleHashes, other.BundleHashes},
	} {
		if len(f.base) > 0 && len(f.other) > 0 && !sameValues(f.base, f.other) {
			flags = append(flags, f.name+"_differs")
		}
	}
	for _, side := range []struct {
		name string
		rw   *RunWith
	}{{"baseline", base}, {"challenger", other}} {
		for _, f := range []struct {
			name   string
			values []string
		}{
			{"variant_version", side.rw.VariantVersions},
			{"harness_version", side.rw.HarnessVersions},
			{"model", side.rw.Models},
			{"bundle_hash", side.rw.BundleHashes},
			{"fleet_instructions", side.rw.FleetInstructions},
		} {
			if len(f.values) > 1 {
				flags = append(flags, side.name+"_mixed_"+f.name)
			}
		}
	}
	return flags
}
