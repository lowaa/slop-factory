// Package llmusagetest holds what an app needs to test its use of
// llmusage: StoreSuite, the tests every Store implementation must pass,
// and MemStore, an in-memory Store for tests of code that uses a Service.
package llmusagetest

import (
	"context"
	"sync"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

// MemStore is an in-memory llmusage.Store. It passes StoreSuite.
type MemStore struct {
	mu   sync.Mutex
	data map[llmusage.TranscriptKey][]llmusage.Record
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{data: map[llmusage.TranscriptKey][]llmusage.Record{}}
}

// Upsert implements llmusage.Store.
func (s *MemStore) Upsert(_ context.Context, key llmusage.TranscriptKey, records []llmusage.Record) ([]llmusage.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data[key]
	s.data[key] = cloneAll(records)
	return previous, nil
}

func cloneAll(records []llmusage.Record) []llmusage.Record {
	out := make([]llmusage.Record, len(records))
	for i := range records {
		out[i] = *records[i].Clone()
	}
	return out
}
