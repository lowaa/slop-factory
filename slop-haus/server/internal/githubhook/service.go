package githubhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is what a delivery can do to tickets: the board's own review
// and merge rules, found by PR rather than by ticket number.
type Tickets interface {
	ApprovePR(ctx context.Context, r *ticket.PRReview) (*ticket.Ticket, error)
	RequestChangesPR(ctx context.Context, r *ticket.PRReview) (*ticket.Ticket, error)
	CloseMergedPR(ctx context.Context, pr codehost.ChangeRef, userID uuid.UUID) (*ticket.Ticket, error)
	// UnpromotedAttemptForPR is the attempt whose change pr is, when only
	// unpromoted attempts report it; nil when pr is anything else.
	UnpromotedAttemptForPR(ctx context.Context, pr codehost.ChangeRef) (*ticket.AttemptRef, error)
	// RecordIgnoredMerge keeps a merge this door did not act on against the
	// live tickets whose change it is, so a person can be told why they are
	// still open.
	RecordIgnoredMerge(ctx context.Context, m *ticket.IgnoredMerge) error
	MergedIntoBase(ctx context.Context, attempt ticket.AttemptRef, pr codehost.ChangeRef, merger ticket.Merger) (*ticket.Ticket, error)
}

// Config is the webhook door's setup. A nil AppSecrets is the door shut.
type Config struct {
	// AppSecrets opens the GitHub App's webhook secrets.
	AppSecrets AppSecrets
	// Host turns an authenticated delivery's payload into what happened.
	// Required, and chosen by the wiring: the signature scheme and headers
	// around it are GitHub's whichever Host is set, so another is for tests
	// until its own door exists.
	Host codehost.Host
	// Archive may be nil, which sees no tenant as archived. A signed
	// delivery for an archived tenant is acknowledged and dropped.
	Archive tenantstate.Archive
}

// AppReceivePath is where GitHub App deliveries arrive. It names no tenant:
// the payload's installation does. The App configuration that tells GitHub
// where to send them (codehost/githubapp) keeps its own copy of this path.
const AppReceivePath = "/hooks/github-app"

// recentDeliveries is how many deliveries the board lists.
const recentDeliveries = 50

// Limits on what a delivery's headers put in the database, matching the
// schema's CHECKs. GitHub's are far shorter.
const (
	maxHeaderValue = 100
	maxDetail      = 1000
	// maxComment bounds a review's body as a ticket comment: GitHub's own
	// limit is 65536 characters, which a person's review never nears.
	maxComment = 65536
)

type Service struct {
	store   Store
	tickets Tickets
	cfg     *Config
	host    codehost.Host
	logger  *slog.Logger
}

func NewService(store Store, tickets Tickets, cfg *Config, logHandler slog.Handler) (*Service, error) {
	if cfg.Host == nil {
		return nil, errors.New("githubhook: Config.Host is required")
	}
	return &Service{store: store, tickets: tickets, cfg: cfg, host: cfg.Host, logger: slog.New(logHandler)}, nil
}

// Enabled reports whether the door is open: there are App secrets to check
// deliveries against.
func (s *Service) Enabled() bool { return s.cfg.AppSecrets != nil }

// Deliveries is the tenant's recent deliveries, newest first.
func (s *Service) Deliveries(ctx context.Context) ([]Delivery, error) {
	return s.store.ListDeliveries(ctx, recentDeliveries)
}

// Incoming is a delivery as it arrived, before anything is trusted.
type Incoming struct {
	Event       string
	DeliveryID  string
	Signature   string
	ContentType string
	Body        []byte
}

// Receipt is what the receiver answers GitHub with. GitHub shows it on the
// webhook's Recent Deliveries page, so it says what happened.
type Receipt struct {
	Duplicate bool
	Outcome   Outcome
	Detail    string
}

// process records and handles a delivery that is authentic and whose tenant
// is known.
func (s *Service) process(ctx context.Context, tenant uuid.UUID, d *Delivery, in *Incoming) (*Receipt, error) {
	ctx = db.WithTenant(ctx, tenant)
	begun, err := s.store.BeginDelivery(ctx, d)
	if err != nil {
		return nil, err
	}
	if !begun {
		return &Receipt{Duplicate: true}, nil
	}

	if err := s.handle(ctx, d, in); err != nil {
		d.Outcome, d.Detail, d.TicketID, d.AttemptNumber = OutcomeError, "internal error; redeliver from GitHub to retry", nil, nil
		// Recorded even when ctx is what failed, or the delivery stays
		// "processing" and a redelivery is turned away as a duplicate.
		if ferr := s.store.FinishDelivery(context.WithoutCancel(ctx), d); ferr != nil {
			s.logger.ErrorContext(ctx, "record failed github delivery", "delivery", d.ID, "err", ferr)
		}
		return nil, err
	}
	d.Detail = hookkit.Clean(d.Detail, maxDetail)
	if finishBarrier != nil {
		finishBarrier()
	}
	// Recorded even if the request is cancelled now: the ticket has
	// already moved, and a record left processing would turn GitHub's
	// redelivery away for a while (see BeginDelivery).
	if err := s.store.FinishDelivery(context.WithoutCancel(ctx), d); err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "github delivery", "tenant", tenant, "installation", d.InstallationID,
		"delivery", d.ID, "event", d.Event, "action", d.Action, "outcome", d.Outcome, "detail", d.Detail, "ticket", d.TicketID)
	return &Receipt{Outcome: d.Outcome, Detail: d.Detail}, nil
}

