// Package logpolicy is the server-log half of the operational telemetry tier
// (go-privacy/README.md, "Telemetry tiers"): an slog.Handler wrapper that
// applies per-app attribute rules before a record reaches any destination.
//
// It works at the log boundary rather than at call sites on purpose. An error
// string that reaches the logger counts as logged, but the same string is
// matched by tests and sometimes shown to the user, so rewriting every error
// would change behaviour that has nothing to do with logging.
package logpolicy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
)

// Action is what a rule does to the value of a matching attribute.
type Action int

const (
	// Drop removes the attribute.
	Drop Action = iota + 1
	// Hash replaces the value with a keyed pseudonym, so two records about
	// the same value can be told apart from records about another without
	// the value being recoverable from the log.
	Hash
	// TruncateIP replaces an IP address (or "ip:port") with its /24 (IPv4)
	// or /48 (IPv6) prefix. A value that does not parse as an address is
	// replaced outright: a field that is meant to hold an address but holds
	// something else is not known to be safe.
	TruncateIP
	// RedactQuoted replaces every double-quoted substring with "[redacted]".
	// Go's %q is how this code base prints an input value into an error
	// ("... named %q already exists"), so this removes the value and keeps
	// the shape of the message.
	RedactQuoted
)

// Rule applies Action to every attribute whose key is one of Keys. Keys match
// case-insensitively, and a dotted key, in a rule or on an attribute, matches
// by its last segment ("http.request.body" is "body").
type Rule struct {
	Keys   []string
	Action Action
}

// DefaultRules are the rules every app gets: no request or response bodies,
// no credentials, client addresses truncated, and the free text of an error
// scrubbed of quoted input. An app's own rules are added to them; they cannot
// be removed, because the point of the operational tier is that no app has to
// remember them.
func DefaultRules() []Rule {
	return []Rule{
		{Keys: []string{"body", "request_body", "response_body", "req_body", "resp_body", "payload"}, Action: Drop},
		{Keys: []string{"password", "passwd", "token", "access_token", "refresh_token", "id_token", "secret", "client_secret",
			"api_key", "apikey", "authorization", "cookie", "set-cookie", "csrf", "jwt", "session"}, Action: Drop},
		{Keys: []string{"client_ip", "remote", "remote_addr", "ip", "started_from", "x-forwarded-for"}, Action: TruncateIP},
		{Keys: []string{"err", "error", "cause", "reason"}, Action: RedactQuoted},
	}
}

// EmailRules hash the keys an address is logged under.
func EmailRules() []Rule {
	return []Rule{{Keys: []string{"email", "to", "from"}, Action: Hash}}
}

// Config configures a Handler.
type Config struct {
	// Rules are added to DefaultRules. A rule for a key the defaults already
	// cover is an error: an app can add keys, never weaken one.
	Rules []Rule
	// HashKey keys Hash's pseudonyms. Nil makes a random key for the life of
	// the process: pseudonyms then can't be joined across restarts, which is
	// the point, since a log with a stable pseudonym for an address is a log
	// with that address in it for anyone who can guess it.
	HashKey []byte
}

// Handler is a slog.Handler that applies rules to everything it forwards.
type Handler struct {
	next  slog.Handler
	rules map[string]Action
	key   []byte
}

// New wraps next.
func New(next slog.Handler, cfg *Config) (*Handler, error) {
	rules := map[string]Action{}
	for _, r := range DefaultRules() {
		for _, k := range r.Keys {
			rules[ruleKey(k)] = r.Action
		}
	}
	for _, r := range cfg.Rules {
		if r.Action < Drop || r.Action > RedactQuoted {
			return nil, fmt.Errorf("logpolicy: unknown action %d for keys %v", r.Action, r.Keys)
		}
		for _, k := range r.Keys {
			if _, exists := rules[ruleKey(k)]; exists {
				return nil, fmt.Errorf("logpolicy: key %q is already covered by the default rules", k)
			}
			rules[ruleKey(k)] = r.Action
		}
	}
	key := cfg.HashKey
	if key == nil {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("logpolicy: hash key: %w", err)
		}
	}
	return &Handler{next: next, rules: rules, key: key}, nil
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a, keep := h.apply(a); keep {
			out.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(h.applyAll(attrs)), rules: h.rules, key: h.key}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name), rules: h.rules, key: h.key}
}

func (h *Handler) applyAll(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a, keep := h.apply(a); keep {
			out = append(out, a)
		}
	}
	return out
}

func (h *Handler) apply(a slog.Attr) (slog.Attr, bool) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		inner := h.applyAll(a.Value.Group())
		if len(inner) == 0 {
			return slog.Attr{}, false
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(inner...)}, true
	}
	action, ok := h.rules[ruleKey(a.Key)]
	switch {
	case ok:
	case isError(a.Value):
		// An error is free text whatever it is logged under.
		action = RedactQuoted
	default:
		return a, true
	}
	switch action {
	case Drop:
		return slog.Attr{}, false
	case Hash:
		return slog.String(a.Key, h.pseudonym(a.Value.String())), true
	case TruncateIP:
		return slog.String(a.Key, truncateIP(a.Value.String())), true
	default:
		return slog.String(a.Key, redactQuoted(a.Value.String())), true
	}
}

func ruleKey(k string) string {
	k = strings.ToLower(k)
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		return k[i+1:]
	}
	return k
}

func isError(v slog.Value) bool {
	if v.Kind() != slog.KindAny {
		return false
	}
	_, ok := v.Any().(error)
	return ok
}

func (h *Handler) pseudonym(s string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(s))))
	return "h:" + hex.EncodeToString(m.Sum(nil)[:8])
}

const (
	ipv4Bits = 24
	ipv6Bits = 48
	// redactedIP stands where a value isn't an address at all.
	redactedIP = "[invalid-ip]"
)

func truncateIP(s string) string {
	s = strings.TrimSpace(s)
	addr, err := netip.ParseAddr(s)
	if err != nil {
		if ap, apErr := netip.ParseAddrPort(s); apErr == nil {
			addr = ap.Addr()
		} else if host, _, splitErr := net.SplitHostPort(s); splitErr == nil {
			if addr, err = netip.ParseAddr(host); err != nil {
				return redactedIP
			}
		} else {
			return redactedIP
		}
	}
	addr = addr.WithZone("").Unmap()
	bits := ipv6Bits
	if addr.Is4() {
		bits = ipv4Bits
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return redactedIP
	}
	return p.String()
}

var quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

func redactQuoted(s string) string {
	return quoted.ReplaceAllString(s, `"[redacted]"`)
}
