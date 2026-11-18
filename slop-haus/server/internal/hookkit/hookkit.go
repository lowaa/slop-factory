// Package hookkit is what slop-haus's webhook doors share: githubhook and
// intake both take a request from a stranger on a listener of their own,
// read it with a size limit, record a delivery's outcome and turn away a
// redelivery of one already handled. The doors differ in who may knock and
// what a delivery does; this is the part that must not drift apart.
package hookkit

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Outcome is what became of a delivery.
type Outcome string

const (
	// OutcomeProcessing is a delivery being handled now, or one whose
	// handling was cut off: a redelivery of it is ignored as a duplicate
	// until it has been processing long enough to count as abandoned.
	OutcomeProcessing Outcome = "processing"
	// OutcomeApplied moved a ticket, or was a ping.
	OutcomeApplied Outcome = "applied"
	// OutcomeIgnored changed nothing, for the reason in its detail.
	OutcomeIgnored Outcome = "ignored"
	// OutcomeError failed on our side. A redelivery of it is handled
	// afresh.
	OutcomeError Outcome = "error"
)

// AbandonedAfter is how long a delivery can sit in processing before it is
// taken to have been cut off -- the process died mid-way -- and a
// redelivery handles it again. Far longer than handling one ever takes.
const AbandonedAfter = 5 * time.Minute

// ErrTooLarge is a body over the limit.
var ErrTooLarge = errors.New("hookkit: body too large")

// ReadBody reads r's body, up to max bytes. The limit is on the read, so an
// oversized body is refused before anything parses it, or MACs it.
func ReadBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, ErrTooLarge
		}
		return nil, err
	}
	return body, nil
}

// ValidHeader reports whether v is fit to be recorded as a header's value:
// present, short, printable UTF-8.
func ValidHeader(v string, max int) bool {
	return v != "" && len(v) <= max && utf8.ValidString(v) &&
		!strings.ContainsFunc(v, unicode.IsControl)
}

// Clean is s fit for a text column: without NULs, which Postgres refuses in
// text, and cut to at most n bytes on a rune boundary.
func Clean(s string, n int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
