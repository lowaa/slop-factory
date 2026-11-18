package llmusage

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	usagelib "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Options configure a Service. All are required.
type Options struct {
	Store     Store
	Collector *usagelib.Collector
	Pricer    usagelib.Pricer
	// Limit is how many reports one agent may send per window. Reports are
	// full totals, so one refused loses nothing the next one won't carry.
	Limit *ratelimit.Config
}

// Service ingests the usage reports agents send.
type Service struct {
	store     Store
	collector *usagelib.Collector
	pricer    usagelib.Pricer
	limiter   *ratelimit.Limiter
	logger    *slog.Logger
}

func NewService(opts *Options, logHandler slog.Handler) (*Service, error) {
	if opts == nil || opts.Store == nil || opts.Collector == nil || opts.Pricer == nil || opts.Limit == nil {
		return nil, errors.New("llmusage: Options needs Store, Collector, Pricer and Limit")
	}
	return &Service{
		store:     opts.Store,
		collector: opts.Collector,
		pricer:    opts.Pricer,
		limiter:   ratelimit.New(opts.Limit),
		logger:    slog.New(logHandler),
	}, nil
}

// Ingest stores a report for the calling agent -- the token's, whatever
// the report says -- attributes it to a ticket, and counts what it added
// in the llm_* metrics.
func (s *Service) Ingest(ctx context.Context, report *Report) (*Attribution, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !s.limiter.Allow(caller.AgentID().String()) {
		return nil, ErrRateLimited
	}
	report.Transcript.AgentID = caller.AgentID()
	// A conductor's usage is the host's to report per conversation. One sent
	// untagged would be charged to nothing (a conductor holds no leases) and,
	// from a full conductor's Stop hook, count the same turns again.
	if c, ok := caller.(interface{ IsConductor() bool }); ok && c.IsConductor() && report.Transcript.ConversationID == nil {
		return nil, invalid("a conductor reports usage per conversation: set conversation_id")
	}
	if err := report.validate(); err != nil {
		return nil, err
	}

	// go-llm-usage's Service runs validate, categorise, estimate, upsert
	// and delta over this transcript's store. One per report because the
	// store carries the transcript; building one is a few field copies.
	bound := &transcriptStore{store: s.store, transcript: &report.Transcript}
	ingest, err := usagelib.NewService(&usagelib.Options{
		Store: bound, Collector: s.collector, Pricer: s.pricer, Logger: s.logger,
	})
	if err != nil {
		return nil, err
	}
	pending, err := ingest.Ingest(ctx, report.Transcript.Key, report.Records)
	if errors.Is(err, ErrImplausible) {
		s.logger.Warn("refused implausible llm usage report",
			"agent_id", report.Transcript.AgentID, "transcript_key", report.Transcript.Key,
			"harness", report.Transcript.Harness, "err", err)
		return nil, err
	}
	if errors.Is(err, usagelib.ErrInvalid) {
		return nil, errors.Join(ErrInvalid, err)
	}
	if err != nil {
		return nil, err
	}
	// The upsert's transaction has committed by now (it is the whole of
	// it), so the delta it applied is counted -- once.
	ingest.Commit(pending)
	return bound.attribution, nil
}

// transcriptStore is go-llm-usage's Store over one transcript of
// slop-haus's store, keeping the attribution the upsert worked out.
type transcriptStore struct {
	store       Store
	transcript  *Transcript
	attribution *Attribution
}

func (b *transcriptStore) Upsert(ctx context.Context, key usagelib.TranscriptKey, records []usagelib.Record) ([]usagelib.Record, error) {
	t := *b.transcript
	t.Key = key
	previous, attribution, err := b.store.Upsert(ctx, &t, records)
	if err != nil {
		return nil, err
	}
	b.attribution = attribution
	return previous, nil
}
