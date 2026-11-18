package ticket

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Bounds on ListFilter.Text. Every word is its own pass over the tenant's
// tickets, comments and handoffs, so it is the word count that bounds a
// list request's cost; the length bounds the pattern each pass compares.
const (
	MaxSearchTextLen = 200
	MaxSearchTerms   = 10
)

func validSearchText(text string) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return fmt.Errorf("%w: not valid text", ErrInvalidSearch)
	}
	if utf8.RuneCountInString(text) > MaxSearchTextLen {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidSearch, MaxSearchTextLen)
	}
	if len(searchTerms(text)) > MaxSearchTerms {
		return fmt.Errorf("%w: at most %d words", ErrInvalidSearch, MaxSearchTerms)
	}
	return nil
}

// searchTerms splits a text filter into the words a ticket must each
// contain. Whitespace-separated rather than parsed: the board's filter is a
// narrowing box, not a query language, and a quote or a minus sign in a
// path or an identifier should be matched, not interpreted. Repeats are
// dropped because each word costs a scan.
func searchTerms(text string) []string {
	terms := []string{}
	seen := map[string]struct{}{}
	for _, word := range strings.Fields(text) {
		key := strings.ToLower(word)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		terms = append(terms, word)
	}
	return terms
}

// ticketRef reads a term of the form #<number> as a ticket id. A longer id
// than int64 holds is just text.
func ticketRef(term string) (int64, bool) {
	digits, ok := strings.CutPrefix(term, "#")
	if !ok || digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	return id, err == nil
}

// likeEscape makes s match only itself inside a LIKE pattern written with
// ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// likeContains is s as a LIKE pattern matching anything containing it.
func likeContains(s string) string {
	return "%" + likeEscape(s) + "%"
}

// textMatch is the condition that the ticket aliased t contains the LIKE
// pattern in placeholder $n anywhere a person would look for it.
//
// Unindexed on purpose: under row-level security Postgres will not use a
// trigram (or full-text) index for ILIKE, because the operator is not
// leakproof and so may not be evaluated ahead of the tenant policy. A
// tenant's own rows are scanned instead, after the tenant_id indexes have
// narrowed to them.
//
// The subqueries join on tenant_id as well as ticket id: ids are only
// unique within a tenant, and RLS already restricting both sides is not a
// reason for the query to rely on it for correctness.
//
// skipAttempts leaves out what belongs to an attempt -- its comments and every
// handoff -- on a ticket with several, for an agent's search: a match on a
// sibling's words would tell the agent they exist, though it is never shown
// them. With a single attempt there is no sibling, and they are searched.
func textMatch(n int, skipAttempts bool) string {
	p := fmt.Sprintf(`$%d`, n)
	comments, handoffs := ``, ``
	if skipAttempts {
		single := `(SELECT count(*) FROM attempts sa WHERE sa.tenant_id = t.tenant_id AND sa.ticket_id = t.id) <= 1`
		comments, handoffs = ` AND (c.attempt_number IS NULL OR `+single+`)`, ` AND `+single
	}
	return `(t.title ILIKE ` + p + ` ESCAPE '\' OR t.body ILIKE ` + p + ` ESCAPE '\'` +
		` OR EXISTS (SELECT 1 FROM ticket_comments c` +
		` WHERE c.tenant_id = t.tenant_id AND c.ticket_id = t.id AND c.body ILIKE ` + p + ` ESCAPE '\'` + comments + `)` +
		` OR EXISTS (SELECT 1 FROM agent_contexts h` +
		` WHERE h.tenant_id = t.tenant_id AND h.ticket_id = t.id AND h.body ILIKE ` + p + ` ESCAPE '\'` + handoffs + `))`
}
