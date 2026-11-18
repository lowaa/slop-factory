package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// AssessmentReader adapts the steward store to pendingaction.Assessments.
type AssessmentReader struct {
	store *steward.Store
	// settings supplies the tenant's veto window, read only for a veto verdict.
	settings steward.Settings
}

func newAssessmentReader(store *steward.Store, settings steward.Settings) *AssessmentReader {
	return &AssessmentReader{store: store, settings: settings}
}

func (r *AssessmentReader) Assessments(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*pendingaction.AssessmentView, error) {
	recs, err := r.store.GetByActions(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*pendingaction.AssessmentView, len(recs))
	veto := -1
	for id, rec := range recs {
		v := &pendingaction.AssessmentView{
			Category: rec.Category, Tier: string(rec.Tier), Verdict: string(rec.Verdict), Mode: string(rec.Mode), Reason: rec.Reason,
		}
		for _, e := range rec.Evidence {
			v.Evidence = append(v.Evidence, pendingaction.AssessmentEvidence{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
		}
		if rec.Verdict == steward.VerdictWouldVeto {
			if veto < 0 {
				cfg, err := r.settings.StewardConfig(ctx)
				if err != nil {
					return nil, fmt.Errorf("steward: read veto window: %w", err)
				}
				veto = cfg.VetoMinutes
			}
			v.VetoMinutes = veto
		}
		out[id] = v
	}
	return out, nil
}
