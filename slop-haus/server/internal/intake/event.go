package intake

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// EventKind is what a sender says happened to its issue.
type EventKind string

const (
	EventOpen    EventKind = "open"
	EventUpdate  EventKind = "update"
	EventResolve EventKind = "resolve"
)

func (k EventKind) valid() bool {
	return k == EventOpen || k == EventUpdate || k == EventResolve
}

// Event is a delivery in the model every adapter maps to. Everything in it
// came from the sender, so it is data: nothing here decides roles, paths,
// scope or a status beyond a request the source's rules judge.
type Event struct {
	// Key is the sender's id for the issue: the correlation and dedupe
	// key, unique per (source, key).
	Key  string
	Kind EventKind
	// Title, Body and Link are shown as external content, never as the
	// ticket's own text.
	Title string
	Body  string
	Link  string
	// Status is a request, empty for none: open or preparing.
	Status ticket.Status
	// Priority is a request, empty for none; capped at the source's max.
	Priority model.Priority
	// Context is a small JSON object of the sender's metadata.
	Context json.RawMessage
}

// Limits bound what a sender can put in the database and in front of an
// agent: the request body is limited before parsing, and these after.
const (
	// MaxRequestBody bounds a delivery, read before anything parses it.
	MaxRequestBody = 64 << 10
	MaxKey         = 200
	MaxTitle       = 200
	MaxBody        = 20_000
	MaxLink        = 500
	MaxContext     = 4_000
)

// Adapter turns a source's own payload format into an Event. Adapters are
// Go code, not templates a user writes: a template language over attacker
// text is an injection surface and a complexity sink.
type Adapter interface {
	// Parse reads a raw body. An error is the delivery's fault and is told
	// to the sender; it must not echo the body.
	Parse(body []byte) (*Event, error)
}

// adapters are the formats a source can name.
var adapters = map[string]Adapter{
	"native":       nativeAdapter{},
	"alertmanager": alertmanagerAdapter{},
}

func adapterFor(name string) (Adapter, bool) {
	a, ok := adapters[name]
	return a, ok
}

// check validates and cleans an Event an adapter produced: invisible and
// control characters out, lengths bounded. It rejects rather than truncates
// what a sender controls exactly (key, link, context); an adapter that must
// accept whatever it is given (Alertmanager's alerts) truncates itself
// first with Truncate.
func (e *Event) check() error {
	e.Key = strings.TrimSpace(ticket.SanitizeExternal(e.Key))
	if e.Key == "" || len(e.Key) > MaxKey || strings.ContainsAny(e.Key, "\n\t") {
		return fmt.Errorf("key must be 1 to %d printable characters on one line", MaxKey)
	}
	if !e.Kind.valid() {
		return fmt.Errorf("event must be open, update or resolve")
	}
	e.Title = strings.Join(strings.Fields(ticket.SanitizeExternal(e.Title)), " ")
	if len(e.Title) > MaxTitle {
		return fmt.Errorf("title is over %d bytes", MaxTitle)
	}
	if e.Kind == EventOpen && e.Title == "" {
		return fmt.Errorf("an open needs a title")
	}
	e.Body = ticket.SanitizeExternal(e.Body)
	if len(e.Body) > MaxBody {
		return fmt.Errorf("body is over %d bytes", MaxBody)
	}
	if e.Link != "" {
		e.Link = strings.TrimSpace(ticket.SanitizeExternal(e.Link))
		u, err := url.Parse(e.Link)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(e.Link) > MaxLink {
			return fmt.Errorf("link must be an http(s) URL of at most %d bytes", MaxLink)
		}
	}
	switch e.Status {
	case "", ticket.StatusOpen, ticket.StatusPreparing:
	default:
		return fmt.Errorf("status may only be open or preparing")
	}
	if e.Priority != "" && !e.Priority.Valid() {
		return fmt.Errorf("priority must be low, normal, high or urgent")
	}
	if len(e.Context) > 0 {
		if len(e.Context) > MaxContext {
			return fmt.Errorf("context is over %d bytes", MaxContext)
		}
		var obj map[string]any
		if err := json.Unmarshal(e.Context, &obj); err != nil || obj == nil {
			return fmt.Errorf("context must be a JSON object")
		}
		// Cleaned as parsed values and re-encoded: compact, and without the
		// NULs and invisible characters that arrived as \u escapes, which
		// jsonb refuses (NUL) or a reader cannot see.
		clean, err := json.Marshal(sanitizeJSON(obj))
		if err != nil {
			return fmt.Errorf("context must be a JSON object")
		}
		e.Context = clean
	}
	return nil
}

func sanitizeJSON(v any) any {
	switch v := v.(type) {
	case string:
		return ticket.SanitizeExternal(v)
	case []any:
		for i := range v {
			v[i] = sanitizeJSON(v[i])
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[ticket.SanitizeExternal(k)] = sanitizeJSON(x)
		}
		return out
	}
	return v
}

// Truncate is s cut to at most n bytes on a rune boundary.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
