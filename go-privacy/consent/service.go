package consent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Config configures a Service.
type Config struct {
	Registry     *Registry
	Capabilities Capabilities
	Documents    Documents
	Store        Store
	Cookie       *CookieCodec
	// Clock is time.Now unless a test sets it.
	Clock  func() time.Time
	Logger slog.Handler
}

// Service holds the consent rules: what is effective, what a decision
// does, and how an anonymous visitor's cookie merges into a record.
type Service struct {
	cfg    Config
	logger *slog.Logger
}

func NewService(cfg *Config) (*Service, error) {
	if cfg.Registry == nil || cfg.Documents == nil || cfg.Store == nil || cfg.Cookie == nil {
		return nil, errors.New("consent: a service needs a registry, documents, a store and a cookie codec")
	}
	c := *cfg
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.DiscardHandler
	}
	return &Service{cfg: c, logger: slog.New(c.Logger)}, nil
}

// Registry is the purposes the service was built with.
func (s *Service) Registry() *Registry { return s.cfg.Registry }

// Subject is who a request is from.
type Subject struct {
	// UserID is the signed-in user, if HasUser.
	UserID    uuid.UUID
	HasUser   bool
	SessionID string
	// Cookie is the visitor's opened consent cookie, nil if none.
	Cookie *CookieState
	// GPC is the Sec-GPC signal.
	GPC bool
}

// Resolved is the result of resolving a subject.
type Resolved struct {
	Effective *Effective
	// Cookie, if non-nil, is the state the response should set the consent
	// cookie to.
	Cookie *CookieState
}

// DecisionError is a refusal of a decision, with a machine-readable code.
type DecisionError struct {
	Code   string
	Detail string
}

func (e *DecisionError) Error() string { return e.Code + ": " + e.Detail }

const (
	CodeInvalidPurpose   = "invalid_purpose"
	CodeNecessary        = "necessary_purpose"
	CodeSignInRequired   = "sign_in_required"
	CodeGPCActive        = "gpc_active"
	CodeDocumentChanged  = "document_changed"
	CodeConsentChanged   = "consent_changed"
	CodeInvalidSource    = "invalid_source"
	CodeDuplicatePurpose = "duplicate_purpose"
)

func refuse(code, detail string) error { return &DecisionError{Code: code, Detail: detail} }

// docStates loads the documents the purposes are shown under. An anonymous
// visitor is never shown a processing purpose, so its document being
// unavailable must not take their consent down with it.
func (s *Service) docStates(ctx context.Context, purposes []*Purpose, signedIn bool) (map[string]*docState, error) {
	out := map[string]*docState{}
	for _, p := range purposes {
		if p.Document == "" || out[p.Document] != nil || (p.Kind == Processing && !signedIn) {
			continue
		}
		vs, err := s.cfg.Documents.Versions(ctx, p.Document)
		if err != nil {
			return nil, &opError{msg: fmt.Sprintf("consent: documents for %q unavailable", p.Document), cause: err}
		}
		ds, err := newDocState(p.Document, vs, s.cfg.Clock())
		if err != nil {
			return nil, err
		}
		out[p.Document] = ds
	}
	return out, nil
}

func eventDecision(e *Event) *decision {
	return &decision{granted: e.Granted, version: e.DocumentVersion, at: e.DecidedAt, eventID: e.ID, source: e.Source}
}

func cookieDecisions(c *CookieState) map[string]*decision {
	out := map[string]*decision{}
	if c == nil {
		return out
	}
	for id, d := range c.Decisions {
		out[id] = &decision{granted: d.Granted, version: d.Version, at: d.DecidedAt, source: d.Source}
	}
	return out
}

// offered is the purposes on offer to the request: capability-gated ones
// whose flag is off are left out.
func (s *Service) offered(ctx context.Context) []*Purpose {
	return s.cfg.Registry.available(ctx, s.cfg.Capabilities)
}

// Effective computes the consent in force without writing anything.
func (s *Service) Effective(ctx context.Context, sub *Subject) (*Effective, error) {
	purposes := s.offered(ctx)
	docs, err := s.docStates(ctx, purposes, sub.HasUser)
	if err != nil {
		return nil, err
	}
	if !sub.HasUser {
		return evaluate(purposes, cookieDecisions(sub.Cookie), docs, sub.GPC, false), nil
	}
	cur, err := s.cfg.Store.Current(ctx, sub.UserID)
	if err != nil {
		return nil, err
	}
	ds := map[string]*decision{}
	for id, e := range cur {
		ds[id] = eventDecision(e)
	}
	return evaluate(purposes, ds, docs, sub.GPC, true), nil
}