// ReceiveApp authenticates a delivery from the tenant's GitHub App and acts
// on it, in the tenant's own transaction, in which row level security keeps
// the delivery to that tenant's tickets whatever it says. A delivery GitHub
// sends again is answered as a duplicate unless the first attempt failed on
// our side.
//
// The delivery names no tenant, only the installation it is about (read
// from the body, which is not yet trusted). That picks the candidate
// tenants, the one global read, and the signature must verify against the
// webhook secret of one of them before anything else is done. A delivery for
// an installation nobody here has connected is answered as ignored, not
// refused: GitHub sends an App's every installation's events to its one
// URL, and an environment sharing the App with another gets the other's.
// Anything for a known installation with a wrong or missing signature is
// ErrUnauthorized, giving nothing away.
func (s *Service) ReceiveApp(ctx context.Context, in *Incoming) (*Receipt, error) {
	if !s.Enabled() {
		return nil, ErrUnauthorized
	}
	installation := installationOf(in.Body)
	if installation == 0 {
		return &Receipt{Outcome: OutcomeIgnored, Detail: "the delivery names no installation"}, nil
	}
	candidates, err := s.store.AppInstallations(ctx, installation)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return &Receipt{Outcome: OutcomeIgnored, Detail: "no GitHub App installation here"}, nil
	}
	tenant, ok := s.signedBy(ctx, candidates, in)
	if !ok {
		return nil, ErrUnauthorized
	}
	// After the signature, so only GitHub can learn the tenant is archived,
	// and answered 200: a refusal would have GitHub retry and then disable
	// the App's hook for every tenant sharing it.
	if s.cfg.Archive != nil {
		archived, err := s.cfg.Archive.ArchivedTenant(ctx, tenant)
		if err != nil {
			return nil, err
		}
		if archived {
			s.logger.InfoContext(ctx, "dropped a github delivery for an archived tenant", "tenant", tenant,
				"installation", installation, "delivery", in.DeliveryID, "event", in.Event)
			return &Receipt{Outcome: OutcomeIgnored, Detail: "this workspace is archived"}, nil
		}
	}
	if !hookkit.ValidHeader(in.DeliveryID, maxHeaderValue) || !hookkit.ValidHeader(in.Event, maxHeaderValue) {
		return nil, fmt.Errorf("%w: missing or oversized X-GitHub-Event or X-GitHub-Delivery", ErrBadDelivery)
	}
	return s.process(ctx, tenant, &Delivery{ID: in.DeliveryID, InstallationID: installation, Event: in.Event, Action: actionOf(in.Body)}, in)
}

// signedBy is the candidate whose webhook secret signed the delivery. A
// secret that cannot be opened (a retired key, a damaged row) rules its
// tenant out, loudly, rather than failing every other candidate's delivery.
func (s *Service) signedBy(ctx context.Context, candidates []AppInstallation, in *Incoming) (uuid.UUID, bool) {
	for i := range candidates {
		c := &candidates[i]
		secret, err := s.cfg.AppSecrets.Open(c.Tenant, c.CredentialID, c.SecretEnc)
		if err != nil {
			s.logger.ErrorContext(ctx, "cannot open a GitHub App webhook secret", "tenant", c.Tenant, "err", err)
			continue
		}
		if hookkit.VerifySignature(secret, in.Body, in.Signature) {
			return c.Tenant, true
		}
	}
	return uuid.Nil, false
}

// handle decides a delivery's outcome, recording it on d. An error is ours,
// not the delivery's: anything wrong with the delivery itself, or anything
// the ticket rules refuse, is an outcome of ignored, with the reason.
func (s *Service) handle(ctx context.Context, d *Delivery, in *Incoming) error {
	if mediaType, _, err := mime.ParseMediaType(in.ContentType); err != nil || mediaType != "application/json" {
		d.Outcome, d.Detail = OutcomeIgnored, "set the webhook's content type to application/json"
		return nil
	}
	switch in.Event {
	case "ping":
		d.Outcome, d.Detail = OutcomeApplied, "ping: GitHub reached this webhook"
		return nil
	}
	return s.handleEvent(ctx, d, in)
}

