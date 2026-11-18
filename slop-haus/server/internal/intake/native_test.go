package intake_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestTheNativeAdapterIsRegistered(t *testing.T) {
	a, ok := intake.AdapterFor("native")
	require.True(t, ok)
	assert.NotNil(t, a)
	_, ok = intake.AdapterFor("nope")
	assert.False(t, ok)
}

func TestNativeParsesEveryField(t *testing.T) {
	ev, err := intake.NativeAdapter().Parse([]byte(`{"key":"k1","event":"open","title":"T","body":"B",
		"status":"preparing","priority":"high","link":"https://x.example/1","context":{"a":1}}`))
	require.NoError(t, err)
	assert.Equal(t, &intake.Event{
		Key: "k1", Kind: intake.EventOpen, Title: "T", Body: "B", Link: "https://x.example/1",
		Status: ticket.StatusPreparing, Priority: model.PriorityHigh, Context: []byte(`{"a":1}`),
	}, ev)
}

func TestNativeRejectsFieldsASenderMustNotSet(t *testing.T) {
	for _, field := range []string{"roles", "paths", "assignment", "role_match", "blockers", "scope", "anything"} {
		t.Run(field, func(t *testing.T) {
			const secret = "SECRET-VALUE-9f3"
			_, err := intake.NativeAdapter().Parse([]byte(`{"key":"k","event":"open","title":"t","` + field + `":"` + secret + `"}`))
			require.Error(t, err)
			assert.Contains(t, err.Error(), field)
			assert.NotContains(t, err.Error(), secret)
		})
	}
}

func TestNativeRejectsMalformedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `hello`,
		"empty":         ``,
		"array":         `[]`,
		"truncated":     `{"key":"k"`,
		"two values":    `{"key":"k","event":"open","title":"t"}{"key":"k2"}`,
		"trailing junk": `{"key":"k","event":"open","title":"t"} x`,
		"wrong type":    `{"key":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := intake.NativeAdapter().Parse([]byte(body))
			assert.Error(t, err)
		})
	}
	_, err := intake.NativeAdapter().Parse([]byte(`{"key":"k","event":"open","title":"t"}  `))
	assert.NoError(t, err, "trailing whitespace is not a second value")
}

func ok() *intake.Event {
	return &intake.Event{Key: "k", Kind: intake.EventOpen, Title: "t"}
}

func TestCheckLimits(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*intake.Event)
		want string // empty: accepted
	}{
		"baseline":           {func(*intake.Event) {}, ""},
		"key missing":        {func(e *intake.Event) { e.Key = "" }, "key"},
		"key blank":          {func(e *intake.Event) { e.Key = "  \u200b " }, "key"},
		"key at limit":       {func(e *intake.Event) { e.Key = strings.Repeat("k", intake.MaxKey) }, ""},
		"key over limit":     {func(e *intake.Event) { e.Key = strings.Repeat("k", intake.MaxKey+1) }, "key"},
		"key newline":        {func(e *intake.Event) { e.Key = "a\nb" }, "key"},
		"key tab":            {func(e *intake.Event) { e.Key = "a\tb" }, "key"},
		"bad event":          {func(e *intake.Event) { e.Kind = "close" }, "event must be"},
		"no event":           {func(e *intake.Event) { e.Kind = "" }, "event must be"},
		"open without title": {func(e *intake.Event) { e.Title = " " }, "needs a title"},
		"update no title":    {func(e *intake.Event) { e.Kind = intake.EventUpdate; e.Title = "" }, ""},
		"resolve no title":   {func(e *intake.Event) { e.Kind = intake.EventResolve; e.Title = "" }, ""},
		"title at limit":     {func(e *intake.Event) { e.Title = strings.Repeat("t", intake.MaxTitle) }, ""},
		"title over limit":   {func(e *intake.Event) { e.Title = strings.Repeat("t", intake.MaxTitle+1) }, "title"},
		"body at limit":      {func(e *intake.Event) { e.Body = strings.Repeat("b", intake.MaxBody) }, ""},
		"body over limit":    {func(e *intake.Event) { e.Body = strings.Repeat("b", intake.MaxBody+1) }, "body"},
		"link https":         {func(e *intake.Event) { e.Link = "https://x.example/a?b=1" }, ""},
		"link http":          {func(e *intake.Event) { e.Link = "http://x.example" }, ""},
		"link javascript":    {func(e *intake.Event) { e.Link = "javascript:alert(1)" }, "link"},
		"link ftp":           {func(e *intake.Event) { e.Link = "ftp://x.example" }, "link"},
		"link no host":       {func(e *intake.Event) { e.Link = "https://" }, "link"},
		"link relative":      {func(e *intake.Event) { e.Link = "/a/b" }, "link"},
		"link over limit": {func(e *intake.Event) {
			e.Link = "https://x.example/" + strings.Repeat("a", intake.MaxLink)
		}, "link"},
		"status open":        {func(e *intake.Event) { e.Status = ticket.StatusOpen }, ""},
		"status preparing":   {func(e *intake.Event) { e.Status = ticket.StatusPreparing }, ""},
		"status in_progress": {func(e *intake.Event) { e.Status = ticket.StatusInProgress }, "status"},
		"status done":        {func(e *intake.Event) { e.Status = ticket.StatusDone }, "status"},
		"status cancelled":   {func(e *intake.Event) { e.Status = ticket.StatusCancelled }, "status"},
		"status review":      {func(e *intake.Event) { e.Status = ticket.StatusReview }, "status"},
		"status blocked":     {func(e *intake.Event) { e.Status = ticket.StatusBlocked }, "status"},
		"status discussion":  {func(e *intake.Event) { e.Status = ticket.StatusDiscussion }, "status"},
		"status parked":      {func(e *intake.Event) { e.Status = ticket.StatusParked }, "status"},
		"priority urgent":    {func(e *intake.Event) { e.Priority = model.PriorityUrgent }, ""},
		"priority bogus":     {func(e *intake.Event) { e.Priority = "critical" }, "priority"},
		"context object":     {func(e *intake.Event) { e.Context = []byte(`{"a":[1,2]}`) }, ""},
		"context empty obj":  {func(e *intake.Event) { e.Context = []byte(`{}`) }, ""},
		"context array":      {func(e *intake.Event) { e.Context = []byte(`[1]`) }, "context"},
		"context string":     {func(e *intake.Event) { e.Context = []byte(`"x"`) }, "context"},
		"context number":     {func(e *intake.Event) { e.Context = []byte(`5`) }, "context"},
		"context null":       {func(e *intake.Event) { e.Context = []byte(`null`) }, "context"},
		"context invalid":    {func(e *intake.Event) { e.Context = []byte(`{`) }, "context"},
		"context over limit": {func(e *intake.Event) {
			e.Context = []byte(`{"a":"` + strings.Repeat("x", intake.MaxContext) + `"}`)
		}, "context"},
	} {
		t.Run(name, func(t *testing.T) {
			e := ok()
			tc.edit(e)
			err := intake.CheckEvent(e)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCheckStripsInvisibleCharacters(t *testing.T) {
	const junk = "\u200b\u202e\U000E0041\x00\x07\x1b\u2028"
	e := &intake.Event{
		Key: "k" + junk + "1", Kind: intake.EventOpen,
		Title:   "ti" + junk + "tle",
		Body:    "line1\n\tline2" + junk + "\r\nline3",
		Context: []byte(`{"a":"x\u200by\u202e\u0000z","n\u0000k":["\u0000q",{"d":"\u200b1"}]}`),
	}
	require.NoError(t, intake.CheckEvent(e))
	assert.Equal(t, "k1", e.Key)
	assert.Equal(t, "title", e.Title)
	assert.Equal(t, "line1\n\tline2\nline3", e.Body)
	assert.JSONEq(t, `{"a":"xyz","nk":["q",{"d":"1"}]}`, string(e.Context), "escaped NULs would make jsonb refuse the row")
}

func TestCheckCollapsesTitleWhitespaceAndTrimsKey(t *testing.T) {
	e := &intake.Event{Key: "  k  ", Kind: intake.EventOpen, Title: " a \n\t b  "}
	require.NoError(t, intake.CheckEvent(e))
	assert.Equal(t, "k", e.Key)
	assert.Equal(t, "a b", e.Title)
}

func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	assert.Equal(t, "abc", intake.Truncate("abc", 5))
	assert.Equal(t, "ab", intake.Truncate("abc", 2))
	assert.Equal(t, "a", intake.Truncate("a\u00e9", 2), "the two-byte rune is dropped whole")
	assert.Equal(t, "a\u00e9", intake.Truncate("a\u00e9", 3))
}
