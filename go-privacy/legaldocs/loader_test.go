package legaldocs_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

type fixedFragments map[string]string

func (f fixedFragments) Fragment(name string) (string, bool) {
	s, ok := f[name]
	return s, ok
}

func newLoader(t *testing.T, frags legaldocs.Fragments) *legaldocs.Loader {
	t.Helper()
	kinds, err := legaldocs.NewKinds("beta_terms")
	require.NoError(t, err)
	l, err := legaldocs.NewLoader(&legaldocs.LoaderConfig{Kinds: kinds, Fragments: frags})
	require.NoError(t, err)
	return l
}

func file(headers ...string) []byte {
	return []byte("---\n" + strings.Join(headers, "\n") + "\n---\n# Synthetic title\n\nSynthetic body.\n")
}

const valid = "kind: terms\nversion: v1\neffective: 2026-01-01\nlocale: en\nmaterial: true"

func TestParseReadsFrontMatter(t *testing.T) {
	v, err := newLoader(t, nil).Parse("legal/terms/v1.md", file(strings.Replace(valid, "locale: en", "locale: fr", 1), "reviewed: true", "reviewed_by: Synthetic Reviewer", "reviewed_on: 2026-01-02"))
	require.NoError(t, err)
	assert.Equal(t, "terms", v.Kind)
	assert.Equal(t, "v1", v.Version)
	assert.Equal(t, "fr", v.Locale)
	assert.Equal(t, jan1, v.Effective)
	assert.True(t, v.Material)
	assert.True(t, v.Reviewed)
	assert.Equal(t, "Synthetic Reviewer", v.ReviewedBy)
	assert.Equal(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), *v.ReviewedOn)
	assert.Equal(t, "# Synthetic title\n\nSynthetic body.\n", v.Body)
}

func TestParseDefaultsReviewedToFalse(t *testing.T) {
	v, err := newLoader(t, nil).Parse("legal/terms/v1.md", file(valid))
	require.NoError(t, err)
	assert.False(t, v.Reviewed, "a file is a draft until it says otherwise")
}

