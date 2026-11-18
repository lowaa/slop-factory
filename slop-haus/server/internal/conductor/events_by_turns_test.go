package conductor_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// turnsConversation is a conversation whose host has answered every message.
type turnsConversation struct {
	f     *fixture
	owned *agent.OwnedConductor
	c     *conductor.Conversation
	h     *host
	hseq  int64
}

func newTurnsConversation(t *testing.T) *turnsConversation {
	t.Helper()
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	tc := &turnsConversation{f: f, owned: owned, c: f.start(t, owned), h: f.polling(t, owned)}
	tc.hostEvents(t, ready(1, 1))
	tc.hseq = 1
	return tc
}

func (tc *turnsConversation) hostEvents(t *testing.T, events ...conductor.HostEvent) {
	t.Helper()
	tc.f.report(t, tc.h, tc.c, events...)
}

// turn is a user message, then the assistant's answer in the given sizes.
func (tc *turnsConversation) turn(t *testing.T, user string, answerSizes ...int) {
	t.Helper()
	_, err := tc.f.svc.Send(tc.f.ctx, tc.owned, tc.c.ID, &conductor.SendRequest{Text: user})
	require.NoError(t, err)
	n := int64(1)
	events := []conductor.HostEvent{}
	for _, size := range answerSizes {
		tc.hseq++
		events = append(events, conductor.HostEvent{HostSeq: tc.hseq, Kind: conductor.EventAssistant, Turn: &n, Body: strings.Repeat("a", size)})
	}
	tc.hseq++
	events = append(events, conductor.HostEvent{HostSeq: tc.hseq, Kind: conductor.EventTurnEnd, Turn: &n})
	tc.hostEvents(t, events...)
}

func (tc *turnsConversation) page(t *testing.T, before int64, turns int) *conductor.TranscriptPage {
	t.Helper()
	p, err := tc.f.svc.EventsByTurns(tc.f.ctx, tc.owned, tc.c.ID, before, turns)
	require.NoError(t, err)
	return p
}

func seqs(events []conductor.Event) []int64 {
	out := []int64{}
	for _, e := range events {
		out = append(out, e.Seq)
	}
	return out
}

func userCount(events []conductor.Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == conductor.EventUser {
			n++
		}
	}
	return n
}

func TestPagingBackwardsByTurnsVisitsEveryEventOnceWithoutSplittingATurn(t *testing.T) {
	tc := newTurnsConversation(t)
	for i := 0; i < 7; i++ {
		tc.turn(t, fmt.Sprintf("q%d", i), 10, 10)
	}
	all := tc.f.transcript(t, tc.owned, tc.c)

	var got []conductor.Event
	before := int64(0)
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10, "paging must end")
		p := tc.page(t, before, 3)
		require.NotEmpty(t, p.Events)
		if p.HasMore {
			assert.Equal(t, conductor.EventUser, p.Events[0].Kind, "a page starts at a turn's start")
		}
		if !p.HasMore {
			assert.Equal(t, int64(0), p.NextBefore)
		}
		got = append(append([]conductor.Event{}, p.Events...), got...)
		if !p.HasMore {
			break
		}
		assert.Equal(t, 3, userCount(p.Events), "turns respected")
		assert.Equal(t, p.Events[0].Seq, p.NextBefore)
		before = p.NextBefore
	}
	assert.Equal(t, seqs(all), seqs(got))
}

func TestTheFirstTurnOwnsTheEventsBeforeTheFirstUserMessage(t *testing.T) {
	tc := newTurnsConversation(t)
	tc.turn(t, "first", 5)
	tc.turn(t, "second", 5)

	p := tc.page(t, 0, 2)

	assert.False(t, p.HasMore)
	assert.Equal(t, conductor.EventReady, p.Events[0].Kind, "the ready before any message comes with the first turn")
	assert.Equal(t, seqs(tc.f.transcript(t, tc.owned, tc.c)), seqs(p.Events))
}

func TestHasMoreIsFalseExactlyWhenTheOldestTurnIsTheFirst(t *testing.T) {
	tc := newTurnsConversation(t)
	tc.turn(t, "first", 5)
	tc.turn(t, "second", 5)

	assert.False(t, tc.page(t, 0, 2).HasMore, "exactly all the turns")
	p := tc.page(t, 0, 1)
	assert.True(t, p.HasMore)
	older := tc.page(t, p.NextBefore, 1)
	assert.False(t, older.HasMore)
	assert.Equal(t, int64(0), older.NextBefore)
}