// Resolve brings a signed-in user's record up to date with the request, then
// computes the effective consent: it records the GPC opt-out (README §7),
// and, with merge, folds the visitor's cookie into the record (README §5).
// Merging is left to the consent routes so that an ordinary request never
// writes more than the GPC opt-out. An anonymous subject is never written
// for.
func (s *Service) Resolve(ctx context.Context, sub *Subject, merge bool) (*Resolved, error) {
	res := &Resolved{}
	if sub.HasUser {
		if merge && sub.Cookie != nil && sub.Cookie.MergeToken != "" {
			if err := s.merge(ctx, sub); err != nil {
				return nil, err
			}
			// Rewritten from the record below, without the token, whether or
			// not anything was merged: a spent token must not linger.
			res.Cookie = &CookieState{}
		}
		if sub.GPC {
			// The signal denies the sale/share purposes for this request
			// whether or not the opt-out could be written, so a failed write
			// is logged and the request goes on rather than losing the rest
			// of the person's consent with it.
			if err := s.recordGPC(ctx, sub); err != nil {
				s.logger.Error("gpc opt-out not recorded; denied for this request only", "err", errText(err))
			}
		}
	}
	eff, err := s.Effective(ctx, sub)
	if err != nil {
		return nil, err
	}
	res.Effective = eff
	if res.Cookie != nil {
		res.Cookie = s.cookieFrom(eff)
	}
	return res, nil
}

// cookieFrom is the cookie for a signed-in user's effective cookie-category
// state: the decisions, none of them unmerged, and no merge token.
func (s *Service) cookieFrom(eff *Effective) *CookieState {
	c := &CookieState{Decisions: map[string]*CookieDecision{}, Expires: s.cfg.Cookie.Expiry(s.cfg.Clock())}
	for _, st := range eff.States {
		if st.Purpose.Kind != CookieCategory || st.Purpose.Necessary || !st.Decided {
			continue
		}
		c.Decisions[st.Purpose.ID] = &CookieDecision{
			Purpose: st.Purpose.ID, Granted: st.DecisionGranted, Version: st.DecisionVersion,
			DecidedAt: st.DecidedAt, Source: st.Source,
		}
	}
	return c
}

// mergeEvents are the cookie's unmerged decisions that belong in the
// record: each only if newer than what the record holds (the record wins
// when it is newer), skipping purposes in skip and any grant of a
// sale/share purpose under GPC.
func (s *Service) mergeEvents(sub *Subject, purposes []*Purpose, cur map[string]*Event, skip map[string]bool) []NewEvent {
	var out []NewEvent
	token := sub.Cookie.MergeToken
	for _, p := range purposes {
		d := sub.Cookie.Decisions[p.ID]
		if d == nil || !d.Unmerged || p.Kind != CookieCategory || p.Necessary || skip[p.ID] {
			continue
		}
		if c := cur[p.ID]; c != nil && !d.DecidedAt.After(c.DecidedAt) {
			continue
		}
		if d.Granted && p.SaleOrShare && sub.GPC {
			continue
		}
		src := d.Source
		if !src.clientSettable() {
			src = SourceBanner
		}
		out = append(out, NewEvent{
			Purpose: p.ID, Granted: d.Granted, DocumentKind: p.Document, DocumentVersion: d.Version,
			Source: src, DecidedAt: d.DecidedAt, SessionID: sub.SessionID,
			MergedFromAnonymous: true, MergeToken: token,
		})
	}
	return out
}

func (s *Service) merge(ctx context.Context, sub *Subject) error {
	purposes := s.offered(ctx)
	_, err := s.cfg.Store.Apply(ctx, sub.UserID, func(cur map[string]*Event) (*Plan, error) {
		return &Plan{MergeToken: sub.Cookie.MergeToken, Events: s.mergeEvents(sub, purposes, cur, nil)}, nil
	})
	return err
}

// recordGPC appends a gpc denial for each declared SaleOrShare purpose the
// user hasn't already denied. One event per change: once the latest
// decision is a denial, nothing more is written. Purposes whose capability
// is off are included: the opt-out has to still be there when the flag
// comes back on.
func (s *Service) recordGPC(ctx context.Context, sub *Subject) error {
	var sale []*Purpose
	for _, p := range s.cfg.Registry.purposes {
		if p.SaleOrShare {
			sale = append(sale, p)
		}
	}
	if len(sale) == 0 {
		return nil
	}
	cur, err := s.cfg.Store.Current(ctx, sub.UserID)
	if err != nil {
		return err
	}
	pending := false
	for _, p := range sale {
		if c := cur[p.ID]; c == nil || c.Granted {
			pending = true
		}
	}
	if !pending {
		return nil
	}
	docs, err := s.docStates(ctx, sale, true)
	if err != nil {
		return err
	}
	now := s.cfg.Clock()
	_, err = s.cfg.Store.Apply(ctx, sub.UserID, func(cur map[string]*Event) (*Plan, error) {
		plan := &Plan{}
		for _, p := range sale {
			c := cur[p.ID]
			if c != nil && !c.Granted {
				continue
			}
			plan.Events = append(plan.Events, NewEvent{
				Purpose: p.ID, Granted: false, DocumentKind: p.Document, DocumentVersion: docs[p.Document].latest,
				Source: SourceGPC, DecidedAt: laterOf(now, c), SessionID: sub.SessionID,
			})
		}
		return plan, nil
	})
	return err
}

// laterOf keeps a purpose's events in order when a server's clock runs
// behind the one that wrote the previous event: ordering is by decided_at,
// so a newer decision must not carry an earlier time.
func laterOf(now time.Time, prev *Event) time.Time {
	if prev != nil && prev.DecidedAt.After(now) {
		return prev.DecidedAt
	}
	return now
}

