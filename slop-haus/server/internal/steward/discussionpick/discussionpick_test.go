package discussionpick_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionpick"
)

// fake decides nothing: it returns what the test set.
type fake struct {
	disc discussionpick.Discussion
	// answer is the text the proposal carries, as the engine hands it over.
	answer    string
	list      *discussionpick.WordList
	paths     []string
	protected map[string]string
	failAt    string
}

func (f *fake) fail(read string) error {
	if f.failAt == read {
		return errors.New(read + " failed")
	}
	return nil
}

func (f *fake) Discussion(context.Context, int64) (*discussionpick.Discussion, error) {
	return &f.disc, f.fail("Discussion")
}
func (f *fake) Pinned(context.Context) (*discussionpick.WordList, error) {
	return f.list, f.fail("Pinned")
}
func (f *fake) Revision(context.Context, int64) (int, error)   { return 1, nil }
func (f *fake) Status(context.Context, int64) (string, error)  { return "in_progress", nil }
func (f *fake) Paths(context.Context, int64) ([]string, error) { return f.paths, f.fail("Paths") }
func (f *fake) Protected(context.Context, []string) (map[string]string, error) {
	return f.protected, f.fail("Protected")
}

func good() *fake {
	return &fake{
		disc: discussionpick.Discussion{
			Question: "Which approach for the cache?",
			Handoff:  "Two options.\n- **Recommended:** Use an LRU cache\nA is slower.",
		},
		answer: "Use an LRU cache",
		list:   &discussionpick.WordList{Version: "words v1", Words: "secret\nprod data\n"},
		paths:  []string{"a/b/"},
	}
}

func run(t *testing.T, f *fake) map[string]steward.Evidence {
	out := map[string]steward.Evidence{}
	for _, c := range checksOf(f) {
		ev, err := c.Run(context.Background(), &steward.Subject{TicketID: 1, Body: f.answer})
		require.NoError(t, err)
		assert.Equal(t, c.Name(), ev.Check)
		out[c.Name()] = ev
	}
	return out
}

func TestCategoryChecksAreExactlyTheSpecified(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"option_recommended_by_worker", "topic_not_sensitive", "no_human_only_marker",
		"head_unchanged", "ticket_rev_unchanged", "no_protected_path",
	}, category(t).Checks)
	assert.Equal(t, []string{steward.KindDiscussionAnswer}, category(t).Kinds)
}

func TestNoOpenDiscussionFails(t *testing.T) {
	f := &nilDisc{good()}
	for _, c := range checksOf(f) {
		ev, err := c.Run(context.Background(), &steward.Subject{TicketID: 1})
		require.NoError(t, err)
		if c.Name() != steward.CheckNoProtectedPath {
			assert.False(t, ev.OK, c.Name())
		}
	}
}

type nilDisc struct{ *fake }

func (nilDisc) Discussion(context.Context, int64) (*discussionpick.Discussion, error) {
	return nil, nil
}

func TestReadErrorsFailTheChecks(t *testing.T) {
	for _, failAt := range []string{"Discussion", "Pinned", "Paths", "Protected"} {
		f := good()
		f.failAt = failAt
		failed := false
		for _, c := range checksOf(f) {
			if _, err := c.Run(context.Background(), &steward.Subject{TicketID: 1}); err != nil {
				failed = true
			}
		}
		assert.True(t, failed, failAt)
	}
}

func TestCategoryChecksAreProvided(t *testing.T) {
	f := good()
	got := run(t, f)
	for _, name := range category(t).Checks {
		switch name {
		case steward.CheckNoHumanOnlyMarker, steward.CheckHeadUnchanged, steward.CheckTicketRevUnchanged:
			continue // the steward's own
		}
		assert.Contains(t, got, name)
	}
	assert.Equal(t, steward.ClassJudgment, category(t).Class)
	for name, ev := range got {
		assert.True(t, ev.OK, name)
	}
}