func TestAPageStopsAddingTurnsPastTheSizeCapButKeepsOne(t *testing.T) {
	tc := newTurnsConversation(t)
	half := conductor.MaxPageBytes/2 + 1024
	tc.turn(t, "a", half/3, half/3, half/3)
	tc.turn(t, "b", half/3, half/3, half/3)
	tc.turn(t, "c", half/3, half/3, half/3)

	p := tc.page(t, 0, 50)

	assert.Equal(t, 1, userCount(p.Events), "the second turn would pass the cap")
	assert.True(t, p.HasMore)
	assert.Equal(t, "c", p.Events[0].Body)

	next := tc.page(t, p.NextBefore, 50)
	assert.Equal(t, "b", next.Events[0].Body)
	assert.True(t, next.HasMore)
}

func TestATurnLargerThanTheCapIsReturnedWhole(t *testing.T) {
	tc := newTurnsConversation(t)
	tc.turn(t, "small", 10)
	tc.turn(t, "big", 60000, 60000, 60000, 60000, 60000)

	p := tc.page(t, 0, 5)

	assert.Equal(t, 1, userCount(p.Events))
	assert.Equal(t, "big", p.Events[0].Body)
	assert.Len(t, p.Events, 1+5+1, "the message, the whole answer and its end")
	assert.True(t, p.HasMore)
	assert.Equal(t, "small", tc.page(t, p.NextBefore, 5).Events[1].Body, "after the ready that opens the first turn")
}

func TestARecentPromptsPageHoldsTheLastTenDistinctNonBlankMessages(t *testing.T) {
	tc := newTurnsConversation(t)
	for i := 0; i < 8; i++ {
		tc.turn(t, fmt.Sprintf("p%d", i), 1)
	}
	tc.turn(t, "p3", 1)
	for i := 8; i < 12; i++ {
		tc.turn(t, fmt.Sprintf("p%d", i), 1)
	}

	p := tc.page(t, 0, 1)

	assert.Equal(t, []string{"p2", "p4", "p5", "p6", "p7", "p3", "p8", "p9", "p10", "p11"}, p.Prompts)
	assert.Nil(t, tc.page(t, p.NextBefore, 1).Prompts, "only the first page carries them")
}

func TestAnotherConductorsTranscriptIsNotReadableByTurns(t *testing.T) {
	tc := newTurnsConversation(t)
	tc.turn(t, "secret", 1)
	other := tc.f.owned(t, tc.f.owner, "c2")

	_, err := tc.f.svc.EventsByTurns(tc.f.ctx, other, tc.c.ID, 0, 1)

	assert.ErrorIs(t, err, conductor.ErrNotFound)
}

func TestBackwardReadsOverHTTP(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{"message": "one"})
	url := r.base + "/" + c.ID + "/events"

	type page struct {
		eventsJSON
		HasMore    *bool    `json:"has_more"`
		NextBefore *int64   `json:"next_before"`
		Prompts    []string `json:"prompts"`
	}
	got := decode[page](t, r.api.do("owner", http.MethodGet, url+"?turns=5", nil))
	require.Len(t, got.Events, 1)
	require.NotNil(t, got.HasMore)
	assert.False(t, *got.HasMore)
	assert.Equal(t, []string{"one"}, got.Prompts)

	for _, text := range []string{"two", "three"} {
		rec := r.api.do("owner", http.MethodPost, r.base+"/"+c.ID+"/messages", map[string]any{"text": text})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}
	newest := decode[page](t, r.api.do("owner", http.MethodGet, url+"?turns=1", nil))
	require.Len(t, newest.Events, 1)
	assert.Equal(t, "three", newest.Events[0].Body)
	require.True(t, *newest.HasMore)
	older := decode[page](t, r.api.do("owner", http.MethodGet, fmt.Sprintf("%s?before=%d&turns=1", url, *newest.NextBefore), nil))
	require.Len(t, older.Events, 1)
	assert.Equal(t, "two", older.Events[0].Body)
	assert.Nil(t, older.Prompts)

	forward := decode[page](t, r.api.do("owner", http.MethodGet, url, nil))
	assert.Nil(t, forward.HasMore, "a forward read is as it was")
	assert.Nil(t, forward.Prompts)

	for _, q := range []string{"?after=1&before=5", "?after=1&turns=2", "?limit=5&turns=2"} {
		rec := r.api.do("owner", http.MethodGet, url+q, nil)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, q)
	}
	assert.Equal(t, http.StatusUnprocessableEntity, r.api.do("owner", http.MethodGet, url+"?turns=51", nil).Code)
}
