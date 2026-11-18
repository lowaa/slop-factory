package llmusage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
)

// TranscriptKey identifies what a report's totals are for -- a transcript,
// a review run -- opaquely: the Service only needs reports for one key to
// overwrite each other. An app keeps its own columns (a ticket, an agent)
// beside it in its own table.
type TranscriptKey string

var transcriptKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,256}$`)

// Validate checks the key's shape.
func (k TranscriptKey) Validate() error {
	if !transcriptKeyPattern.MatchString(string(k)) {
		return invalid("transcript key %q", string(k))
	}
	return nil
}

// Store is where an app keeps usage. Each app implements it over its own
// table, inside its own tenant transaction; the module never sees tenancy.
// llmusagetest.StoreSuite is the test every implementation must pass.
type Store interface {
	// Upsert stores records as key's full totals, replacing whatever was
	// stored for key before (records and their tools: a record or tool
	// missing from records is gone afterwards), and returns what was
	// stored before -- empty on first sight.
	//
	// It must be atomic: the previous totals come from the same statement
	// or transaction that overwrites them (a CTE with ... RETURNING, or a
	// row lock), so two concurrent reports for one key can't both see the
	// same previous totals, and so both count.
	Upsert(ctx context.Context, key TranscriptKey, records []Record) (previous []Record, err error)
}

// Pricer fills in a record's estimated cost; pricing.Table is the real one.
type Pricer interface {
	// Estimate sets EstimatedCostMicros, PriceVersion and Unpriced.
	Estimate(r *Record)
}

// Options configure a Service. All are required.
type Options struct {
	Store     Store
	Collector *Collector
	Pricer    Pricer
	Logger    *slog.Logger
}

// Service ingests usage reports: Validate, Categorize, Estimate,
// Store.Upsert, Delta, and -- once the app has committed -- the collector.
// Its methods are safe for concurrent use.
type Service struct {
	store     Store
	collector *Collector
	pricer    Pricer
	logger    *slog.Logger
}

// NewService builds a Service.
func NewService(opts *Options) (*Service, error) {
	if opts == nil || opts.Store == nil || opts.Collector == nil || opts.Pricer == nil || opts.Logger == nil {
		return nil, errors.New("llmusage: Options needs Store, Collector, Pricer and Logger")
	}
	return &Service{store: opts.Store, collector: opts.Collector, pricer: opts.Pricer, logger: opts.Logger}, nil
}

// Pending is an ingested report whose metrics have not been counted yet.
type Pending struct {
	// Records are the totals as stored: categorised and priced.
	Records []Record

	deltas []*Record
	once   sync.Once
}

// Ingest validates a report of full totals for key, categorises its tools,
// prices it, stores it, and works out what the store actually changed. It
// does not touch the metrics: the caller passes the result to Commit once
// the transaction Store.Upsert ran in has committed. Counting before the
// commit would count a report that then rolled back, and a retry of it
// would count again.
//
// If Store.Upsert commits on its own, call Commit as soon as Ingest
// returns. If it joins a wider transaction of the app's, call Commit after
// that one commits, and drop the Pending if it rolls back.
func (s *Service) Ingest(ctx context.Context, key TranscriptKey, records []Record) (*Pending, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateReport(records); err != nil {
		return nil, err
	}
	stored := make([]Record, len(records))
	for i := range records {
		r := records[i].Clone()
		for j := range r.Tools {
			r.Tools[j].Category, _ = Categorize(r.Harness, r.Tools[j].Tool)
		}
		s.pricer.Estimate(r)
		// Again, now that the server's own fields are filled in, so a
		// Pricer can't hand the store something a receiver would refuse.
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("llmusage: priced record %d: %w", i, err)
		}
		stored[i] = *r
	}
	previous, err := s.store.Upsert(ctx, key, stored)
	if err != nil {
		return nil, fmt.Errorf("llmusage: store usage for %s: %w", key, err)
	}
	return &Pending{Records: stored, deltas: s.deltas(key, previous, stored)}, nil
}

func (s *Service) deltas(key TranscriptKey, previous, stored []Record) []*Record {
	before := make(map[RecordKey]*Record, len(previous))
	for i := range previous {
		// Re-priced with today's table, so a price-table change between
		// two reports shows as a cost change only where tokens changed,
		// not as a shrink that drops the whole delta.
		p := previous[i].Clone()
		s.pricer.Estimate(p)
		before[p.Key()] = p
	}
	var out []*Record
	for i := range stored {
		k := stored[i].Key()
		d, err := Delta(before[k], &stored[i])
		delete(before, k)
		if err != nil {
			// Unreachable: before is indexed by the same key.
			s.logger.Error("llm usage delta", "key", string(key), "model", k.Model, "error", err)
			continue
		}
		if d.Shrank() {
			// Counters only go up. Totals that shrank (a transcript cut
			// short, say) add nothing rather than a decrement or a
			// partial count; the store still holds the new totals.
			s.logger.Warn("llm usage totals shrank; not counted", "key", string(key), "provider", k.Provider, "model", k.Model, "harness", string(k.Harness))
			continue
		}
		if !d.IsZero() {
			out = append(out, d)
		}
	}
	for k := range before {
		s.logger.Warn("llm usage record dropped from totals; not counted", "key", string(key), "provider", k.Provider, "model", k.Model, "harness", string(k.Harness))
	}
	return out
}

// Commit counts an ingested report's delta. Call it once the transaction
// the Upsert ran in has committed; a second call adds nothing.
func (s *Service) Commit(p *Pending) {
	p.once.Do(func() {
		for _, d := range p.deltas {
			if err := s.collector.Add(d); err != nil {
				// Unreachable: Ingest validated and dropped shrunk deltas.
				s.logger.Error("llm usage metrics", "model", d.Model, "error", err)
			}
		}
	})
}