func TestOptionRecommendedByWorker(t *testing.T) {
	cases := map[string]struct {
		mutate func(*fake)
		ok     bool
	}{
		"exact":             {func(*fake) {}, true},
		"different":         {func(f *fake) { f.answer = "Use a FIFO cache" }, false},
		"trailing space":    {func(f *fake) { f.answer = "Use an LRU cache " }, false},
		"case differs":      {func(f *fake) { f.answer = "use an lru cache" }, false},
		"no recommendation": {func(f *fake) { f.disc.Handoff = "Two options, A and B." }, false},
		"conflicting": {func(f *fake) {
			f.disc.Handoff += "\nRecommended: Use a FIFO cache"
		}, false},
		"repeated same": {func(f *fake) {
			f.disc.Handoff += "\nRecommended: Use an LRU cache"
		}, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := good()
			c.mutate(f)
			ev := run(t, f)[steward.CheckOptionRecommendedByAgent]
			assert.Equal(t, c.ok, ev.OK, ev.Detail)
		})
	}
}

func TestRecommendedRecordsTheLine(t *testing.T) {
	f := good()
	rec, err := discussionpick.Recommended(&f.disc)
	require.NoError(t, err)
	assert.Equal(t, "Which approach for the cache?", rec.Question)
	assert.Equal(t, "Use an LRU cache", rec.Option)
	assert.Equal(t, "- **Recommended:** Use an LRU cache", rec.Line)
	ev := run(t, f)[steward.CheckOptionRecommendedByAgent]
	assert.Contains(t, ev.Detail, rec.Line)
}

func TestTopicNotSensitive(t *testing.T) {
	t.Run("question matches, evidence names word and version", func(t *testing.T) {
		f := good()
		f.disc.Question = "Should we log the SECRET value?"
		ev := run(t, f)[steward.CheckTopicNotSensitive]
		assert.False(t, ev.OK)
		assert.Contains(t, ev.Detail, `"secret"`)
		assert.Contains(t, ev.Detail, "words v1")
	})
	t.Run("recommended option matches even if the answer differs", func(t *testing.T) {
		f := good()
		f.disc.Handoff = "Recommended: wipe the prod data"
		f.answer = "something else"
		assert.False(t, run(t, f)[steward.CheckTopicNotSensitive].OK)
	})
	t.Run("whole words only", func(t *testing.T) {
		f := good()
		f.disc.Question = "Which secretary?"
		assert.True(t, run(t, f)[steward.CheckTopicNotSensitive].OK)
	})
	t.Run("no pinned list fails closed", func(t *testing.T) {
		f := good()
		f.list = nil
		assert.False(t, run(t, f)[steward.CheckTopicNotSensitive].OK)
	})
	t.Run("read error", func(t *testing.T) {
		f := good()
		f.failAt = "Pinned"
		var err error
		for _, c := range checksOf(f) {
			if c.Name() == steward.CheckTopicNotSensitive {
				_, err = c.Run(context.Background(), &steward.Subject{TicketID: 1})
			}
		}
		assert.Error(t, err)
	})
}

type defaults struct {
	v   *fleetinstructions.Version
	got fleetinstructions.Kind
}

func (d *defaults) DefaultOf(_ context.Context, k fleetinstructions.Kind) (*fleetinstructions.Version, error) {
	d.got = k
	return d.v, nil
}

