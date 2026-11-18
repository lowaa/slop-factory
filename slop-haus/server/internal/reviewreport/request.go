package reviewreport

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RequiredPersonas are the slop-review personas a change is reviewed by: a
// review request stays open until each has a run, and the steward wants all
// of them approving. The supervisor runs one slop-review pass, the standard
// persona over the whole change.
var RequiredPersonas = []string{"standard"}

var (
	// ErrNoHeldAttempt is a caller that holds no attempt to review.
	ErrNoHeldAttempt = errors.New("reviewreport: the caller holds no attempt")
	// ErrNotSupervised is a caller no supervisor runs, so nobody would act on
	// its request.
	ErrNotSupervised = errors.New("reviewreport: no supervisor runs the caller")
	// ErrNoRequest is a findings read that names no head, by an attempt that
	// never requested a review.
	ErrNoRequest = errors.New("reviewreport: no review has been requested for the attempt")
)

// Request is an agent's request that its supervisor review a head of the
// attempt it holds.
type Request struct {
	TicketID    int64
	Attempt     int
	HeadSHA     string
	Intent      string
	RequestedAt time.Time
}

// HeadReview is the runs of one head of an attempt, with their findings.
type HeadReview struct {
	TicketID int64
	Attempt  int
	HeadSHA  string
	// Runs are the runs of that head filed under the attempt, newest first,
	// failed ones included.
	Runs []AttemptRun
	// Truncated says older runs of the head were left out.
	Truncated bool
}

// Label limits in characters, as the REST route's schema states them; the
// MCP tool has no schema limits, so Service.Label holds both doors to them.
const (
	MaxLabelReasonChars = 4096
	MaxFixedInChars     = 256
)

// maxHeadRuns bounds the runs a findings read returns for one head: a
// supervisor posts one per request, so this is many rounds.
const maxHeadRuns = 100

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// RequestReview records the calling agent's request that its supervisor
// review headSHA of the attempt it holds, replacing the attempt's last one.
func (s *Service) RequestReview(ctx context.Context, headSHA, intent string) (*Request, error) {
	caller, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	head := strings.ToLower(strings.TrimSpace(headSHA))
	if !fullSHA.MatchString(head) {
		return nil, fmt.Errorf("%w: head_sha must be the full 40-character commit sha", ErrInvalid)
	}
	if err := checkText("intent", intent, report.MaxTextBytes); err != nil {
		return nil, err
	}
	if strings.TrimSpace(intent) == "" {
		return nil, fmt.Errorf("%w: intent is required: the change's PR description", ErrInvalid)
	}
	return s.store.RequestReview(ctx, caller.AgentID(), head, intent)
}

// OpenRequest is agentID's open request on the attempt, or nil when it has
// none open.
func (s *Service) OpenRequest(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt int) (*Request, error) {
	return s.store.OpenRequest(ctx, agentID, ticketID, attempt)
}

// Findings are the runs of the attempt the caller holds, at headSHA or,
// when it is empty, at the head the attempt last requested a review of.
func (s *Service) Findings(ctx context.Context, headSHA string) (*HeadReview, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	head := strings.TrimSpace(headSHA)
	if head != "" && len(head) < minHeadPrefix {
		return nil, fmt.Errorf("%w: head_sha must be at least %d characters", ErrInvalid, minHeadPrefix)
	}
	if len(head) > maxShaBytes {
		return nil, fmt.Errorf("%w: head_sha is over %d characters", ErrInvalid, maxShaBytes)
	}
	return s.store.HeldAttemptRuns(ctx, caller.AgentID(), head, maxHeadRuns)
}

// maxShaBytes bounds a sha the caller names: long enough for SHA-256.
const maxShaBytes = 64

func checkText(field, v string, maxBytes int) error {
	if len(v) > maxBytes {
		return fmt.Errorf("%w: %s is over %d bytes", ErrInvalid, field, maxBytes)
	}
	return checkStorable(field, v)
}

func checkChars(field, v string, maxChars int) error {
	if utf8.RuneCountInString(v) > maxChars {
		return fmt.Errorf("%w: %s is over %d characters", ErrInvalid, field, maxChars)
	}
	return checkStorable(field, v)
}

func checkStorable(field, v string) error {
	if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return fmt.Errorf("%w: %s holds text that cannot be stored (a NUL character or invalid encoding)", ErrInvalid, field)
	}
	return nil
}