func TestParseRefusals(t *testing.T) {
	cases := map[string]struct {
		path string
		data []byte
		want string
	}{
		"material missing":              {"legal/terms/v1.md", file("kind: terms", "version: v1", "locale: en", "effective: 2026-01-01"), "material is required"},
		"effective missing":             {"legal/terms/v1.md", file("kind: terms", "version: v1", "locale: en", "material: false"), "effective is required"},
		"unknown kind":                  {"legal/other/v1.md", file("kind: other", "version: v1", "effective: 2026-01-01", "locale: en", "material: true"), "not a declared kind"},
		"unknown key":                   {"legal/terms/v1.md", file(valid, "materail: true"), "not valid"},
		"kind differs from directory":   {"legal/privacy_policy/v1.md", file(valid), "directory"},
		"version differs from file":     {"legal/terms/v2.md", file(valid), "file name"},
		"bad version":                   {"legal/terms/a b.md", file("kind: terms", "version: a b", "effective: 2026-01-01", "locale: en", "material: true"), "version"},
		"bad locale":                    {"legal/terms/v1.md", file(valid, "locale: EN_us"), "locale"},
		"no front matter":               {"legal/terms/v1.md", []byte("# Title\n"), "front matter"},
		"unclosed front matter":         {"legal/terms/v1.md", []byte("---\nkind: terms\n# Title\n"), "not closed"},
		"empty body":                    {"legal/terms/v1.md", []byte("---\n" + valid + "\n---\n\n"), "empty"},
		"reviewed without reviewer":     {"legal/terms/v1.md", file(valid, "reviewed: true"), "reviewed_by"},
		"reviewed with a TODO":          {"legal/terms/v1.md", []byte("---\n" + valid + "\nreviewed: true\nreviewed_by: A\nreviewed_on: 2026-01-02\n---\n[[TODO-LEGAL: x]]\n"), "TODO-LEGAL"},
		"reviewed with pending section": {"legal/terms/v1.md", []byte("---\n" + valid + "\nreviewed: true\nreviewed_by: A\nreviewed_on: 2026-01-02\n---\n{{inventory:retention}}\n"), "pending"},
		"unknown section":               {"legal/terms/v1.md", []byte("---\n" + valid + "\n---\n{{inventory:nope}}\n"), "unknown inventory section"},
		"too big":                       {"legal/terms/v1.md", []byte("---\n" + valid + "\n---\n" + strings.Repeat("x", 600<<10)), "over"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newLoader(t, nil).Parse(c.path, c.data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestParseDirectives(t *testing.T) {
	data := []byte("---\n" + valid + "\n---\n## Cookies\n{{inventory:cookie_table}}\n\n## Retention\n{{inventory:retention}}\n")

	pending, err := newLoader(t, nil).Parse("legal/terms/v1.md", data)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(pending.Body, legaldocs.PendingInventory), "marked pending until the inventory exists")

	filled, err := newLoader(t, fixedFragments{"cookie_table": "| synthetic |\n"}).Parse("legal/terms/v1.md", data)
	require.NoError(t, err)
	assert.Contains(t, filled.Body, "| synthetic |")
	assert.Equal(t, 1, strings.Count(filled.Body, legaldocs.PendingInventory), "only what the inventory can't fill stays pending")
}

func TestParseTreatsCRLFLikeLF(t *testing.T) {
	crlf := strings.ReplaceAll(string(file(valid)), "\n", "\r\n")
	a, err := newLoader(t, nil).Parse("legal/terms/v1.md", []byte(crlf))
	require.NoError(t, err)
	b, err := newLoader(t, nil).Parse("legal/terms/v1.md", file(valid))
	require.NoError(t, err)
	assert.Equal(t, b.ContentHash(), a.ContentHash())
}

func TestLoadWalksKindDirectoriesAndFailsOnAnyBadFile(t *testing.T) {
	good := fstest.MapFS{
		"legal/terms/v1.md":      {Data: file(valid)},
		"legal/terms/v2.md":      {Data: file("kind: terms", "version: v2", "effective: 2026-03-01", "locale: en", "material: false")},
		"legal/beta_terms/v1.md": {Data: file("kind: beta_terms", "version: v1", "effective: 2026-01-01", "locale: en", "material: true")},
		"legal/terms/notes.txt":  {Data: []byte("ignored")},
	}
	vs, err := newLoader(t, nil).Load(good, "legal")
	require.NoError(t, err)
	assert.Len(t, vs, 3)

	good["legal/terms/v3.md"] = &fstest.MapFile{Data: file("kind: terms", "version: v3", "effective: 2026-03-01", "locale: en")}
	_, err = newLoader(t, nil).Load(good, "legal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legal/terms/v3.md")
}

func TestLoadRefusesTheSameVersionTwice(t *testing.T) {
	fsys := fstest.MapFS{
		"a/terms/v1.md": {Data: file(valid)},
		"b/terms/v1.md": {Data: file(valid)},
	}
	_, err := newLoader(t, nil).Load(fsys, ".")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same kind, version and locale")
}

func TestNewKindsRefusesAnUnsafeName(t *testing.T) {
	for _, bad := range []string{"", "Terms", "a/b", "../x", "a b", "1x"} {
		_, err := legaldocs.NewKinds(bad)
		assert.Error(t, err, fmt.Sprintf("%q", bad))
	}
}

func TestTemplatesAreDraftPlaceholdersForEveryBuiltInKind(t *testing.T) {
	titles := map[string]string{
		legaldocs.PrivacyPolicy: "# Privacy policy", legaldocs.CookiePolicy: "# Cookie policy", legaldocs.Terms: "# Terms of service",
		legaldocs.HealthDataNotice: "# Health data notice", legaldocs.CCPANoticeAtCollection: "# Notice at collection",
	}
	// The generated sections each document is expected to carry.
	directives := map[string][]string{
		legaldocs.PrivacyPolicy: {"sub_processors", "retention"}, legaldocs.CookiePolicy: {"cookie_table"},
		legaldocs.HealthDataNotice: {"sub_processors", "retention"}, legaldocs.CCPANoticeAtCollection: {"retention"},
	}
	for kind, title := range titles {
		t.Run(kind, func(t *testing.T) {
			data, err := legaldocs.Template(kind, "2026-01-01.1", jan1)
			require.NoError(t, err)
			v, err := newLoader(t, nil).Parse("legal/"+kind+"/2026-01-01.1.md", data)
			require.NoError(t, err)
			assert.False(t, v.Reviewed, "every document starts as a draft")
			assert.True(t, v.Material)
			assert.Contains(t, v.Body, "[[TODO-LEGAL:", "with gaps marked")
			assert.Contains(t, v.Body, "\n## ", "and section headings")
			assert.True(t, strings.HasPrefix(v.Body, title+"\n"), "of its own kind")
			assert.Equal(t, len(directives[kind]), strings.Count(v.Body, legaldocs.PendingInventory), "with its generated sections pending")
		})
	}
	_, err := legaldocs.Template("nope", "v1", jan1)
	assert.Error(t, err)
}
