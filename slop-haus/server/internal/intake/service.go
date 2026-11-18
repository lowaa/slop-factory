package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strings"
	"time"

	"github.com/google/uuid"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// Tickets is what a delivery can do to tickets.
type Tickets interface {
	ApplyIntake(ctx context.Context, ref model.IntakeRef, deliveryID string, plan ticket.IntakePlanner) (*ticket.IntakeResult, error)
}

// Config is the intake door's setup. A false Enabled is the door shut.
type Config struct {
	Enabled bool
	// Secrets derives HMAC sources' signing secrets; nil means a source
	// can only use a bearer token.
	Secrets *Secrets
	// PublicBaseURL is where senders reach the receiver, for the URL the
	// board shows.
	PublicBaseURL string
	// Archive may be nil, which sees no tenant as archived. An authenticated
	// delivery for an archived tenant is acknowledged and dropped.
	Archive tenantstate.Archive
	// Owners says who may let a source close tickets; nil means nobody.
	Owners Owners
	// Limiter is the per-source rate limit.
	Limiter Limiter
	// Caps are the most one source may pile up; zero values take
	// DefaultCaps.
	Caps Caps
	// Now is the clock; nil is time.Now. Tests set it.
	Now func() time.Time
}

// Owners is the tenant's owner check.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// ReceivePath is the receiver's route; {id} is the source.
const ReceivePath = "/hooks/intake/{id}"

// DeliveryHeader is the sender's own id for a delivery, if it has one: it
// makes redelivery of the same event a no-op for good. Without it a
// delivery is identified by its body within a short window (see
// deliveryID).
const DeliveryHeader = "X-Slophaus-Delivery"

const (
	recentDeliveries = 50
	maxDetail        = 1000
	maxHeaderValue   = 100
	// dedupeWindow is how long an identical body without a delivery id
	// counts as the same delivery. Long enough to cover a sender's retries
	// of one; short enough that Alertmanager repeating an unchanged
	// notification an hour later still reaches a ticket someone closed.
	dedupeWindow = 10 * time.Minute
	// refusalInterval is how often rate-limited refusals are recorded.
	refusalInterval = time.Minute
)

type Service struct {
	store    Store
	tickets  Tickets
	cfg      *Config
	events   *audit.Recorder
	logger   *slog.Logger
	now      func() time.Time
	refusals *refusalLog
	gate     *authn.OwnerGate
}

func NewService(store Store, tickets Tickets, cfg *Config, events *audit.Recorder, logHandler slog.Handler) *Service {
	if events == nil {
		events = audit.New(logHandler, nil)
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewRateLimiter(60, 20)
	}
	if cfg.Caps.EventsPerTicket == 0 {
		cfg.Caps.EventsPerTicket = DefaultCaps.EventsPerTicket
	}
	if cfg.Caps.LiveTickets == 0 {
		cfg.Caps.LiveTickets = DefaultCaps.LiveTickets
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Service{store: store, tickets: tickets, cfg: cfg, events: events, logger: slog.New(logHandler), now: now}
	s.refusals = newRefusalLog(refusalInterval, now)
	s.gate = authn.NewOwnerGate(cfg.Owners, authn.WithRefusal(ErrNotOwner))
	return s
}

// Enabled reports whether the door is open.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// IntakeURL is the URL a sender is given for a source.
func (s *Service) IntakeURL(id uuid.UUID) string {
	return s.cfg.PublicBaseURL + strings.Replace(ReceivePath, "{id}", id.String(), 1)
}

// Spec is what a person registers a source with.
type Spec struct {
	Name            string
	Adapter         string
	Trust           Trust
	AuthKind        AuthKind
	DefaultStatus   ticket.Status
	DefaultPriority model.Priority
	MaxPriority     model.Priority
	RoleNames       []string
	RoleMatch       model.RoleMatch
	Paths           []string
	ReopenPolicy    ReopenPolicy
	// ResolveCloses is Source.ResolveCloses. Setting it takes the tenant's
	// owner, and a trusted source.
	ResolveCloses bool
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSource, fmt.Sprintf(format, args...))
}

