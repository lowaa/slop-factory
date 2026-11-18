// Package httperr maps a package's sentinel errors onto HTTP answers from one
// table, so that a refusal is declared once and the table can be tested
// without going through HTTP.
package httperr

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type Kind int

const (
	NotFound Kind = iota + 1
	Invalid
	Conflict
	Forbidden
	Unauthorized
	Unavailable
	TooMany
	TooLarge
	BadGateway
)

var statuses = map[Kind]int{
	NotFound:     http.StatusNotFound,
	Invalid:      http.StatusUnprocessableEntity,
	Conflict:     http.StatusConflict,
	Forbidden:    http.StatusForbidden,
	Unauthorized: http.StatusUnauthorized,
	Unavailable:  http.StatusServiceUnavailable,
	TooMany:      http.StatusTooManyRequests,
	TooLarge:     http.StatusRequestEntityTooLarge,
	BadGateway:   http.StatusBadGateway,
}

func (k Kind) Status() int { return statuses[k] }

// Rule answers errors.Is(err, Err) with Kind. The detail is Message, or the
// error's own text when Message is empty (so the text can differ from the
// sentinel's, which may name internals a client should not see). A Rule with
// a Code answers as an authn.Problem carrying that machine-readable code.
type Rule struct {
	Err     error
	Kind    Kind
	Message string
	Code    string
	// Warn, if set, logs the error at warn level under this message: for a
	// refusal that is the client's to see but the operator's to notice.
	Warn string
}

// SignIn is the answer to a request that needs a signed-in user and has none.
var SignIn = Rule{Err: authn.ErrNoUser, Kind: Unauthorized, Message: "sign in first"}

// Table is a package's mapping. Rules are tried in order. Custom, if set, is
// asked first and claims an error by returning non-nil: for errors that carry
// data of their own (errors.As) or are answered by another package.
type Table struct {
	Rules  []Rule
	Custom func(err error) error
	// Unexpected is the log line for an error nothing claims, which is
	// answered as a detail-free 500.
	Unexpected string
}

// Covers reports whether the table answers err other than as a 500.
func (t *Table) Covers(err error) bool {
	if t.Custom != nil && t.Custom(err) != nil {
		return true
	}
	for _, r := range t.Rules {
		if errors.Is(err, r.Err) {
			return true
		}
	}
	return false
}

// Map answers err from the table; an error it does not claim is logged and
// answered as a 500.
func Map(logger *slog.Logger, err error, t *Table) error {
	if t.Custom != nil {
		if out := t.Custom(err); out != nil {
			return out
		}
	}
	for _, r := range t.Rules {
		if !errors.Is(err, r.Err) {
			continue
		}
		msg := r.Message
		if msg == "" {
			msg = err.Error()
		}
		if r.Warn != "" {
			logger.Warn(r.Warn, "err", err)
		}
		if r.Code != "" {
			return authn.NewProblem(r.Kind.Status(), r.Code, msg)
		}
		return huma.NewError(r.Kind.Status(), msg)
	}
	logger.Error(t.Unexpected, "err", err)
	return huma.Error500InternalServerError("internal server error")
}