func (s *Service) handleEvent(ctx context.Context, d *Delivery, in *Incoming) error {
	ev := s.host.ParseEvent(in.Event, in.Body)
	switch ev.Kind {
	case codehost.EventReview:
		return s.handleReview(ctx, d, ev)
	case codehost.EventMerged:
		return s.handleMerged(ctx, d, ev)
	}
	d.Outcome, d.Detail = OutcomeIgnored, ev.Ignore
	return nil
}

func (s *Service) handleReview(ctx context.Context, d *Delivery, ev *codehost.Event) error {
	reviewer, err := s.member(ctx, d, &ev.Actor, "reviewer")
	if reviewer == nil || err != nil {
		return err
	}

	r := &ticket.PRReview{PR: ev.Change, UserID: *reviewer, SubmittedAt: ev.SubmittedAt}
	// The review's own link is its identity, the same whichever webhook
	// reports it. The verdict, reviewer and time ride along because a link
	// alone is only as unique as the payload that made it, and a second
	// review that differs in any of them is a different review.
	key := ""
	if ev.URL != "" {
		key = fmt.Sprintf("review:%s|%d|%d|%d", ev.URL, ev.Verdict, ev.Actor.ID, ev.SubmittedAt.Unix())
	}
	return s.once(ctx, d, key, func() error {
		if ev.Verdict == codehost.VerdictApproved {
			t, err := s.tickets.ApprovePR(ctx, r)
			return s.applied(d, "approved", t, err)
		}
		// The body is often empty -- a review that is all inline comments --
		// so the comment always links to the review itself.
		r.Comment = hookkit.Clean(strings.TrimSpace(strings.TrimSpace(ev.Body)+"\n\nChanges requested on GitHub: "+ev.URL), maxComment)
		t, err := s.tickets.RequestChangesPR(ctx, r)
		return s.applied(d, "sent back for changes", t, err)
	})
}

func (s *Service) handleMerged(ctx context.Context, d *Delivery, ev *codehost.Event) error {
	// An experiment's merge into its base branch is checked before who made
	// it: a bot, an outside collaborator or an unlinked account is exactly
	// where a mistaken merge comes from, and it is the safeguard that tells
	// a person. Main is not at risk, so no membership is needed.
	unpromoted, err := s.tickets.UnpromotedAttemptForPR(ctx, ev.Change)
	if err != nil {
		return err
	}
	if unpromoted != nil {
		return s.handleMergedUnpromoted(ctx, d, ev, unpromoted)
	}
	merger, err := s.member(ctx, d, &ev.Actor, "merger")
	if err != nil {
		return err
	}
	if merger != nil {
		// A merge happens once and the ticket rules already see it again: a
		// ticket it closed is no longer mergeable, and an attempt's change it
		// consumed is no longer live (ErrNotMergeable, ignored). So, unlike a
		// review, it needs no key of its own.
		t, err := s.tickets.CloseMergedPR(ctx, ev.Change, *merger)
		if err := s.applied(d, "closed as merged", t, err); err != nil {
			return err
		}
	}
	if d.Outcome != OutcomeIgnored {
		return nil
	}
	// A merge nothing was done for leaves the ticket open, and nothing on the
	// board would say why. The recorded merge decides nothing: closing on it
	// is a person's act, which asks the host again.
	return s.tickets.RecordIgnoredMerge(ctx, &ticket.IgnoredMerge{
		PR: ev.Change, MergeSHA: hookkit.Clean(ev.MergeSHA, maxHeaderValue),
		MergerLogin: hookkit.Clean(ev.Actor.Login, maxHeaderValue), MergerGitHubID: ev.Actor.ID,
		Reason: d.Detail,
	})
}

func (s *Service) handleMergedUnpromoted(ctx context.Context, d *Delivery, ev *codehost.Event, attempt *ticket.AttemptRef) error {
	// An experiment's PR targets the ticket's own base branch; a merge into
	// that is the one thing worth telling a person. A merge into anything
	// else is not something slop-haus opened the PR for, so it stays
	// ignored, with the branch it names.
	if want := ticket.UnpromotedBase(attempt.TicketID); ev.Base != want {
		d.TicketID, d.AttemptNumber = &attempt.TicketID, &attempt.Number
		d.Outcome = OutcomeIgnored
		d.Detail = fmt.Sprintf("#%d attempt %d is not promoted, and the merge was into %q, not its base branch %q",
			attempt.TicketID, attempt.Number, hookkit.Clean(ev.Base, maxHeaderValue), want)
		return nil
	}
	userID, err := s.store.MemberForGitHubUser(ctx, ev.Actor.ID)
	if err != nil {
		return err
	}
	merger := ticket.Merger{UserID: userID, GitHubLogin: hookkit.Clean(ev.Actor.Login, maxHeaderValue), GitHubID: ev.Actor.ID}
	t, err := s.tickets.MergedIntoBase(ctx, *attempt, ev.Change, merger)
	return s.applied(d, "merged into its base branch; attempt sent to discussion", t, err)
}

