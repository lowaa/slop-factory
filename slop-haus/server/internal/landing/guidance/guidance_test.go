package guidance_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
)

type sourceFake struct{ landed []guidance.Landed }

func (s *sourceFake) LandedSince(context.Context, int64, int) ([]guidance.Landed, error) {
	return s.landed, nil
}
func (s *sourceFake) HeadFiles(context.Context, int64, int, string) ([]string, error) {
	return []string{"a.go"}, nil
}

type historyFake struct{ n int }

func (h historyFake) Collisions(context.Context, int64) (int, error) { return h.n, nil }

func ptr(b bool) *bool { return &b }

func advisor(n int) *guidance.Advisor {
	src := &sourceFake{landed: []guidance.Landed{{TicketID: 7, ChangeURL: "https://h/pr/7", MergeSHA: "abc", Files: []string{"a.go"}}}}
	return guidance.New(src, historyFake{n}, 0, slog.DiscardHandler)
}

func TestRed_OnlyGreenOnBothSidesIsAnInteraction(t *testing.T) {
	for name, tc := range map[string]struct {
		facts    guidance.Facts
		counts   bool
		names7   bool
		contains string
	}{
		"green on both":      {guidance.Facts{GreenOnBase: ptr(true), GreenOnHead: ptr(true)}, true, true, "comes from your change together"},
		"red on own head":    {guidance.Facts{GreenOnBase: ptr(true), GreenOnHead: ptr(false)}, false, false, "own failure"},
		"red on base":        {guidance.Facts{GreenOnBase: ptr(false), GreenOnHead: ptr(true)}, false, false, "red on the base's tip too"},
		"base unknown":       {guidance.Facts{GreenOnHead: ptr(true)}, false, false, "Fix them"},
		"head unknown":       {guidance.Facts{GreenOnBase: ptr(true)}, false, false, "Fix them"},
		"nothing is known":   {guidance.Facts{}, false, false, "Fix them"},
		"own head wins both": {guidance.Facts{GreenOnBase: ptr(false), GreenOnHead: ptr(false)}, false, false, "own failure"},
	} {
		t.Run(name, func(t *testing.T) {
			c := advisor(0).Red(context.Background(), 1, 1, "head", []string{"ci/build"}, tc.facts)
			assert.Equal(t, tc.counts, c.Counts)
			assert.Contains(t, c.Guidance, tc.contains)
			assert.Equal(t, tc.names7, len(c.Predecessors) == 1 && c.Predecessors[0] == 7)
		})
	}
}

func TestAtBound_TriggersOnTheThirdCollisionAndNotTheSecond(t *testing.T) {
	for prior, want := range map[int]bool{0: false, 1: false, 2: true, 3: true} {
		_, got := advisor(prior).AtBound(context.Background(), 1)
		assert.Equal(t, want, got, "after %d earlier collisions", prior)
	}
}
