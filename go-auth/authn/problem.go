package authn

import (
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Problem is huma's error body plus a machine-readable code, so a client
// can tell refusals apart without matching on their text -- a
// "reauth_required" means re-authenticate and retry, a "csrf" means sign in
// again. The codes are shared by every app using this package for the same
// refusals, so one sign-in UI can serve every app.
type Problem struct {
	huma.ErrorModel
	Code string `json:"code" doc:"Machine-readable reason, e.g. reauth_required"`
}

// NewProblem is a refusal with status, code and a human-readable detail.
func NewProblem(status int, code, detail string) *Problem {
	return &Problem{ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: detail}, Code: code}
}

// writeProblem answers a request a middleware refuses. huma.WriteErr can
// only write its own ErrorModel, which has no code.
func writeProblem(api huma.API, ctx huma.Context, p *Problem) {
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	_ = api.Marshal(ctx.BodyWriter(), "application/json", p)
}

// ProblemEntry is how one sentinel error is answered over HTTP.
type ProblemEntry struct {
	Err    error
	Status int
	Code   string
}

// ProblemTable turns an app's sentinel errors into Problems. Each app owns
// its table (the refusals differ), but the matching and the detail rules are
// shared so the apps cannot drift.
type ProblemTable struct {
	entries  []ProblemEntry
	prefixes []string
}

// NewProblemTable is a table whose details have any one of prefixes removed
// from the front: a sentinel's "user: " package prefix is for logs, and a
// person reads the rest. Entries are tried in order, so put a more specific
// error before one it wraps.
func NewProblemTable(prefixes []string, entries ...ProblemEntry) *ProblemTable {
	return &ProblemTable{entries: entries, prefixes: prefixes}
}

// ProblemFor is err as a Problem from the first entry err matches with
// errors.Is, or nil if there is none. The detail is the matched sentinel's
// own text, never err's: a wrapped error can carry SQL, DSNs or provider
// bodies that must not reach a client.
func (t *ProblemTable) ProblemFor(err error) *Problem {
	for _, e := range t.entries {
		if errors.Is(err, e.Err) {
			return NewProblem(e.Status, e.Code, t.detail(e.Err))
		}
	}
	return nil
}

func (t *ProblemTable) detail(err error) string {
	msg := err.Error()
	for _, p := range t.prefixes {
		if rest, ok := strings.CutPrefix(msg, p); ok {
			return rest
		}
	}
	return msg
}