// once runs apply for the review named by key unless a delivery
// already applied it. A review can reach one tenant in more than one
// delivery, and the second must find the first's work done. The ticket
// rules refuse most repeats on their own (a ticket no longer in review), but
// not all: a ticket sent back and put into review again would take the old
// review a second time.
//
// An empty key is an event with no identity to dedupe by.
//
// The key stays only if apply recorded the delivery as applied; any other
// outcome, or an error, gives it up so a redelivery can still act.
func (s *Service) once(ctx context.Context, d *Delivery, key string, apply func() error) error {
	if key == "" {
		return apply()
	}
	claimed, err := s.store.ClaimEvent(ctx, key, d.ID)
	if err != nil {
		return err
	}
	if !claimed {
		d.Outcome, d.Detail = OutcomeIgnored, "already applied from another delivery"
		return nil
	}
	err = apply()
	if ferr := s.store.FinishEvent(context.WithoutCancel(ctx), key, d.ID, err == nil && d.Outcome == OutcomeApplied); ferr != nil && err == nil {
		return ferr
	}
	return err
}

// member is the tenant member who is GitHub user u. None is an outcome,
// recorded on d -- the delivery is ignored -- not an error: an agent's bot
// account is never linked, and nor is anyone who has not linked theirs.
func (s *Service) member(ctx context.Context, d *Delivery, u *codehost.Actor, role string) (*uuid.UUID, error) {
	found, err := s.store.MemberForGitHubUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if found == nil {
		d.Outcome = OutcomeIgnored
		d.Detail = fmt.Sprintf("the %s, GitHub user %s (id %d), is not a member here with that GitHub account linked",
			role, hookkit.Clean(u.Login, maxHeaderValue), u.ID)
	}
	return found, nil
}

// ignorable are the ticket rules' refusals: the delivery was fine, and
// there was just nothing for it to do.
var ignorable = []error{
	ticket.ErrNoTicketForPR, ticket.ErrAmbiguousPR, ticket.ErrStaleReview,
	ticket.ErrNotInReview, ticket.ErrNotMergeable, ticket.ErrEmptyComment,
	ticket.ErrAttemptNotPromoted,
}

// finishBarrier is a test-only seam, nil in production: it runs after a
// delivery is handled and before it is recorded, so a test can move the
// ticket's promotion there and show the delivery keeps the attempt it matched.
var finishBarrier func()

// applied records a ticket call's result on d.
func (s *Service) applied(d *Delivery, what string, t *ticket.Ticket, err error) error {
	for _, target := range ignorable {
		if errors.Is(err, target) {
			d.Outcome, d.Detail = OutcomeIgnored, strings.TrimPrefix(err.Error(), "ticket: ")
			// A PR matched to an attempt that is not acted on still
			// records which: reviews per attempt are what gets counted.
			var unpromoted *ticket.AttemptNotPromotedError
			if errors.As(err, &unpromoted) {
				d.TicketID, d.AttemptNumber = &unpromoted.Attempt.TicketID, &unpromoted.Attempt.Number
			}
			return nil
		}
	}
	if err != nil {
		return err
	}
	d.Outcome, d.Detail, d.TicketID = OutcomeApplied, fmt.Sprintf("#%d %s", t.ID, what), &t.ID
	d.AttemptNumber = &t.MatchedAttempt
	return nil
}

// installationOf is the installation a payload names, 0 for none or for a
// body that is not JSON. It is read before the signature is checked, to
// find the secret to check it with, so it is trusted with nothing else.
func installationOf(body []byte) int64 {
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &p) != nil || p.Installation.ID < 0 {
		return 0
	}
	return p.Installation.ID
}

// actionOf is a payload's action, if it has one, for the delivery's
// record; the event's own handling reads it again, strictly.
func actionOf(body []byte) string {
	var a deliveryAction
	_ = json.Unmarshal(body, &a)
	return hookkit.Clean(a.Action, maxHeaderValue)
}

// deliveryAction reads a payload's action, for the delivery's record,
// before knowing which event's shape the rest of it has.
type deliveryAction struct {
	Action string `json:"action"`
}