// check validates spec and fills in its defaults.
func (s *Service) check(spec *Spec) error {
	spec.Name = oneLine(spec.Name)
	if spec.Name == "" || len(spec.Name) > 100 {
		return invalid("a name of 1 to 100 characters is required")
	}
	if _, ok := adapterFor(spec.Adapter); !ok {
		return invalid("unknown adapter %q", spec.Adapter)
	}
	if spec.Trust != TrustTrusted && spec.Trust != TrustUntrusted {
		return invalid("trust must be trusted or untrusted")
	}
	if spec.AuthKind != AuthBearer && spec.AuthKind != AuthHMAC {
		return invalid("auth must be bearer or hmac")
	}
	if spec.AuthKind == AuthHMAC && s.cfg.Secrets == nil {
		return ErrHMACDisabled
	}
	if spec.DefaultStatus == "" {
		spec.DefaultStatus = ticket.StatusPreparing
	}
	if spec.DefaultStatus != ticket.StatusOpen && spec.DefaultStatus != ticket.StatusPreparing {
		return invalid("default status must be open or preparing")
	}
	if spec.Trust == TrustUntrusted && spec.DefaultStatus != ticket.StatusPreparing {
		return invalid("an untrusted source's tickets are always preparing")
	}
	if spec.DefaultPriority == "" {
		spec.DefaultPriority = model.PriorityNormal
	}
	if spec.MaxPriority == "" {
		spec.MaxPriority = spec.DefaultPriority
	}
	if !spec.DefaultPriority.Valid() || !spec.MaxPriority.Valid() {
		return invalid("priorities must be low, normal, high or urgent")
	}
	if priorityRank[spec.DefaultPriority] > priorityRank[spec.MaxPriority] {
		return invalid("the default priority is above the maximum")
	}
	if spec.RoleMatch == "" {
		spec.RoleMatch = model.RoleMatchAny
	}
	if !spec.RoleMatch.Valid() {
		return invalid("role match must be any or all")
	}
	if spec.ReopenPolicy == "" {
		spec.ReopenPolicy = ReopenNew
	}
	if spec.ReopenPolicy != ReopenNew && spec.ReopenPolicy != ReopenExisting {
		return invalid("reopen policy must be new or reopen")
	}
	if spec.ResolveCloses && spec.Trust != TrustTrusted {
		return invalid("only a trusted source may close tickets when it reports them resolved")
	}
	paths := make([]string, 0, len(spec.Paths))
	for _, p := range spec.Paths {
		n, err := scope.NormalizePath(p)
		if err != nil {
			return invalid("path %q: %v", p, err)
		}
		paths = append(paths, n)
	}
	spec.Paths = paths
	return nil
}

// Created is a new source and its credential. The credential is shown once:
// a person who lost a token rotates it for a new one; an HMAC secret could
// be derived again, but is not kept on offer.
type Created struct {
	Source *Source
	// Token is set for a bearer source, Secret for an HMAC one.
	Token  string
	Secret string
}

// Create registers a source in who's tenant. A credential for a trusted
// source lets whoever holds it put open, claimable work in front of the
// tenant's agents, so it needs a recent sign-in, as changing how an account
// signs in does.
func (s *Service) Create(ctx context.Context, who *authn.User, spec *Spec) (*Created, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	if err := s.check(spec); err != nil {
		return nil, err
	}
	if spec.ResolveCloses {
		if err := s.gate.RequireOwner(ctx, who); err != nil {
			s.events.Record(ctx, audit.IntakeResolveClosesRefused, "user_id", who.ID, "tenant", who.TenantID, "name", spec.Name)
			return nil, err
		}
	}
	id := uuid.New()
	cred, token, err := s.newCredential(spec.AuthKind)
	if err != nil {
		return nil, err
	}
	src, err := s.store.CreateSource(ctx, id, spec, cred, who.ID)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.IntakeSourceCreated, "user_id", who.ID, "tenant", who.TenantID, "source", id,
		"name", src.Name, "trust", string(src.Trust), "auth", string(src.AuthKind), "adapter", src.Adapter)
	if src.ResolveCloses {
		s.events.Record(ctx, audit.IntakeResolveClosesEnabled, "user_id", who.ID, "tenant", who.TenantID, "source", id, "name", src.Name)
	}
	out := &Created{Source: src, Token: token}
	if src.AuthKind == AuthHMAC {
		out.Secret = s.cfg.Secrets.For(id, 1)
	}
	return out, nil
}

