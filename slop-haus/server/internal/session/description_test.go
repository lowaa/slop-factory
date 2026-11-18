package session_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

func TestParseDescription_TakesTheFieldsItKnows(t *testing.T) {
	d := session.ParseDescription([]byte(`{
		"harness": "pi", "harness_version": "0.87.0", "provider": "anthropic", "model": "claude-sonnet-4-5",
		"labels": {"gpu": "a100", "docker": "", "cores": 16, "fast": true},
		"something_new": "ignored"
	}`))
	assert.Equal(t, &session.Description{
		Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Labels: map[string]string{"gpu": "a100", "docker": "", "cores": "16", "fast": "true"},
	}, d)
}

// run_remote_control.sh hands the revision over through the environment, so it arrives as
// a string of digits; anything that is not a plain non-negative whole
// number is dropped rather than guessed at.
func TestParseDescription_ConfigRevision(t *testing.T) {
	for raw, want := range map[string]int64{
		`{"config_revision": "12"}`: 12,
		`{"config_revision": 12}`:   12,
		`{"config_revision": "0"}`:  0,
	} {
		d := session.ParseDescription([]byte(raw))
		if assert.NotNil(t, d, raw) && assert.NotNil(t, d.ConfigRevision, raw) {
			assert.Equal(t, want, *d.ConfigRevision, raw)
		}
	}
	for _, raw := range []string{
		`{"config_revision": "-1"}`, `{"config_revision": -1}`, `{"config_revision": "1.5"}`,
		`{"config_revision": 1.5}`, `{"config_revision": "012"}`, `{"config_revision": "+3"}`,
		`{"config_revision": " 3"}`, `{"config_revision": "99999999999999999999"}`, `{"config_revision": true}`,
		`{"config_revision": 9007199254740993}`,
	} {
		assert.Nil(t, session.ParseDescription([]byte(raw)), raw)
	}
}

// Whatever arrives, the answer is a description or nothing -- never an
// error for the hook to fail on.
func TestParseDescription_DropsWhatItCannotUse(t *testing.T) {
	for _, raw := range []string{``, `null`, `"pi"`, `42`, `[1,2]`, `{}`, `{"harness": {"x": 1}}`, `{"labels": "gpu"}`, `not json`} {
		assert.Nil(t, session.ParseDescription([]byte(raw)), raw)
	}
	d := session.ParseDescription([]byte(`{"harness": ["pi"], "model": "m", "labels": {"ok": "1", "nested": {"a": 1}}}`))
	assert.Equal(t, &session.Description{Model: "m", Labels: map[string]string{"ok": "1", "nested": ""}}, d)
}

func TestSanitized_StripsControlAndFormatCharactersAndTrims(t *testing.T) {
	d := (&session.Description{
		Harness: "  pi\n",
		Model:   "claude\x1b]0;pwned\x07-sonnet‮",
		Labels:  map[string]string{" gpu\t": " a100 ", "\x00": "dropped: key is empty once cleaned"},
	}).Sanitized()
	assert.Equal(t, "pi", d.Harness)
	assert.Equal(t, "claude]0;pwned-sonnet", d.Model)
	assert.Equal(t, map[string]string{"gpu": "a100"}, d.Labels)
}

func TestSanitized_CapsLengthsInRunesAndTheLabelCount(t *testing.T) {
	labels := map[string]string{}
	for i := range session.MaxLabels + 5 {
		labels[fmt.Sprintf("k%02d", i)] = strings.Repeat("v", session.MaxLabelValue+1)
	}
	labels[strings.Repeat("a", session.MaxLabelKey+1)] = "long key"
	d := (&session.Description{Model: strings.Repeat("é", session.MaxDescriptionField+1), Labels: labels}).Sanitized()

	assert.Equal(t, strings.Repeat("é", session.MaxDescriptionField), d.Model)
	assert.Len(t, d.Labels, session.MaxLabels)
	// The alphabetically first keys survive, so which ones is stable.
	assert.Contains(t, d.Labels, strings.Repeat("a", session.MaxLabelKey))
	assert.Contains(t, d.Labels, "k00")
	assert.NotContains(t, d.Labels, fmt.Sprintf("k%02d", session.MaxLabels))
	assert.Len(t, d.Labels["k00"], session.MaxLabelValue)
}

func TestParseDescription_BundleHash(t *testing.T) {
	d := session.ParseDescription([]byte(`{"bundle_hash": "c1ff58edfd3c9bb6"}`))
	require.NotNil(t, d, "a bundle hash alone is a description")
	assert.Equal(t, "c1ff58edfd3c9bb6", d.BundleHash)

	d = session.ParseDescription([]byte(`{"harness": "pi", "bundle_hash": "ab\u001b[31mcd"}`))
	require.NotNil(t, d)
	assert.Equal(t, "ab[31mcd", d.BundleHash, "control characters are stripped")

	d = session.ParseDescription([]byte(`{"harness": "pi", "bundle_hash": {"x": 1}}`))
	require.NotNil(t, d)
	assert.Empty(t, d.BundleHash, "nested values are dropped")
}