// DecisionInput is one purpose's new decision.
type DecisionInput struct {
	Purpose string
	Granted bool
	// DocumentVersion is the version the person was shown.
	DocumentVersion string
	// BasedOn is the id of the purpose's current event as the client last
	// saw it (0 for none). Signed-in decisions only.
	BasedOn int64
}

// DecideInput is a batch of decisions made on one surface.
type DecideInput struct {
	Source    Source
	Decisions []DecisionInput
}

// Decide records decisions: appended to the user's record when signed in,
// or into the cookie for an anonymous visitor. It returns the resulting
// effective consent and the cookie to set. A refusal is a *DecisionError.
func (s *Service) Decide(ctx context.Context, sub *Subject, in *DecideInput) (*Resolved, error) {
	if !in.Source.clientSettable() {
		return nil, refuse(CodeInvalidSource, "unknown source")
	}
	purposes := s.offered(ctx)
	docs, err := s.docStates(ctx, purposes, sub.HasUser)
	if err != nil {
		return nil, err
	}
	offered := map[string]*Purpose{}
	for _, p := range purposes {
		offered[p.ID] = p
	}
	seen := map[string]bool{}
	for _, d := range in.Decisions {
		p := offered[d.Purpose]
		switch {
		case p == nil:
			return nil, refuse(CodeInvalidPurpose, "unknown purpose")
		case seen[p.ID]:
			return nil, refuse(CodeDuplicatePurpose, "a purpose can be decided once per request")
		case p.Necessary:
			return nil, refuse(CodeNecessary, "a necessary purpose can't be changed")
		case p.Kind == Processing && !sub.HasUser:
			return nil, refuse(CodeSignInRequired, "sign in to decide this purpose")
		case d.Granted && p.SaleOrShare && sub.GPC:
			return nil, refuse(CodeGPCActive, "Global Privacy Control is denying this purpose")
		case d.DocumentVersion != docs[p.Document].latest:
			return nil, refuse(CodeDocumentChanged, "the document has changed; reload and decide again")
		}
		seen[p.ID] = true
	}

	now := s.cfg.Clock()
	if !sub.HasUser {
		return s.decideAnonymous(ctx, sub, in, now)
	}
	// A decision can arrive before any GET has merged the visitor's cookie.
	// It is merged in the same unit as the decision, or the cookie rewrite
	// below would drop what the visitor chose while signed out. A purpose
	// decided now isn't merged: the decision is newer than the cookie's.
	merging := sub.Cookie != nil && sub.Cookie.MergeToken != ""
	decided := map[string]bool{}
	for _, d := range in.Decisions {
		decided[d.Purpose] = true
	}
	_, err = s.cfg.Store.Apply(ctx, sub.UserID, func(cur map[string]*Event) (*Plan, error) {
		plan := &Plan{}
		if merging {
			plan.MergeToken = sub.Cookie.MergeToken
			plan.Events = s.mergeEvents(sub, purposes, cur, decided)
		}
		for _, d := range in.Decisions {
			p := offered[d.Purpose]
			c := cur[p.ID]
			var id int64
			if c != nil {
				id = c.ID
			}
			if d.BasedOn != id {
				return nil, refuse(CodeConsentChanged, "consent changed since it was loaded; reload and decide again")
			}
			plan.Events = append(plan.Events, NewEvent{
				Purpose: p.ID, Granted: d.Granted, DocumentKind: p.Document, DocumentVersion: d.DocumentVersion,
				Source: in.Source, DecidedAt: laterOf(now, c), SessionID: sub.SessionID,
			})
		}
		return plan, nil
	})
	if err != nil {
		return nil, err
	}
	eff, err := s.Effective(ctx, sub)
	if err != nil {
		return nil, err
	}
	return &Resolved{Effective: eff, Cookie: s.cookieFrom(eff)}, nil
}

func (s *Service) decideAnonymous(ctx context.Context, sub *Subject, in *DecideInput, now time.Time) (*Resolved, error) {
	token, err := NewMergeToken()
	if err != nil {
		return nil, err
	}
	next := &CookieState{Decisions: map[string]*CookieDecision{}, MergeToken: token, Expires: s.cfg.Cookie.Expiry(now)}
	if sub.Cookie != nil {
		for id, d := range sub.Cookie.Decisions {
			c := *d
			next.Decisions[id] = &c
		}
	}
	for _, d := range in.Decisions {
		next.Decisions[d.Purpose] = &CookieDecision{
			Purpose: d.Purpose, Granted: d.Granted, Version: d.DocumentVersion,
			DecidedAt: now, Source: in.Source, Unmerged: true,
		}
	}
	after := *sub
	after.Cookie = next
	eff, err := s.Effective(ctx, &after)
	if err != nil {
		return nil, err
	}
	return &Resolved{Effective: eff, Cookie: next}, nil
}

// History is the user's whole record, oldest first: the proof of consent.
func (s *Service) History(ctx context.Context, userID uuid.UUID) ([]*Event, error) {
	return s.cfg.Store.History(ctx, userID)
}