func (s *Service) newCredential(kind AuthKind) (cred *NewCredential, token string, err error) {
	if kind == AuthHMAC {
		return &NewCredential{Kind: AuthHMAC}, "", nil
	}
	token, hash, err := NewToken()
	if err != nil {
		return nil, "", fmt.Errorf("make intake token: %w", err)
	}
	return &NewCredential{Kind: AuthBearer, TokenHash: hash}, token, nil
}

// Rotate replaces a source's credential: the old one stops working at once.
func (s *Service) Rotate(ctx context.Context, who *authn.User, id uuid.UUID) (*Created, error) {
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	src, err := s.store.LiveSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if src.AuthKind == AuthHMAC && s.cfg.Secrets == nil {
		// Before anything is changed: rotating would end the old secret
		// with no way to give the new one.
		return nil, ErrHMACDisabled
	}
	cred, token, err := s.newCredential(src.AuthKind)
	if err != nil {
		return nil, err
	}
	generation, err := s.store.Rotate(ctx, id, cred)
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.IntakeSourceRotated, "user_id", who.ID, "tenant", who.TenantID, "source", id)
	out := &Created{Source: src, Token: token}
	if src.AuthKind == AuthHMAC {
		out.Secret = s.cfg.Secrets.For(id, generation)
	}
	return out, nil
}

// Revoke shuts a source for good. No step-up: taking a credential away is
// never the dangerous direction.
func (s *Service) Revoke(ctx context.Context, who *authn.User, id uuid.UUID) error {
	if err := s.store.Revoke(ctx, id); err != nil {
		return err
	}
	s.events.Record(ctx, audit.IntakeSourceRevoked, "user_id", who.ID, "tenant", who.TenantID, "source", id)
	return nil
}

// List is the tenant's sources, revoked ones included.
func (s *Service) List(ctx context.Context) ([]Source, error) { return s.store.ListSources(ctx) }

// Deliveries is the tenant's recent deliveries, newest first.
func (s *Service) Deliveries(ctx context.Context) ([]Delivery, error) {
	return s.store.ListDeliveries(ctx, recentDeliveries)
}

// Incoming is a delivery as it arrived, before anything is trusted.
type Incoming struct {
	// SourceID is the {id} path segment, unparsed.
	SourceID      string
	Authorization string
	Signature     string
	DeliveryID    string
	ContentType   string
	Body          []byte
}

// Receipt is what the receiver answers with: it says what happened, as the
// sender's own logs are where a person looks first.
type Receipt struct {
	Duplicate bool
	Outcome   Outcome
	Detail    string
}

// absentHash stands in for a source's stored hash when there is none, so a
// request for an id that does not exist takes as long to refuse as one for
// an id that does.
var absentHash = HashToken("no such source")

