package consent

import (
	"context"
	"time"
)

// PurposeState is one purpose as a request sees it.
type PurposeState struct {
	Purpose *Purpose
	// Granted is the effective answer: whether what the purpose governs may
	// happen for this request.
	Granted bool
	// Decided is whether the person has a decision on record for it, and
	// DecisionGranted, DecisionVersion, DecidedAt, EventID and Source say
	// what it is. EventID is the record's event id, 0 for an anonymous
	// visitor (whose decision lives in the cookie).
	Decided         bool
	DecisionGranted bool
	DecisionVersion string
	DecidedAt       time.Time
	EventID         int64
	Source          Source
	// NeedsDecision is the re-prompt: no decision, or one under a document
	// version older than the latest material one.
	NeedsDecision bool
	// DocumentVersion is the version a new decision has to cite.
	DocumentVersion string
	// GPCLocked means Global Privacy Control is denying the purpose.
	GPCLocked bool
}

// Effective is the consent in force for one request.
type Effective struct {
	// States holds the purposes on offer, in declaration order: those whose
	// capability is off are absent, and for an anonymous visitor so are the
	// processing purposes.
	States   []*PurposeState
	GPC      bool
	SignedIn bool
	byID     map[string]*PurposeState
}

// Granted reports whether purpose is effectively granted. An unknown or
// switched-off purpose is not.
func (e *Effective) Granted(purpose string) bool {
	if e == nil {
		return false
	}
	s := e.byID[purpose]
	return s != nil && s.Granted
}

// State returns purpose's state, or nil if it isn't on offer.
func (e *Effective) State(purpose string) *PurposeState {
	if e == nil {
		return nil
	}
	return e.byID[purpose]
}

// NeedingDecision returns the purposes that need a decision.
func (e *Effective) NeedingDecision() []*PurposeState {
	var out []*PurposeState
	for _, s := range e.States {
		if s.NeedsDecision {
			out = append(out, s)
		}
	}
	return out
}

type decision struct {
	granted bool
	version string
	at      time.Time
	eventID int64
	source  Source
}

// evaluate is the rule of README §5, "Effective consent":
//
//	necessary OR (current decision is a grant AND it cites a document version
//	at or after the latest material one AND NOT (SaleOrShare AND GPC))
//
// with the capability gate applied by the caller's choice of purposes.
func evaluate(purposes []*Purpose, decisions map[string]*decision, docs map[string]*docState, gpc, signedIn bool) *Effective {
	e := &Effective{GPC: gpc, SignedIn: signedIn, byID: map[string]*PurposeState{}}
	for _, p := range purposes {
		if p.Kind == Processing && !signedIn {
			continue
		}
		cp := *p // a copy: a caller holding the state can't edit the policy
		s := &PurposeState{Purpose: &cp, GPCLocked: p.SaleOrShare && gpc}
		if p.Necessary {
			s.Granted = true
		} else {
			ds := docs[p.Document]
			s.DocumentVersion = ds.latest
			d := decisions[p.ID]
			if d != nil {
				s.Decided = true
				s.DecisionGranted, s.DecisionVersion, s.DecidedAt, s.EventID, s.Source = d.granted, d.version, d.at, d.eventID, d.source
			}
			s.NeedsDecision = d == nil || !ds.current(d.version)
			s.Granted = !s.NeedsDecision && d.granted && !s.GPCLocked
		}
		e.States = append(e.States, s)
		e.byID[p.ID] = s
	}
	return e
}

type ctxKey struct{}

// WithEffective puts the consent in force on ctx.
func WithEffective(ctx context.Context, e *Effective) context.Context {
	return context.WithValue(ctx, ctxKey{}, e)
}

// FromContext returns the consent the middleware put on ctx. Without the
// middleware it is nil, which grants nothing.
func FromContext(ctx context.Context) *Effective {
	e, _ := ctx.Value(ctxKey{}).(*Effective)
	return e
}