// An unpinned newer version is never offered by DefaultOf, so the list in use
// stays the pinned one: a word only the newer version has does not match.
func TestPinnedWordsUsesTheDefaultVersionOnly(t *testing.T) {
	src := &defaults{v: &fleetinstructions.Version{Name: "words", Version: 1, Body: "secret"}}
	pinned := discussionpick.NewPinnedWords(src)
	list, err := pinned.Pinned(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fleetinstructions.KindSensitiveWords, src.got)
	assert.Equal(t, "words v1", list.Version)

	f := good()
	f.disc.Question = "Delete the ledger?" // "ledger" is only in a hypothetical unpinned v2
	check := discussionpick.TopicNotSensitive(f, pinned)
	ev, err := check.Run(context.Background(), &steward.Subject{TicketID: 1})
	require.NoError(t, err)
	assert.True(t, ev.OK)
	assert.Equal(t, "sensitive words words v1", ev.Ref)

	src.v = nil
	list, err = pinned.Pinned(context.Background())
	require.NoError(t, err)
	assert.Nil(t, list)
}

func TestNoProtectedPath(t *testing.T) {
	f := good()
	f.protected = map[string]string{"a/b/": "deploys"}
	ev := run(t, f)[steward.CheckNoProtectedPath]
	assert.False(t, ev.OK)
	assert.Contains(t, ev.Detail, "deploys")

	f = good()
	f.paths = nil
	assert.False(t, run(t, f)[steward.CheckNoProtectedPath].OK)
}

func TestComparator(t *testing.T) {
	d := func(x steward.Decision) *steward.Decision { return &x }
	owner := func(option, answer string) agreement.OwnerAnswer {
		return func(*steward.Record) (string, string, bool) { return option, answer, true }
	}
	now := time.Now()
	cases := map[string]struct {
		r     steward.Record
		owner agreement.OwnerAnswer
		want  agreement.Outcome
	}{
		"approved":           {steward.Record{HumanDecision: d(steward.DecisionApproved)}, nil, agreement.Agreed},
		"rejected":           {steward.Record{HumanDecision: d(steward.DecisionRejected)}, nil, agreement.Disagreed},
		"edited unknown":     {steward.Record{HumanDecision: d(steward.DecisionEdited)}, nil, agreement.Disagreed},
		"edited same option": {steward.Record{HumanDecision: d(steward.DecisionEdited)}, owner("A", "A"), agreement.Agreed},
		"edited free text":   {steward.Record{HumanDecision: d(steward.DecisionEdited)}, owner("A", "A, but also B"), agreement.Disagreed},
		"vetoed":             {steward.Record{VetoedAt: &now, HumanDecision: d(steward.DecisionApproved)}, nil, agreement.Disagreed},
		"undone":             {steward.Record{UndoneAt: &now, HumanDecision: d(steward.DecisionApproved)}, nil, agreement.Disagreed},
		"undecided":          {steward.Record{}, nil, agreement.Undecided},
		"needs human":        {steward.Record{Verdict: steward.VerdictNeedsHuman, HumanDecision: d(steward.DecisionApproved)}, nil, agreement.Undecided},
		"withdrawn":          {steward.Record{HumanDecision: d(steward.DecisionWithdrawn)}, nil, agreement.Undecided},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, agreement.PickComparator(c.owner)(&c.r))
		})
	}
}

func TestComparatorIsTheRegisteredOne(t *testing.T) {
	assert.True(t, agreement.Registered(discussionpick.CategoryID))
	edited := steward.DecisionEdited
	rec := &steward.Record{Category: discussionpick.CategoryID, HumanDecision: &edited}
	assert.Equal(t, agreement.Disagreed, agreement.ComparatorFor(discussionpick.CategoryID)(rec), "an edit with no owner answer recorded")
}

type readers interface {
	discussionpick.DiscussionReader
	discussionpick.WordListReader
	steward.TicketReader
	discussionpick.ProtectedPathReader
}

func checksOf(f readers) []steward.Check {
	return []steward.Check{
		discussionpick.OptionRecommendedByWorker(f), discussionpick.TopicNotSensitive(f, f), discussionpick.NoProtectedPath(f, f),
	}
}

func category(t *testing.T) steward.Category {
	c, ok := steward.Lookup(discussionpick.CategoryID)
	require.True(t, ok, "discussion_pick is on the allowlist")
	return c
}
