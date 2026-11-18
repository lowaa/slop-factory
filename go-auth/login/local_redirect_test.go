package login_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login"
)

// localRedirectCase is one row of testdata/local_redirect_cases.json, which
// js-components/router's safe_redirect_path.test.ts reads as well. A nil
// result is a refusal.
type localRedirectCase struct {
	Name  string  `json:"name"`
	Input string  `json:"input"`
	Go    *string `json:"go"`
	TS    *string `json:"ts"`
	Note  string  `json:"note"`
}

func loadLocalRedirectCases(t *testing.T) []*localRedirectCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/local_redirect_cases.json")
	require.NoError(t, err)
	var file struct {
		Cases []*localRedirectCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Cases)
	return file.Cases
}

func TestLocalRedirect_Table(t *testing.T) {
	for _, tc := range loadLocalRedirectCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			want := "/"
			if tc.Go != nil {
				want = *tc.Go
			}
			assert.Equal(t, want, login.LocalRedirect(tc.Input))
		})
	}
}

// The file itself: the contract LocalRedirect keeps, and a reason wherever
// the two implementations part ways.
func TestLocalRedirectCases_WellFormed(t *testing.T) {
	names := map[string]bool{}
	for _, tc := range loadLocalRedirectCases(t) {
		assert.NotEmpty(t, tc.Name)
		assert.False(t, names[tc.Name], "duplicate case name %q", tc.Name)
		names[tc.Name] = true
		if tc.Go != nil {
			assert.Equal(t, tc.Input, *tc.Go, "%s: LocalRedirect returns an accepted input unchanged", tc.Name)
		}
		same := (tc.Go == nil) == (tc.TS == nil) && (tc.Go == nil || *tc.Go == *tc.TS)
		if !same {
			assert.NotEmpty(t, tc.Note, "%s: the go and ts columns differ, so the case needs a note saying why", tc.Name)
		}
	}
}
