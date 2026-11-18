package conductor_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func (f *fixture) startWith(t *testing.T, message string, clientID *uuid.UUID) *conductor.Conversation {
	t.Helper()
	owned := f.owned(t, f.owner, "c-"+uuid.NewString()[:8])
	c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: &message, ClientID: clientID})
	require.NoError(t, err)
	return c
}

func strp(s string) *string { return &s }

func TestFirstLineComesFromTheOpeningMessage(t *testing.T) {
	f := newFixture(t)
	for name, tc := range map[string]struct{ message, want string }{
		"one line":            {"fix the login bug", "fix the login bug"},
		"several lines":       {"fix the login bug\nit is on the second page\nplease", "fix the login bug"},
		"leading blank":       {"\n  \n\t\n  fix   the\tlogin  bug  \nmore", "fix the login bug"},
		"carriage returns":    {"first\r\nsecond", "first"},
		"multibyte":           {"日本語のテスト 🚀 ok", "日本語のテスト 🚀 ok"},
		"long, ascii":         {strings.Repeat("a", 300), strings.Repeat("a", conductor.MaxFirstLineRunes)},
		"long, emoji":         {strings.Repeat("🚀", 300), strings.Repeat("🚀", conductor.MaxFirstLineRunes)},
		"cut ends in a space": {strings.Repeat("a", conductor.MaxFirstLineRunes-1) + " bbb", strings.Repeat("a", conductor.MaxFirstLineRunes-1)},
	} {
		t.Run(name, func(t *testing.T) {
			c := f.startWith(t, tc.message, nil)
			require.NotNil(t, c.FirstLine)
			assert.Equal(t, tc.want, *c.FirstLine)
		})
	}
}

func TestFirstLineComesFromTheFirstSendWhenTheStartHadNoMessage(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	require.Nil(t, c.FirstLine)

	_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "first words\nmore"})
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "different words"})
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FirstLine)
	assert.Equal(t, "first words", *got.FirstLine, "a later message never changes it")
}

func TestALaterMessageDoesNotChangeTheFirstLine(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: strp("opening")})
	require.NoError(t, err)

	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "second"})
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "opening", *got.FirstLine)
}

func TestARetriedStartLeavesOneFirstLine(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clientID := uuid.New()
	first, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: strp("original"), ClientID: &clientID})
	require.NoError(t, err)

	again, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: strp("a different retry"), ClientID: &clientID})
	require.NoError(t, err)

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, "original", *again.FirstLine)
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestTheOwnerRenamesAndClearsASession(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: strp("default name")})
	require.NoError(t, err)

	renamed, err := f.svc.Rename(f.ctx, owned, c.ID, strp("  My name  "))
	require.NoError(t, err)
	assert.Equal(t, strp("My name"), renamed.Title, "trimmed")
	assert.Equal(t, strp("default name"), renamed.FirstLine, "the default is left alone")
	assert.True(t, renamed.UpdatedAt.Equal(c.UpdatedAt), "a rename is not activity")

	changed, err := f.svc.Rename(f.ctx, owned, c.ID, strp("日本語 🚀"))
	require.NoError(t, err)
	assert.Equal(t, strp("日本語 🚀"), changed.Title)

	for _, clear := range []*string{nil, strp(""), strp("   ")} {
		_, err = f.svc.Rename(f.ctx, owned, c.ID, strp("again"))
		require.NoError(t, err)
		cleared, err := f.svc.Rename(f.ctx, owned, c.ID, clear)
		require.NoError(t, err)
		assert.Nil(t, cleared.Title)
		assert.Equal(t, strp("default name"), cleared.FirstLine)
	}

	// An ended session can still be named.
	_, err = f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	stopped, err := f.svc.Rename(f.ctx, owned, c.ID, strp("after the end"))
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, stopped.State)
	assert.Equal(t, strp("after the end"), stopped.Title)
}

func TestARenameIsRefusedRatherThanCut(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	for name, title := range map[string]string{
		"too long":       strings.Repeat("a", conductor.MaxTitleRunes+1),
		"too long runes": strings.Repeat("🚀", conductor.MaxTitleRunes+1),
		"newline":        "two\nlines",
		"tab":            "a\tb",
		"nul":            "a\x00b",
		"escape":         "a\x1b[31mb",
		"bad utf-8":      "a\xffb",
	} {
		_, err := f.svc.Rename(f.ctx, owned, c.ID, &title)
		assert.ErrorIs(t, err, conductor.ErrInvalid, name)
	}
	atLimit := strings.Repeat("🚀", conductor.MaxTitleRunes)
	got, err := f.svc.Rename(f.ctx, owned, c.ID, &atLimit)
	require.NoError(t, err)
	assert.Equal(t, atLimit, *got.Title)
}

func TestRenamingAnotherConductorsSessionIsNotFound(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c := f.start(t, mine)

	_, err := f.svc.Rename(f.ctx, theirs, c.ID, strp("hijack"))
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	got, err := f.svc.Get(f.ctx, mine, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Title)
}

func TestNamesOverHTTP(t *testing.T) {
	r := newRest(t)
	type named struct {
		FirstLine *string `json:"first_line"`
		Title     *string `json:"title"`
	}
	started := r.start(t, map[string]any{"message": "hello there\nmore"})
	path := r.base + "/" + started.ID

	get := decode[named](t, r.api.do("owner", http.MethodGet, path, nil))
	assert.Equal(t, strp("hello there"), get.FirstLine)
	assert.Nil(t, get.Title)

	rec := r.api.do("owner", http.MethodPatch, path, map[string]any{"title": "Mine"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patched := decode[named](t, rec)
	assert.Equal(t, strp("Mine"), patched.Title)
	assert.Equal(t, strp("hello there"), patched.FirstLine)

	list := decode[struct {
		Conversations []named `json:"conversations"`
	}](t, r.api.do("owner", http.MethodGet, r.base, nil))
	require.Len(t, list.Conversations, 1)
	assert.Equal(t, strp("Mine"), list.Conversations[0].Title)
	assert.Equal(t, strp("hello there"), list.Conversations[0].FirstLine)

	detail := decode[named](t, r.api.do("owner", http.MethodGet, path, nil))
	assert.Equal(t, strp("Mine"), detail.Title)

	assert.Equal(t, http.StatusUnprocessableEntity,
		r.api.do("owner", http.MethodPatch, path, map[string]any{"title": strings.Repeat("a", 101)}).Code)
	assert.Equal(t, http.StatusUnprocessableEntity,
		r.api.do("owner", http.MethodPatch, path, map[string]any{}).Code, "title must be given, null to clear")

	cleared := r.api.do("owner", http.MethodPatch, path, `{"title":null}`)
	require.Equal(t, http.StatusOK, cleared.Code, cleared.Body.String())
	assert.Nil(t, decode[named](t, cleared).Title)
}

func TestRenamingInAnotherTenantIsNotFound(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	c := f.start(t, mine)
	elsewhere := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))

	// Row level security hides the row from a request running as another tenant.
	_, err := f.svc.Rename(elsewhere, mine, c.ID, strp("hijack"))
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	got, err := f.svc.Get(f.ctx, mine, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Title)
}
