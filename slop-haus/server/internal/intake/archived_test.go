package intake_test

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
)

type countingLimiter struct{ calls atomic.Int64 }

func (c *countingLimiter) Allow(uuid.UUID) bool { c.calls.Add(1); return true }

// A delivery for an archived tenant is acknowledged (so the sender does not
// retry or give up on its hook) and changes nothing: no ticket, no record,
// and no charge to the source's rate limit. Unarchived, the same post files.
func TestReceive_AnArchivedTenantsPostIsAcknowledgedAndDropped(t *testing.T) {
	cases := map[string]struct {
		spec func() *intake.Spec
		body func() any
	}{
		"native": {
			spec: func() *intake.Spec { return spec(trusted) },
			body: func() any { return event("open", "k1", nil) },
		},
		"alertmanager": {
			spec: func() *intake.Spec {
				return spec(trusted, func(s *intake.Spec) { s.Adapter = "alertmanager" })
			},
			body: func() any { return amFile(t, "firing_single") },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			limiter := &countingLimiter{}
			f := newFixture(t, withLimiter(limiter))
			s := f.source(f.a, c.spec())

			dbtest.ArchiveTenant(t, f.database, f.a.id)
			rec := f.send(s.post(c.body()))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "ignored: this workspace is archived\n", rec.Body.String())
			assert.Empty(t, f.deliveries(f.a))
			assert.Zero(t, limiter.calls.Load(), "an archived tenant's post is not charged to the source")
			all, err := f.tickets.List(f.a.ctx, nil)
			require.NoError(t, err)
			assert.Empty(t, all)

			dbtest.UnarchiveTenant(t, f.database, f.a.id)
			rec = f.send(s.post(c.body()))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "archived")
			assert.NotEmpty(t, f.deliveries(f.a))
			assert.Equal(t, int64(1), limiter.calls.Load())
		})
	}
}

// A revoked source is refused as ever, archived tenant or not: it learns
// nothing of the tenant's state.
func TestReceive_ARevokedSourceOfAnArchivedTenantIsStillUnauthorized(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec(trusted))
	require.NoError(t, f.svc.Revoke(f.a.ctx, f.a.who(), s.id))
	dbtest.ArchiveTenant(t, f.database, f.a.id)

	rec := f.send(s.post(event("open", "k1", nil)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
