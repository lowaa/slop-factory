package legaldocs

import (
	"fmt"
	"regexp"
	"strings"
)

// Fragments supplies the generated sections of a document from the data
// inventory: the cookie table, the sub-processor list, the retention periods
// and the data flow.
type Fragments interface {
	// Fragment returns the Markdown for name ("cookie_table",
	// "sub_processors", "retention", "data_flow"), or false if it can't be
	// generated.
	Fragment(name string) (string, bool)
}

// CompletenessReporter is what a Fragments needs to be trusted with a
// document marked reviewed. Fragments render a value nobody has decided as
// plain words ("To be confirmed"), so the text alone can't be told from a
// decided one; the generator, which knows, says so here.
type CompletenessReporter interface {
	// Checked is Fragment plus what is still a placeholder in the text, as
	// strings for an error message; empty means the fragment is complete.
	// It is one call so the two come from the same pass.
	Checked(name string) (text string, unresolved []string, ok bool)
}

// PendingInventory is what a generated section says until the inventory
// can fill it in.
const PendingInventory = "*Pending inventory.*"

var (
	directivePattern = regexp.MustCompile(`\{\{inventory:([a-z_]+)\}\}`)
	todoMarker       = "TODO-LEGAL"
	knownFragments   = map[string]bool{"cookie_table": true, "sub_processors": true, "retention": true, "data_flow": true}
)

// rendered is a body with its generated sections filled in.
type rendered struct {
	body string
	// pending: a section had no generator and says PendingInventory.
	pending bool
	// unresolved: sections the generator filled but reports placeholders
	// in, each "<section>: <what>".
	unresolved []string
	// unchecked: sections filled by a Fragments that can't say whether they
	// are complete.
	unchecked []string
}

// render fills the inventory directives in body. A section is generated and
// checked once however often it is used, so every use shows the text that was
// checked and a section isn't reported twice.
func render(body string, frags Fragments) (*rendered, error) {
	var bad string
	r := &rendered{}
	texts := map[string]string{}
	r.body = directivePattern.ReplaceAllStringFunc(body, func(m string) string {
		name := directivePattern.FindStringSubmatch(m)[1]
		if !knownFragments[name] {
			bad = name
			return m
		}
		if text, done := texts[name]; done {
			return text
		}
		if text, unresolved, ok := fragment(frags, name); ok {
			text = strings.TrimSpace(text)
			texts[name] = text
			if unresolved == nil {
				r.unchecked = append(r.unchecked, name)
			}
			for _, u := range unresolved {
				r.unresolved = append(r.unresolved, name+": "+u)
			}
			return text
		}
		r.pending = true
		return PendingInventory
	})
	if bad != "" {
		return nil, fmt.Errorf("unknown inventory section %q", bad)
	}
	return r, nil
}

// fragment gets a section's text and what is unresolved in it. unresolved is
// nil only for a Fragments that can't say, and never nil for one that can
// (an empty, non-nil slice means complete).
func fragment(frags Fragments, name string) (string, []string, bool) {
	switch f := frags.(type) {
	case nil:
		return "", nil, false
	case CompletenessReporter:
		text, unresolved, ok := f.Checked(name)
		if unresolved == nil {
			unresolved = []string{}
		}
		return text, unresolved, ok
	}
	text, ok := frags.Fragment(name)
	return text, nil, ok
}
