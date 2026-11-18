package llmusagetest

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

func TestStoreSuite_MemStore(t *testing.T) {
	StoreSuite(t, func(*testing.T) llmusage.Store { return NewMemStore() })
}

// addingStore adds a report to what is stored instead of replacing it: the
// double count StoreSuite exists to catch.
type addingStore struct{ *MemStore }

func (s *addingStore) Upsert(ctx context.Context, key llmusage.TranscriptKey, records []llmusage.Record) ([]llmusage.Record, error) {
	s.mu.Lock()
	previous := cloneAll(s.data[key])
	s.mu.Unlock()
	merged := cloneAll(records)
	for i := range merged {
		for _, p := range previous {
			if p.Key() == merged[i].Key() {
				merged[i].Requests += p.Requests
			}
		}
	}
	_, err := s.MemStore.Upsert(ctx, key, merged)
	return previous, err
}

// racyStore reads the previous totals and writes the new ones in two steps.
// The first two upserts of a concurrent run meet between the steps, so
// both deterministically see the same previous totals.
type racyStore struct {
	*MemStore
	calls int
	meet  sync.WaitGroup
}

func newRacyStore() *racyStore {
	s := &racyStore{MemStore: NewMemStore()}
	s.meet.Add(2)
	return s
}

func (s *racyStore) Upsert(ctx context.Context, key llmusage.TranscriptKey, records []llmusage.Record) ([]llmusage.Record, error) {
	s.mu.Lock()
	previous := cloneAll(s.data[key])
	s.calls++
	meets := s.calls <= 2
	s.mu.Unlock()
	if meets {
		s.meet.Done()
		s.meet.Wait()
	}
	s.mu.Lock()
	s.data[key] = cloneAll(records)
	s.mu.Unlock()
	return previous, nil
}

// brokenStoreEnv picks the broken store a child run of the suite uses.
const brokenStoreEnv = "LLMUSAGETEST_BROKEN_STORE"

// TestStoreSuite_CatchesBrokenStores runs the suite against broken stores
// in a child test process, since a failing suite would fail this test too,
// and checks the subtest meant to catch each one fails.
func TestStoreSuite_CatchesBrokenStores(t *testing.T) {
	switch os.Getenv(brokenStoreEnv) {
	case "adding":
		StoreSuite(t, func(*testing.T) llmusage.Store { return &addingStore{MemStore: NewMemStore()} })
		return
	case "racy":
		// Only the concurrency subtest: the rendezvous would block the
		// sequential ones forever.
		StoreSuite(t, func(t *testing.T) llmusage.Store {
			if !strings.HasSuffix(t.Name(), "/ConcurrentUpsertsSeeAConsistentPrevious") {
				t.Skip("racy store is only for the concurrency subtest")
			}
			return newRacyStore()
		})
		return
	}

	for broken, failing := range map[string]string{
		"adding": "OverwriteReplaces",
		"racy":   "ConcurrentUpsertsSeeAConsistentPrevious",
	} {
		t.Run(broken, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStoreSuite_CatchesBrokenStores$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(), brokenStoreEnv+"="+broken)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "the suite passed a %s store:\n%s", broken, out)
			assert.Contains(t, string(out), "--- FAIL: TestStoreSuite_CatchesBrokenStores/"+failing)
		})
	}
}
