package ticket

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// AcquireIntegrationLock takes the merge lock. No lock name is exposed: a
// lock whose lifetime derives from a ticket lease only makes sense for a
// holder that has one, and a general lock_acquire(name) would let an agent
// create a lock with no lease behind it and no way to ever expire. Which lock
// it is follows from the base branch of the lease's ticket (LockNameFor).
func (s *Service) AcquireIntegrationLock(ctx context.Context, agentID uuid.UUID) error {
	name, err := s.lockNameOf(ctx, agentID)
	if err != nil {
		return err
	}
	return s.store.AcquireLock(ctx, agentID, name)
}

// AwaitIntegrationLock is AcquireIntegrationLock that stops at reserving the
// lock: see PostgresStore.AwaitLock.
func (s *Service) AwaitIntegrationLock(ctx context.Context, agentID uuid.UUID) error {
	name, err := s.lockNameOf(ctx, agentID)
	if err != nil {
		return err
	}
	return s.store.AwaitLock(ctx, agentID, name)
}

// IntegrationLockState is the holder and queue the board shows: the tenant's
// default lock.
func (s *Service) IntegrationLockState(ctx context.Context) (*LockState, error) {
	return s.store.LockState(ctx, IntegrationLockName)
}

// NamedLockState is one of the tenant's locks with its holder and queue.
type NamedLockState struct {
	Name string
	*LockState
}

// IntegrationLocks is every lock the tenant has: the default one always, then
// each per-branch lock that has a holder or a queue.
func (s *Service) IntegrationLocks(ctx context.Context) ([]NamedLockState, error) {
	names, err := s.store.LockNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NamedLockState, 0, len(names)+1)
	for _, name := range append([]string{IntegrationLockName}, names...) {
		if name == IntegrationLockName && len(out) > 0 {
			continue
		}
		state, err := s.store.LockState(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, NamedLockState{Name: name, LockState: state})
	}
	return out, nil
}

// knownLock resolves the name a person addresses a lock by: empty is the
// default lock, anything else must be a lock this tenant has a holder or a
// queue on. A name is never taken on trust: it is an input of the console.
func (s *Service) knownLock(ctx context.Context, name string) (string, error) {
	if name == "" || name == IntegrationLockName {
		return IntegrationLockName, nil
	}
	names, err := s.store.LockNames(ctx)
	if err != nil {
		return "", err
	}
	if !slices.Contains(names, name) {
		return "", ErrUnknownLock
	}
	return name, nil
}

// IntegrationLockStateNamed is IntegrationLockState for a named lock.
func (s *Service) IntegrationLockStateNamed(ctx context.Context, name string) (*LockState, error) {
	name, err := s.knownLock(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.store.LockState(ctx, name)
}

// IntegrationLockStateFor is the lock, and queue, the agent's ticket merges
// under.
func (s *Service) IntegrationLockStateFor(ctx context.Context, agentID uuid.UUID) (*LockState, error) {
	name, err := s.lockNameOf(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return s.store.LockState(ctx, name)
}

// ForceReleaseNamedLock is a person's release of the named lock.
func (s *Service) ForceReleaseNamedLock(ctx context.Context, name string) (*LockHolder, error) {
	name, err := s.knownLock(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.store.ForceReleaseLock(ctx, name)
}

// DropNamedLockWaiter is a person's removal of an agent from the named lock's
// queue.
func (s *Service) DropNamedLockWaiter(ctx context.Context, name string, agentID uuid.UUID) error {
	name, err := s.knownLock(ctx, name)
	if err != nil {
		return err
	}
	return s.store.DropLockWaiter(ctx, name, agentID)
}

// ReleaseIntegrationLock releases the agent's lock. Releasing one a merge
// already freed (ending the lease drops it) succeeds, so an agent that still
// unlocks after change_merge is not told it failed.
func (s *Service) ReleaseIntegrationLock(ctx context.Context, agentID uuid.UUID) error {
	err := s.store.ReleaseLock(ctx, agentID)
	if errors.Is(err, ErrNoSuchLock) {
		if snap, serr := s.store.WorkerSnapshot(ctx, agentID); serr == nil && !snap.Active && snap.TicketStatus == StatusDone {
			return nil
		}
	}
	return err
}

// lockNameOf is the integration lock the agent's ticket merges under. It
// reads the manifest only, never the code host: the lock is taken and queued
// for often and under no network wait.
func (s *Service) lockNameOf(ctx context.Context, agentID uuid.UUID) (string, error) {
	if s.workspace == nil {
		return IntegrationLockName, nil
	}
	lease, err := s.store.LeaseFor(ctx, agentID)
	if err != nil {
		return "", err
	}
	t, err := s.store.GetTicket(ctx, lease.TicketID)
	if err != nil {
		return "", err
	}
	pl, err := s.workspace.Place(ctx, scopePaths(t.Paths))
	if err != nil {
		return "", fmt.Errorf("place the ticket: %w", err)
	}
	if !pl.Declared || len(pl.Unplaced) > 0 || len(pl.Repos) != 1 {
		return IntegrationLockName, nil
	}
	rb, _ := manifestBase(ctx, &BaseQuery{Manifest: &pl.Repos[0]})
	return LockNameFor(pl.Repos[0].Path, rb), nil
}
