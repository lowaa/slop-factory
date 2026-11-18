package dsr_test

import (
	"sync"
	"testing"
	"time"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/migrations"
	"github.com/lowaa/slop-factory/go-privacy/dsr"
	"github.com/lowaa/slop-factory/go-privacy/dsr/dsrtest"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, dsrtest.Migrations())
}

func TestSchemaFollowsGlobalTableRules(t *testing.T) {
	for _, err := range migrations.LintGlobalTenantInvariants(dsrtest.Migrations()) {
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

type fixture struct {
	svc   *dsr.Service
	store *dsr.PostgresStore
	clock *clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		store: dsr.NewPostgresStore(dbtest.AppEngine()),
		clock: &clock{now: time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)},
	}
	var err error
	f.svc, err = dsr.NewService(&dsr.Config{Store: f.store, Clock: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
