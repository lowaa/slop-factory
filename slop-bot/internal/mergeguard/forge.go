package mergeguard

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

var errNoRepo = errors.New("mergeguard: no repository to ask")

// ghPRValueOptions are `gh pr`'s options, before its subcommand, that take
// a separate argument.
var ghPRValueOptions = []string{"-R", "--repo"}

// ghAPIValueOptions are `gh api`'s options that take a separate argument,
// other than the method and fields, which are read.
var ghAPIValueOptions = []string{"-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "-p", "--preview", "--cache", "--input"}

var (
	// restMerge matches the REST endpoints that merge: a pull request's
	// merge, and the merge-a-branch endpoint.
	restMerge = regexp.MustCompile(`(?i)(^|/)(pulls/[^/?]+/merge|repos/[^/?]+/[^/?]+/merges)/?(\?|$)`)
	// graphQLMerge matches the GraphQL mutations that merge, or queue or
	// arm a merge.
	graphQLMerge = regexp.MustCompile(`(?i)\b(mergePullRequest|enablePullRequestAutoMerge|enqueuePullRequest|mergeBranch)\b`)
)

func (s *scan) gh(args []word) {
	i := firstPositional(args, nil)
	if i < 0 {
		return
	}
	if !args[i].ok {
		s.opaque(printed(args[i].node), "gh's command is computed")
		return
	}
	rest := args[i+1:]
	switch args[i].val {
	case "pr":
		j := firstPositional(rest, ghPRValueOptions)
		if j < 0 {
			return
		}
		if !rest[j].ok {
			s.opaque(printed(rest[j].node), "gh pr's subcommand is computed")
			return
		}
		if rest[j].val == "merge" {
			s.find(KindPRMerge, "gh pr merge")
		}
	case "api":
		s.ghAPI(rest)
	}
}

func (s *scan) glab(args []word) {
	if len(args) >= 2 && args[0].val == "mr" && args[1].val == "merge" {
		s.find(KindPRMerge, "glab mr merge")
	}
}

// firstPositional is the index of the first argument that isn't an option
// (or an option's value), -1 when there is none.
func firstPositional(args []word, values []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok || !strings.HasPrefix(a.val, "-") {
			return i
		}
		if slices.Contains(values, a.val) {
			i++
		}
	}
	return -1
}

// ghAPI classifies `gh api <endpoint>`: a REST merge endpoint called with
// a method that writes, or a GraphQL merge mutation.
func (s *scan) ghAPI(args []word) {
	a := parseGHAPI(args)
	if a.endpoint == nil {
		return
	}
	if !a.endpoint.ok {
		s.opaque(printed(a.endpoint.node), "gh api's endpoint is computed")
		return
	}
	ep := a.endpoint.val
	switch {
	case strings.EqualFold(strings.Trim(ep, "/"), "graphql"):
		if a.input {
			s.find(KindAPIMerge, "gh api graphql --input: the guard can't read the query, which may merge")
			return
		}
		for _, f := range a.fields {
			if graphQLMerge.MatchString(f.text()) {
				s.find(KindAPIMerge, "gh api graphql with a merge mutation")
				return
			}
		}
	case restMerge.MatchString(ep) && a.method != "GET" && a.method != "HEAD":
		s.find(KindAPIMerge, "gh api "+ep)
	}
}

// text is the word's value, or its source when it has none to read.
func (w *word) text() string {
	if w.ok {
		return w.val
	}
	return printed(w.node)
}

// ghAPIArgs is a `gh api` command line, taken apart.
type ghAPIArgs struct {
	endpoint *word
	method   string
	fields   []word
	input    bool
}

func parseGHAPI(args []word) *ghAPIArgs {
	g := &ghAPIArgs{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok || !strings.HasPrefix(a.val, "-") {
			if g.endpoint == nil {
				g.endpoint = &args[i]
			}
			continue
		}
		var next *word
		if i+1 < len(args) {
			next = &args[i+1]
		}
		if g.option(a, next) {
			i++
		}
	}
	return g
}

// option records one option, and reports whether it took next as its
// value.
func (g *ghAPIArgs) option(a word, next *word) bool {
	name, inline, hasValue := strings.Cut(a.val, "=")
	value, took := word{val: inline, ok: true, node: a.node}, false
	if !hasValue && next != nil {
		value, took = *next, true
	}
	switch {
	case name == "-X" || name == "--method":
		g.method = strings.ToUpper(value.val)
	case strings.HasPrefix(a.val, "-X"):
		g.method = strings.ToUpper(a.val[2:])
		return false
	case name == "-f" || name == "-F" || name == "--field" || name == "--raw-field":
		g.fields = append(g.fields, value)
	case slices.Contains(ghAPIValueOptions, name):
		g.input = g.input || name == "--input"
	default:
		return false
	}
	return took
}

// httpClient flags a curl (or similar) aimed at a merge endpoint, or
// carrying a GraphQL merge mutation.
func (s *scan) httpClient(name string, args []word) {
	for _, a := range args {
		text := a.text()
		if restMerge.MatchString(strings.SplitN(text, "#", 2)[0]) || graphQLMerge.MatchString(text) {
			s.find(KindAPIMerge, name+" to a merge endpoint")
			return
		}
	}
}
