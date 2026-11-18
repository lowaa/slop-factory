package reviewreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Options configure a Service. Store, Settings and Limit are required.
type Options struct {
	Store    Store
	Settings SettingsHistory
	// Limit is how many reports and labels one agent or supervisor may post
	// per window.
	Limit *ratelimit.Config
	// BoardURL is the board's public base URL, for the link an accepted run
	// answers with; empty sends none.
	BoardURL string
}

// Service is what the report routes go through.
type Service struct {
	store    Store
	settings SettingsHistory
	limiter  *ratelimit.Limiter
	boardURL string
	logger   *slog.Logger
}

func NewService(opts *Options, logHandler slog.Handler) (*Service, error) {
	if opts == nil || opts.Store == nil || opts.Settings == nil || opts.Limit == nil {
		return nil, errors.New("reviewreport: Options needs Store, Settings and Limit")
	}
	return &Service{
		store:    opts.Store,
		settings: opts.Settings,
		limiter:  ratelimit.New(opts.Limit),
		boardURL: opts.BoardURL,
		logger:   slog.New(logHandler),
	}, nil
}

// maxErrorBytes bounds the validation errors quoted back: a report can be
// wrong in thousands of ways at once.
const maxErrorBytes = 4 << 10

// Ingest stores a run posted by the caller -- a supervisor or an agent, the
// token's, whatever the report says. raw is the body as posted, which is
// decoded and validated here: the client's own validation is never trusted.
func (s *Service) Ingest(ctx context.Context, raw []byte) (*report.Accepted, bool, error) {
	caller, err := s.caller(ctx)
	if err != nil {
		return nil, false, err
	}
	poster := &Poster{Kind: PostedByAgent, ID: caller.AgentID()}
	if authn.IsSupervisor(caller) {
		poster.Kind = PostedBySupervisor
	}
	if len(raw) > report.MaxReportBytes {
		return nil, false, fmt.Errorf("%w: the report is over %d bytes", ErrInvalid, report.MaxReportBytes)
	}
	var run report.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, fmt.Errorf("%w: the body is not a run: %s", ErrInvalid, clip(err.Error()))
	}
	if err := run.Validate(); err != nil {
		return nil, false, fmt.Errorf("%w: %s", ErrInvalid, clip(err.Error()))
	}
	// The contract reports this so a server can refuse it: such a run's
	// diff and findings may quote secrets.
	if run.Config.AllowSecrets {
		return nil, false, fmt.Errorf("%w: the run showed secret-shaped files to the model unredacted, so it is not stored", ErrInvalid)
	}
	inserted, err := s.store.InsertRun(ctx, poster, &run)
	if err != nil {
		return nil, false, err
	}
	accepted := &report.Accepted{RunID: run.ID, Created: inserted.Created}
	if a := inserted.Attribution; a != nil {
		accepted.URL = s.attemptURL(caller.TenantID(), a)
	}
	return accepted, inserted.Created, nil
}

// Label records the calling agent's verdict on a finding of a run of the
// attempt it holds, or of a run it posted.
func (s *Service) Label(ctx context.Context, findingID uuid.UUID, l *Label) error {
	caller, err := s.caller(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(LabelVerdicts, l.Verdict) {
		return fmt.Errorf("%w: verdict must be one of %v", ErrInvalid, LabelVerdicts)
	}
	if l.FixedIn != "" && l.Verdict != "confirmed" {
		return fmt.Errorf("%w: fixed_in applies to a confirmed finding only", ErrInvalid)
	}
	if err := checkChars("reason", l.Reason, MaxLabelReasonChars); err != nil {
		return err
	}
	if err := checkChars("fixed_in", l.FixedIn, MaxFixedInChars); err != nil {
		return err
	}
	return s.store.LabelFinding(ctx, caller.AgentID(), findingID, l)
}

// AttemptRuns is the attempt's newest runs for the board.
func (s *Service) AttemptRuns(ctx context.Context, ref ticket.AttemptRef, limit int) ([]AttemptRun, error) {
	return s.store.AttemptRuns(ctx, ref, limit)
}

// caller is the posting agent or supervisor, counted against its limit.
func (s *Service) caller(ctx context.Context) (authn.Identity, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !s.limiter.Allow(caller.AgentID().String()) {
		return nil, ErrRateLimited
	}
	return caller, nil
}

// attemptURL is the attempt's page on the board. The tenant is in the link
// because a ticket's number is only unique within one.
func (s *Service) attemptURL(tenant uuid.UUID, a *Attribution) string {
	if s.boardURL == "" {
		return ""
	}
	query := url.Values{"tenant": {tenant.String()}}
	return fmt.Sprintf("%s/tickets/%d/attempts/%d?%s", s.boardURL, a.TicketID, a.Attempt, query.Encode())
}

// ProviderConfig is the tenant's provider config as the reviewer reads it;
// ErrNoProviderConfig when none is set.
func (s *Service) ProviderConfig(ctx context.Context) (*providerconfig.Config, error) {
	cfg, err := readProviderConfig(ctx, s.settings)
	if errors.Is(err, errStoredConfigInvalid) {
		// Stored under an earlier release that knew other providers: a
		// reviewer would ignore it, so it is served as none.
		s.logger.Warn("the stored provider config is no longer valid; serving none", "err", err)
		return nil, ErrNoProviderConfig
	}
	return cfg, err
}

func clip(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	return s[:maxErrorBytes] + "..."
}
