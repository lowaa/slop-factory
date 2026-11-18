package consent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/migrations"
	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/consent/consenttest"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, consenttest.Migrations())
}

func newUser(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := dbtest.AppEngine().ExecContext(context.Background(),
		`INSERT INTO global_users (id, email) VALUES ($1, $2)`, id, id.String()+"@example.test")
	require.NoError(t, err)
	return id
}

func TestSchemaFollowsGlobalTableRules(t *testing.T) {
	for _, err := range migrations.LintGlobalTenantInvariants(consenttest.Migrations()) {
		t.Error(err)
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var (
	jan1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// secret is synthetic: 32 bytes of nothing a real key would be.
	secret = []byte("0123456789abcdef0123456789abcdef")
)

type fixture struct {
	svc   *consent.Service
	store *consent.PostgresStore
	docs  *consenttest.StaticDocuments
	caps  consent.StaticCapabilities
	clock *clock
	codec *consent.CookieCodec
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	reg, err := consent.NewRegistry(
		consent.Purpose{ID: "necessary", Kind: consent.CookieCategory, Necessary: true},
		consent.Purpose{ID: "analytics", Kind: consent.CookieCategory, Document: "cookie_policy"},
		consent.Purpose{ID: "interaction_recording", Kind: consent.CookieCategory, Document: "cookie_policy", Capability: "interaction_recording"},
		consent.Purpose{ID: "marketing", Kind: consent.CookieCategory, Document: "cookie_policy", SaleOrShare: true},
		consent.Purpose{ID: "health_data_processing", Kind: consent.Processing, Document: "health_data_notice"},
	)
	require.NoError(t, err)
	f := &fixture{
		store: consent.NewPostgresStore(dbtest.AppEngine()),
		docs: consenttest.NewStaticDocuments(map[string][]consent.DocVersion{
			"cookie_policy":      {{Version: "v1", Effective: jan1, Material: true}},
			"health_data_notice": {{Version: "v1", Effective: jan1, Material: true}},
		}),
		caps:  consent.StaticCapabilities{"interaction_recording": true},
		clock: &clock{now: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)},
	}
	f.codec, err = consent.NewCookieCodec(&consent.CookieConfig{Name: "app_consent", Secret: secret, Secure: true})
	require.NoError(t, err)
	f.svc, err = consent.NewService(&consent.Config{
		Registry: reg, Capabilities: f.caps, Documents: f.docs, Store: f.store, Cookie: f.codec, Clock: f.clock.Now,
	})
	require.NoError(t, err)
	return f
}

func in(source consent.Source, ds ...consent.DecisionInput) *consent.DecideInput {
	return &consent.DecideInput{Source: source, Decisions: ds}
}

func grant(purpose string, granted bool) consent.DecisionInput {
	return consent.DecisionInput{Purpose: purpose, Granted: granted, DocumentVersion: "v1"}
}
