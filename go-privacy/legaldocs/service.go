package legaldocs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/go-privacy/consent"
)

// Config configures a Service.
type Config struct {
	Kinds *Kinds
	Store Store
	// DefaultLocale is the locale served when a request names none, and
	// the one Versions reports to consent.
	DefaultLocale string
	// Clock is time.Now unless a test sets it.
	Clock  func() time.Time
	Logger slog.Handler
}

// Service publishes documents and answers which version is in force. It
// serves from the Store, not from the files: the Store holds what was
// published, which is what a person consented to.
type Service struct {
	cfg    Config
	logger *slog.Logger
}

// Service is the consent package's Documents, so a version bump drives a
// re-prompt.
var _ consent.Documents = (*Service)(nil)

func NewService(cfg *Config) (*Service, error) {
	if cfg.Kinds == nil || cfg.Store == nil {
		return nil, errors.New("legaldocs: a service needs kinds and a store")
	}
	c := *cfg
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.DefaultLocale == "" {
		c.DefaultLocale = "en"
	}
	if c.Logger == nil {
		c.Logger = slog.DiscardHandler
	}
	return &Service{cfg: c, logger: slog.New(c.Logger)}, nil
}

// Publish records the versions the Store does not hold yet, typically what
// a Loader read at startup, and leaves every version already published
// alone.
//
// A published version is not compared with the file it came from. Its
// generated sections are filled from the inventory as it is now, which may
// have moved on since it was published (a storage key added), so a
// comparison would refuse to start over a change nobody made to the
// version. The database row is what people agreed to and what is served;
// the file's source is held still in CI by Lock, and the rendered text of
// the version in force with it.
func (s *Service) Publish(ctx context.Context, versions []*Version) error {
	for _, v := range versions {
		if !s.cfg.Kinds.Has(v.Kind) {
			return fmt.Errorf("legaldocs: %q is not a declared kind", v.Kind)
		}
		_, err := s.cfg.Store.Get(ctx, v.Kind, v.Version, v.Locale)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, ErrNotFound):
			return err
		}
		// Another replica can publish the same version between the Get and
		// here, rendered from a different inventory during a rolling
		// deploy. The row it wrote stands, so that is not an error.
		if err := s.cfg.Store.Publish(ctx, v); err != nil && !errors.Is(err, ErrVersionChanged) {
			return err
		}
	}
	return nil
}

func (s *Service) locale(l string) (string, error) {
	if l == "" {
		return s.cfg.DefaultLocale, nil
	}
	if !localePattern.MatchString(l) {
		return "", ErrNotFound
	}
	return l, nil
}

// inForce is the versions whose effective date has come, ordered
// (effective, version). A scheduled version is not public until then.
func (s *Service) inForce(ctx context.Context, kind, locale string) ([]*Version, error) {
	if !s.cfg.Kinds.Has(kind) {
		return nil, ErrNotFound
	}
	locale, err := s.locale(locale)
	if err != nil {
		return nil, err
	}
	all, err := s.cfg.Store.List(ctx, kind, locale)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Clock()
	var out []*Version
	for _, v := range all {
		if !v.Effective.After(now) {
			out = append(out, v)
		}
	}
	return out, nil
}

// currentSummary is Current without the body.
func (s *Service) currentSummary(ctx context.Context, kind, locale string) (*Version, error) {
	vs, err := s.inForce(ctx, kind, locale)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, ErrNotFound
	}
	return vs[len(vs)-1], nil
}

// Current is the version in force: the latest by (effective, version).
func (s *Service) Current(ctx context.Context, kind, locale string) (*Version, error) {
	cur, err := s.currentSummary(ctx, kind, locale)
	if err != nil {
		return nil, err
	}
	return s.cfg.Store.Get(ctx, kind, cur.Version, cur.Locale)
}

// Get returns one version in force, or ErrNotFound.
func (s *Service) Get(ctx context.Context, kind, version, locale string) (*Version, error) {
	if !s.cfg.Kinds.Has(kind) {
		return nil, ErrNotFound
	}
	locale, err := s.locale(locale)
	if err != nil {
		return nil, err
	}
	v, err := s.cfg.Store.Get(ctx, kind, version, locale)
	if err != nil {
		return nil, err
	}
	if v.Effective.After(s.cfg.Clock()) {
		return nil, ErrNotFound
	}
	return v, nil
}

// History lists every version in force, newest first, without bodies.
func (s *Service) History(ctx context.Context, kind, locale string) ([]*Version, error) {
	vs, err := s.inForce(ctx, kind, locale)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(vs)-1; i < j; i, j = i+1, j-1 {
		vs[i], vs[j] = vs[j], vs[i]
	}
	return vs, nil
}

// Versions is consent.Documents: the published versions of kind in the
// default locale. It is an error for a kind to have none.
func (s *Service) Versions(ctx context.Context, kind string) ([]consent.DocVersion, error) {
	all, err := s.cfg.Store.List(ctx, kind, s.cfg.DefaultLocale)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("legaldocs: no version of %q is published", kind)
	}
	out := make([]consent.DocVersion, len(all))
	for i, v := range all {
		out[i] = consent.DocVersion{Version: v.Version, Effective: v.Effective, Material: v.Material}
	}
	return out, nil
}
