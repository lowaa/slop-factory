package hookkit_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

func TestCleanStripsNULs(t *testing.T) {
	assert.Equal(t, "ab", hookkit.Clean("a\x00b", 10))
	assert.Equal(t, "ab", hookkit.Clean("a\x00b\x00", 2), "NULs do not count against the limit")
}

func TestCleanTruncatesOnARuneBoundary(t *testing.T) {
	assert.Equal(t, "abc", hookkit.Clean("abc", 3))
	assert.Equal(t, "ab", hookkit.Clean("abc", 2))
	assert.Equal(t, "", hookkit.Clean("abc", 0))

	s := "a\u00e9\u65e5\U0001F600"
	for n := 0; n <= len(s); n++ {
		got := hookkit.Clean(s, n)
		assert.LessOrEqual(t, len(got), n)
		assert.True(t, utf8.ValidString(got), "cut at %d: %q", n, got)
		assert.True(t, strings.HasPrefix(s, got))
	}
	assert.Equal(t, "a", hookkit.Clean(s, 2))
	assert.Equal(t, "a\u00e9", hookkit.Clean(s, 3))
}

func TestValidHeader(t *testing.T) {
	for name, tc := range map[string]struct {
		v    string
		max  int
		want bool
	}{
		"plain":        {"abc-123", 10, true},
		"at max":       {"abcde", 5, true},
		"over max":     {"abcdef", 5, false},
		"empty":        {"", 5, false},
		"newline":      {"a\nb", 10, false},
		"nul":          {"a\x00b", 10, false},
		"tab":          {"a\tb", 10, false},
		"invalid utf8": {"a\xffb", 10, false},
		"unicode":      {"caf\u00e9", 10, true},
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, hookkit.ValidHeader(tc.v, tc.max)) })
	}
}

func TestReadBodyLimit(t *testing.T) {
	read := func(body string, max int64) ([]byte, error) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		return hookkit.ReadBody(httptest.NewRecorder(), r, max)
	}

	got, err := read("12345", 5)
	require.NoError(t, err)
	assert.Equal(t, "12345", string(got), "exactly max is accepted")

	_, err = read("123456", 5)
	assert.ErrorIs(t, err, hookkit.ErrTooLarge)

	got, err = read("", 5)
	require.NoError(t, err)
	assert.Empty(t, got)
}
