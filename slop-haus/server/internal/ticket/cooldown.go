package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidRetryAfter is returned for a negative retry_after.
var ErrInvalidRetryAfter = errors.New("ticket: retry_after must not be negative")

// CooldownPolicy bounds how long a release can hold a ticket out of the
// queue, and how many times in a row it may.
//
// The zero value is no cooldown at all: a release requeues at once, as it
// did before cooldowns. DefaultCooldownPolicy is what the server runs with.
type CooldownPolicy struct {
	// Min and Max clamp a requested retry_after. A release that asks for
	// none gets Min, which is what stops a claim/release loop whatever an
	// agent does. Max == 0 turns cooldowns off.
	Min, Max time.Duration
	// EscalateAfter is how many back-to-back cooldown releases a ticket gets:
	// the release that makes this many moves the ticket to discussion instead
	// of cooling it again. Zero never escalates on count.
	EscalateAfter int
	// MaxTotal is how long after a streak's first cooldown release a further
	// one is still allowed. Zero never escalates on time.
	MaxTotal time.Duration
}

// DefaultCooldownPolicy is the policy an unconfigured server runs.
func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{Min: 5 * time.Minute, Max: 24 * time.Hour, EscalateAfter: 3, MaxTotal: 72 * time.Hour}
}

func (p CooldownPolicy) enabled() bool { return p.Max > 0 }

// clamp is the cooldown a release asking for requested gets, zero for none.
func (p CooldownPolicy) clamp(requested time.Duration) time.Duration {
	if !p.enabled() {
		return 0
	}
	d := requested
	if d < p.Min {
		d = p.Min
	}
	return min(d, p.Max)
}

// CooldownStreak is a ticket's run of back-to-back cooldown releases.
//
// The streak is the ticket's, not an attempt's, and any other status write
// resets it: so one attempt's exit can reset the streak another attempt's
// releases were building. Accepted, because that errs towards more checks of
// a ticket, not fewer. An escalation moves the releasing attempt to
// discussion, not the ticket; the ticket's status follows from its attempts.
type CooldownStreak struct {
	Releases int
	Since    *time.Time
}

// CooldownWrite is a release's cooldown on the ticket's row.
type CooldownWrite struct {
	Until  time.Time
	Streak CooldownStreak
}

// ReleaseParams is an agent handing its ticket back.
type ReleaseParams struct {
	Reason  string
	Handoff string
	// BlockedOn is a ticket the released one waits on.
	BlockedOn *int64
	// RetryAfter is how long to hold the ticket out of the queue before it is
	// claimable again, clamped by the policy. Zero asks for the shortest.
	RetryAfter time.Duration
}

// ReleaseOutcome is what a release did to the ticket.
type ReleaseOutcome struct {
	Ticket *Ticket
	// RetryAt is when a cooled ticket becomes claimable again, nil when it
	// was not cooled.
	RetryAt *time.Time
	// Escalated is set when the ticket went to discussion instead of cooling:
	// Checks cooldown releases in a row had not cleared its precondition.
	Escalated bool
	Checks    int
}

// cooldownNow is the time a cooldown is measured from: the database's, which
// the expiry check and the sweep compare it with, so API pods whose clocks
// differ cannot shorten or stretch one. A test replaces it with WithClock to
// make a cooldown that has already ended.
func (s *Service) cooldownNow(ctx context.Context, tx TransitionTx) (time.Time, error) {
	if s.now != nil {
		return s.now(), nil
	}
	var now time.Time
	if err := tx.Querier().QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("read the database clock: %w", err)
	}
	return now, nil
}

// ReleaseWith hands the agent's ticket back. See releaseExit for what it does
// with the cooldown.
func (s *Service) ReleaseWith(ctx context.Context, agentID uuid.UUID, p ReleaseParams) (*ReleaseOutcome, error) {
	if p.RetryAfter < 0 {
		return nil, ErrInvalidRetryAfter
	}
	out := &ReleaseOutcome{}
	err := s.store.OnHeldLease(ctx, agentID, func(tx TransitionTx, lease *HeldLease) error {
		streak, err := tx.CooldownStreak(ctx, lease.TicketID)
		if err != nil {
			return err
		}
		now, err := s.cooldownNow(ctx, tx)
		if err != nil {
			return err
		}
		*out = ReleaseOutcome{}
		out.Ticket, err = decideAndApply(ctx, tx, lease, lease.TicketID, func(*Ticket) (*Change, error) {
			return releaseExit(&p, s.cooldown, streak, now, out)(nil, agentID)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// escalates is whether this release, the one that makes next, should go to
// discussion instead of cooling. The time rule is about how long the streak
// has already run, not how long this release asks to wait.
func escalates(p CooldownPolicy, next CooldownStreak, now time.Time) bool {
	if p.EscalateAfter > 0 && next.Releases >= p.EscalateAfter {
		return true
	}
	return p.MaxTotal > 0 && next.Since != nil && now.Sub(*next.Since) > p.MaxTotal
}

func escalationComment(reason string, checks int, streak CooldownStreak, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Moved to discussion: the precondition this ticket waits on was still unmet after %d checks", checks)
	if streak.Since != nil {
		fmt.Fprintf(&b, " over %s", now.Sub(*streak.Since).Round(time.Minute))
	}
	b.WriteString(". Re-checking on a timer will not clear it; a person needs to look.")
	if said := strings.TrimSpace(reason); said != "" {
		b.WriteString("\n\nLast release reason: " + said)
	}
	return b.String()
}

func cooldownNote(until time.Time) string {
	return fmt.Sprintf("Cooling down: not claimable again until %s UTC.", until.UTC().Format("15:04 on 2 Jan"))
}
