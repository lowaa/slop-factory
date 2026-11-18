// Package discussionpick is the steward's discussion_pick category: a worker
// stopped with a question and recommended one of its options, and the steward
// answers by accepting exactly that option. The allowlist
// entry is in steward/category.go and the comparator in steward/agreement,
// since neither can import this package.
package discussionpick

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

const CategoryID = "discussion_pick"

// Discussion is the open question as the server reads it now.
type Discussion struct {
	Question string
	// Handoff is the worker's handoff text, where its recommendation lives.
	Handoff string
}

// DiscussionReader reads the ticket's open discussion. The proposed answer is
// the subject's Body, which the engine takes from the proposal itself.
type DiscussionReader interface {
	Discussion(ctx context.Context, ticketID int64) (*Discussion, error)
}

// WordList is the owner's sensitive word list at the version pinned for use.
type WordList struct {
	Version string
	Words   string
}

// WordListReader reads the pinned list. A newer saved version that is not
// pinned must not be returned: the owner chooses when a change takes effect.
type WordListReader interface {
	Pinned(ctx context.Context) (*WordList, error)
}

// ProtectedPathReader reads the repo's protected_paths rules at their current
// revision, not the assessment's.
type ProtectedPathReader interface {
	// Protected returns, for each of paths that a rule covers, the rule's
	// reason (possibly empty), keyed by path.
	Protected(ctx context.Context, paths []string) (map[string]string, error)
}

type check struct {
	name string
	run  func(ctx context.Context, s *steward.Subject) (steward.Evidence, error)
}

func (c check) Name() string { return c.name }
func (c check) Run(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
	ev, err := c.run(ctx, s)
	ev.Check = c.name
	return ev, err
}

// recommendLine finds a handoff line that names the recommended option after a
// colon: "Recommended: X", "- **Recommendation:** X", "I recommend: X".
var recommendLine = regexp.MustCompile(`(?i)^[\s#>*_\-]*(?:i\s+|we\s+)?recommend(?:s|ed|ation)?\b[\s*_]*:[\s*_]*(\S.*)$`)

// Recommendation is what the steward must record about a pick: the question,
// the option and the handoff line that recommends it.
type Recommendation struct {
	Question string `json:"question"`
	Option   string `json:"option"`
	Line     string `json:"line"`
}

// Recommended finds the option the handoff recommends. Two lines that
// recommend different options are ambiguous and give no option.
func Recommended(d *Discussion) (*Recommendation, error) {
	var rec *Recommendation
	for _, line := range strings.Split(d.Handoff, "\n") {
		m := recommendLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		// Only the whitespace at the ends is dropped: anything more would let
		// an answer that differs from the option pass.
		option := strings.TrimSpace(m[1])
		if rec != nil && rec.Option != option {
			return nil, fmt.Errorf("the handoff recommends more than one option")
		}
		if rec == nil {
			rec = &Recommendation{Question: d.Question, Option: option, Line: strings.TrimSpace(line)}
		}
	}
	if rec == nil {
		return nil, fmt.Errorf("the handoff recommends no option")
	}
	return rec, nil
}

// SameOption is the comparison used by the check and the scorer: exact, byte
// for byte.
func SameOption(recommended, answer string) bool { return recommended == answer }

// OptionRecommendedByWorker holds when the proposed answer is, byte for byte,
// the option the worker's handoff recommends.
func OptionRecommendedByWorker(d DiscussionReader) steward.Check {
	return check{steward.CheckOptionRecommendedByAgent, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		disc, err := d.Discussion(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		if disc == nil {
			return steward.Evidence{Detail: "no open discussion"}, nil
		}
		rec, err := Recommended(disc)
		if err != nil {
			return steward.Evidence{Detail: err.Error()}, nil
		}
		if !SameOption(rec.Option, s.Body) {
			return steward.Evidence{Detail: fmt.Sprintf("the answer is not the recommended option; the handoff says %q", rec.Line)}, nil
		}
		return steward.Evidence{OK: true, Detail: fmt.Sprintf("question %q; option %q; handoff line %q", rec.Question, rec.Option, rec.Line)}, nil
	}}
}

// TopicNotSensitive holds when neither the question nor the recommended option
// contains an entry of the pinned sensitive word list. The evidence names the
// matched word and the list version.
func TopicNotSensitive(d DiscussionReader, w WordListReader) steward.Check {
	return check{steward.CheckTopicNotSensitive, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		disc, err := d.Discussion(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		if disc == nil {
			return steward.Evidence{Detail: "no open discussion"}, nil
		}
		list, err := w.Pinned(ctx)
		if err != nil {
			return steward.Evidence{}, err
		}
		if list == nil {
			return steward.Evidence{Detail: "no sensitive word list is pinned"}, nil
		}
		ref := "sensitive words " + list.Version
		// The option is read from the handoff, not the answer, so a mismatched
		// answer cannot hide a sensitive option from this check.
		text := disc.Question
		if rec, err := Recommended(disc); err == nil {
			text += "\n" + rec.Option
		}
		if word, ok := fleetinstructions.Match(list.Words, text); ok {
			return steward.Evidence{Ref: ref, Detail: fmt.Sprintf("matches %q on list version %s", word, list.Version)}, nil
		}
		return steward.Evidence{OK: true, Ref: ref, Detail: "no match on list version " + list.Version}, nil
	}}
}

// NoProtectedPath holds when no declared path is protected. A ticket with no
// declared paths fails: the check cannot clear what it cannot read.
func NoProtectedPath(t steward.TicketReader, p ProtectedPathReader) steward.Check {
	return check{steward.CheckNoProtectedPath, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		paths, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		if len(paths) == 0 {
			return steward.Evidence{Detail: "the ticket declares no paths, so it counts as protected"}, nil
		}
		hits, err := p.Protected(ctx, paths)
		if err != nil {
			return steward.Evidence{}, err
		}
		var parts []string
		for _, path := range paths {
			if reason, ok := hits[path]; ok {
				if reason != "" {
					path += " (" + reason + ")"
				}
				parts = append(parts, path)
			}
		}
		if len(parts) > 0 {
			return steward.Evidence{Detail: "protected: " + strings.Join(parts, ", ")}, nil
		}
		return steward.Evidence{OK: true, Detail: fmt.Sprintf("%d paths, none protected", len(paths))}, nil
	}}
}

// PinnedWords reads the pinned sensitive word list from fleet instructions.
type PinnedWords struct{ src DefaultSource }

// DefaultSource is the part of fleetinstructions.Service the pin needs.
type DefaultSource interface {
	DefaultOf(ctx context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error)
}

func NewPinnedWords(src DefaultSource) *PinnedWords { return &PinnedWords{src: src} }

// Pinned returns the tenant's default sensitive_words version, never a newer
// unpinned one, or nil when none is pinned.
func (p *PinnedWords) Pinned(ctx context.Context) (*WordList, error) {
	v, err := p.src.DefaultOf(ctx, fleetinstructions.KindSensitiveWords)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return &WordList{Version: fmt.Sprintf("%s v%d", v.Name, v.Version), Words: v.Body}, nil
}