// Receive authenticates a delivery and acts on it.
//
// In order: the credential, which needs only the source's one global row --
// read before the tenant is known, and the only database access an
// unauthenticated request causes; then the tenant's own transaction, in
// which row level security keeps the delivery to that tenant's tickets
// whatever it says; then the rate limit, then the payload. Nothing is
// parsed before the credential is good. An unknown source, a revoked one
// and a wrong credential are all ErrUnauthorized.
func (s *Service) Receive(ctx context.Context, in *Incoming) (*Receipt, error) {
	if !s.Enabled() {
		return nil, ErrUnauthorized
	}
	id, cred, err := s.authenticate(ctx, in)
	if err != nil {
		return nil, err
	}
	ctx = db.WithTenant(ctx, cred.Tenant)
	src, err := s.store.LiveSource(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	// After the source is known live, so a revoked credential learns nothing
	// of the tenant's state, and before the limiter is charged: an archived
	// tenant's delivery changes nothing, not even a rate-limit bucket. It is
	// answered 200 so the sender does not retry or give up on its hook.
	if s.cfg.Archive != nil {
		archived, err := s.cfg.Archive.ArchivedTenant(ctx, cred.Tenant)
		if err != nil {
			return nil, err
		}
		if archived {
			s.logger.InfoContext(ctx, "dropped an intake delivery for an archived tenant", "tenant", cred.Tenant, "source", id)
			return &Receipt{Outcome: OutcomeIgnored, Detail: "this workspace is archived"}, nil
		}
	}
	if !s.cfg.Limiter.Allow(id) {
		s.refuse(ctx, src)
		return nil, ErrRateLimited
	}

	d := &Delivery{ID: s.deliveryID(in), SourceID: id}
	begun, err := s.store.BeginDelivery(ctx, d)
	if err != nil {
		return nil, err
	}
	if !begun {
		return &Receipt{Duplicate: true}, nil
	}
	if err := s.handle(ctx, src, d, in); err != nil {
		d.Outcome, d.TicketID = OutcomeError, nil
		if d.Detail == "" {
			d.Detail = "internal error; send it again to retry"
		}
		// Recorded even when ctx is what failed, or the delivery stays
		// "processing" and a redelivery is turned away as a duplicate.
		if ferr := s.store.FinishDelivery(context.WithoutCancel(ctx), d); ferr != nil {
			s.logger.ErrorContext(ctx, "record failed intake delivery", "delivery", d.ID, "err", ferr)
		}
		return nil, err
	}
	d.Detail = hookkit.Clean(d.Detail, maxDetail)
	if err := s.store.FinishDelivery(context.WithoutCancel(ctx), d); err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "intake delivery", "tenant", cred.Tenant, "source", id, "delivery", d.ID,
		"event", d.Event, "key", d.Key, "outcome", d.Outcome, "downgraded", d.Downgraded, "ticket", d.TicketID)
	return &Receipt{Outcome: d.Outcome, Detail: d.Detail}, nil
}

