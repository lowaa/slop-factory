package consent

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/go-auth/auth"
)

// DefaultCookieLifetime is a placeholder pending legal review (README §5).
const DefaultCookieLifetime = 6 * 30 * 24 * time.Hour

// maxCookieLen bounds what is decoded from a request: browsers drop
// cookies over ~4KB, and the server shouldn't parse arbitrary input.
const maxCookieLen = 4096

// cookieVersion prefixes every sealed cookie and is covered by its MAC, so
// a change of format gets a new version and an old cookie fails closed.
const cookieVersion = "v1"

const cookieKeyLabel = "consent-cookie"

// CookieDecision is one anonymous visitor's decision on a cookie category.
type CookieDecision struct {
	Purpose   string
	Granted   bool
	Version   string
	DecidedAt time.Time
	Source    Source
	// Unmerged marks a decision made while signed out and not yet merged
	// into a record. Decisions the cookie carries across a sign-in or
	// sign-out are not flagged, so a shared device's next person never
	// inherits the previous one's choices.
	Unmerged bool
}

// CookieState is what the consent cookie holds. It is sealed, so the
// server can trust the timestamps inside.
type CookieState struct {
	Decisions map[string]*CookieDecision
	// MergeToken is present only while the cookie holds decisions made
	// while signed out, which are not yet in anyone's record. It is random,
	// regenerated at each anonymous decision, and used for nothing but
	// making a merge happen once.
	MergeToken string
	// Expires is when the visitor is asked again.
	Expires time.Time
}

type wireDecision struct {
	Purpose  string `json:"p"`
	Granted  bool   `json:"g"`
	Version  string `json:"v"`
	At       int64  `json:"t"`
	Source   Source `json:"s"`
	Unmerged bool   `json:"u,omitempty"`
}

type wireCookie struct {
	Decisions []wireDecision `json:"d"`
	Token     string         `json:"m,omitempty"`
	Expires   int64          `json:"x"`
}

// CookieConfig describes the consent cookie.
type CookieConfig struct {
	// Name follows the app's cookie naming, e.g. app_consent.
	Name string
	// Secret keys the seal and must be at least 32 bytes. The MAC key is
	// derived from it under a label of its own.
	Secret []byte
	// Secure sets the Secure attribute. There is no default: a deployment
	// served over https must say true, and only local development says false.
	Secure bool
	// Lifetime is how long a decision stands before the visitor is asked
	// again. Zero means DefaultCookieLifetime.
	Lifetime time.Duration
}

// CookieCodec seals consent cookies and opens them again.
type CookieCodec struct {
	name     string
	key      []byte
	secure   bool
	lifetime time.Duration
}

func NewCookieCodec(cfg *CookieConfig) (*CookieCodec, error) {
	if cfg.Name == "" {
		return nil, errors.New("consent: the consent cookie needs a name")
	}
	key, err := auth.DeriveKey(cfg.Secret, cookieKeyLabel)
	if err != nil {
		return nil, fmt.Errorf("consent: cookie key: %w", err)
	}
	lifetime := cfg.Lifetime
	if lifetime == 0 {
		lifetime = DefaultCookieLifetime
	}
	if lifetime < 0 {
		return nil, errors.New("consent: the cookie lifetime can't be negative")
	}
	return &CookieCodec{name: cfg.Name, key: key, secure: cfg.Secure, lifetime: lifetime}, nil
}

// Name is the cookie's name.
func (c *CookieCodec) Name() string { return c.name }

// NewMergeToken is a fresh random token for a cookie that holds
// not-yet-merged decisions.
func NewMergeToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("consent: merge token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (c *CookieCodec) mac(signed string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(signed))
	return m.Sum(nil)
}

// Encode seals s.
func (c *CookieCodec) Encode(s *CookieState) (string, error) {
	w := wireCookie{Token: s.MergeToken, Expires: s.Expires.UnixMilli()}
	for _, d := range s.Decisions {
		w.Decisions = append(w.Decisions, wireDecision{d.Purpose, d.Granted, d.Version, d.DecidedAt.UnixMilli(), d.Source, d.Unmerged})
	}
	raw, err := json.Marshal(&w)
	if err != nil {
		return "", fmt.Errorf("consent: encode cookie: %w", err)
	}
	signed := cookieVersion + "." + base64.RawURLEncoding.EncodeToString(raw)
	out := signed + "." + base64.RawURLEncoding.EncodeToString(c.mac(signed))
	if len(out) > maxCookieLen {
		return "", errors.New("consent: cookie too large")
	}
	return out, nil
}

// Decode opens a sealed cookie. Anything that doesn't verify (a bad MAC,
// another version, unknown fields) is an error: callers treat it as no
// cookie. Nothing is parsed before the MAC checks out, and the MAC is
// compared in constant time. The error never echoes the value.
func (c *CookieCodec) Decode(value string) (*CookieState, error) {
	errBad := errors.New("consent: invalid consent cookie")
	if value == "" || len(value) > maxCookieLen {
		return nil, errBad
	}
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return nil, errBad
	}
	signed, sealedMAC := value[:i], value[i+1:]
	version, payload, ok := strings.Cut(signed, ".")
	if !ok || version != cookieVersion {
		return nil, errBad
	}
	got, err := base64.RawURLEncoding.DecodeString(sealedMAC)
	if err != nil || !hmac.Equal(got, c.mac(signed)) {
		return nil, errBad
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errBad
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wireCookie
	if err := dec.Decode(&w); err != nil {
		return nil, errBad
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errBad
	}
	s := &CookieState{Decisions: map[string]*CookieDecision{}, MergeToken: w.Token, Expires: time.UnixMilli(w.Expires)}
	for _, d := range w.Decisions {
		s.Decisions[d.Purpose] = &CookieDecision{d.Purpose, d.Granted, d.Version, time.UnixMilli(d.At), d.Source, d.Unmerged}
	}
	return s, nil
}

// HTTPCookie is the Set-Cookie for s: httpOnly, because only the server
// reads and writes it; SameSite=Lax; Secure as configured. Its Max-Age
// follows s.Expires but isn't what expires the choice: the sealed expiry
// is, because a client can keep a cookie as long as it likes.
func (c *CookieCodec) HTTPCookie(s *CookieState, now time.Time) (*http.Cookie, error) {
	v, err := c.Encode(s)
	if err != nil {
		return nil, err
	}
	maxAge := int(s.Expires.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name: c.name, Value: v, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode,
	}, nil
}

// Expiry is when a cookie written at now lapses.
func (c *CookieCodec) Expiry(now time.Time) time.Time { return now.Add(c.lifetime) }

// Open reads the request's consent cookie. nil means none, a bad one, or an
// expired one: all three leave the visitor undecided.
func (c *CookieCodec) Open(r *http.Request, now time.Time) *CookieState {
	ck, err := r.Cookie(c.name)
	if err != nil {
		return nil
	}
	return c.OpenValue(ck.Value, now)
}

// OpenValue is Open for a cookie value already read.
func (c *CookieCodec) OpenValue(value string, now time.Time) *CookieState {
	s, err := c.Decode(value)
	if err != nil || !now.Before(s.Expires) {
		return nil
	}
	return s
}