// authenticate checks the delivery's credential against the source's, and
// answers ErrUnauthorized for every way it can fail.
func (s *Service) authenticate(ctx context.Context, in *Incoming) (uuid.UUID, *Credential, error) {
	id, err := uuid.Parse(in.SourceID)
	if err != nil {
		CheckToken("", absentHash)
		return uuid.Nil, nil, ErrUnauthorized
	}
	cred, err := s.store.CredentialOf(ctx, id)
	if errors.Is(err, ErrNotFound) {
		CheckToken("", absentHash)
		return uuid.Nil, nil, ErrUnauthorized
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	switch cred.Kind {
	case AuthBearer:
		token := goauthn.BearerToken(in.Authorization)
		ok := token != ""
		// Compared even when the header is absent, so that costs the same.
		if !CheckToken(token, cred.TokenHash) || !ok {
			return uuid.Nil, nil, ErrUnauthorized
		}
	case AuthHMAC:
		if s.cfg.Secrets == nil || !s.cfg.Secrets.Verify(id, cred.Generation, in.Body, in.Signature) {
			return uuid.Nil, nil, ErrUnauthorized
		}
	default:
		return uuid.Nil, nil, ErrUnauthorized
	}
	return id, cred, nil
}

// deliveryID is the sender's own id when it gave a usable one; otherwise
// the body's hash within a window, so the sender's retries of one event are
// one delivery and a later, identical notification is not.
func (s *Service) deliveryID(in *Incoming) string {
	if hookkit.ValidHeader(in.DeliveryID, maxHeaderValue) {
		return "id:" + in.DeliveryID
	}
	sum := sha256.Sum256(in.Body)
	return fmt.Sprintf("body:%s:%d", hex.EncodeToString(sum[:16]), s.now().Unix()/int64(dedupeWindow.Seconds()))
}

// refuse records a rate-limited delivery, at most once a minute per source.
func (s *Service) refuse(ctx context.Context, src *Source) {
	n := s.refusals.refuse(src.ID)
	if n == 0 {
		return
	}
	s.events.Record(ctx, audit.IntakeRateLimited, "tenant", ctxTenant(ctx), "source", src.ID, "refused", n)
	d := &Delivery{
		ID: "limit:" + uuid.NewString(), SourceID: src.ID, Outcome: OutcomeRateLimited,
		Detail: fmt.Sprintf("%d deliveries refused: over the source's rate limit", n),
	}
	if err := s.store.RecordRefusal(context.WithoutCancel(ctx), d); err != nil {
		s.logger.ErrorContext(ctx, "record rate limited intake deliveries", "source", src.ID, "err", err)
	}
}

func ctxTenant(ctx context.Context) uuid.UUID {
	t, _ := db.TenantFrom(ctx)
	return t
}

// handle decides a delivery's outcome, recording it on d. An error is ours,
// not the delivery's: anything wrong with the delivery itself is an outcome
// of rejected, with the reason; anything the ticket rules or caps decline
// is ignored, with the reason.
func (s *Service) handle(ctx context.Context, src *Source, d *Delivery, in *Incoming) error {
	reject := func(why string) error {
		// Cleaned: a decoder's message can carry a field name the sender chose.
		d.Outcome, d.Detail = OutcomeRejected, oneLine(why)
		return nil
	}
	if mediaType, _, err := mime.ParseMediaType(in.ContentType); err != nil || mediaType != "application/json" {
		return reject("send the event as application/json")
	}
	adapter, ok := adapterFor(src.Adapter)
	if !ok {
		return fmt.Errorf("intake: source %s names adapter %q, which this server does not have", src.ID, src.Adapter)
	}
	ev, err := adapter.Parse(in.Body)
	if err != nil {
		return reject(err.Error())
	}
	if err := ev.check(); err != nil {
		return reject(err.Error())
	}
	d.Event, d.Key = string(ev.Kind), ev.Key

	var dec *decision
	result, err := s.tickets.ApplyIntake(ctx, model.IntakeRef{SourceID: src.ID, Key: ev.Key}, d.ID,
		func(state *ticket.IntakeState) (*ticket.IntakePlan, error) {
			dec = src.Plan(ev, d.ID, state, &s.cfg.Caps)
			return dec.plan, nil
		})
	if errors.Is(err, ticket.ErrUnknownRole) {
		d.Outcome, d.Detail = OutcomeError, "the source's roles name a role that no longer exists; fix the source"
		return err
	}
	if err != nil {
		return err
	}
	if result.AlreadyApplied {
		d.Outcome, d.TicketID = OutcomeApplied, &result.Ticket.ID
		d.Detail = fmt.Sprintf("#%d already updated by an earlier attempt at this delivery", result.Ticket.ID)
		return nil
	}
	if result.Action == ticket.IntakeSkip {
		d.Outcome, d.Detail = OutcomeIgnored, result.Skipped
		return nil
	}
	t := result.Ticket
	d.Outcome, d.TicketID = OutcomeApplied, &t.ID
	d.Detail = fmt.Sprintf("#%d %s", t.ID, applied(result.Action, t.Status))
	if dec.downgraded {
		d.Downgraded = true
		d.Detail += "; the sender asked for open, filed as preparing because this source is untrusted"
		s.events.Record(ctx, audit.IntakeDowngraded, "tenant", ctxTenant(ctx), "source", src.ID, "delivery", d.ID, "ticket", t.ID)
	}
	return nil
}

func applied(a ticket.IntakeAction, status ticket.Status) string {
	switch a {
	case ticket.IntakeCreate:
		return "created as " + string(status)
	case ticket.IntakeReopen:
		return "reopened as " + string(status)
	case ticket.IntakeCancel:
		return "cancelled: the sender reported it resolved"
	}
	return "updated"
}
