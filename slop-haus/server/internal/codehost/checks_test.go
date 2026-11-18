package codehost_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

func TestNewChecks_OverallState(t *testing.T) {
	ok := codehost.Check{Name: "ok", State: codehost.CheckSuccess}
	bad := codehost.Check{Name: "bad", State: codehost.CheckFailure}
	wait := codehost.Check{Name: "wait", State: codehost.CheckPending}
	skip := codehost.Check{Name: "skip", State: codehost.CheckNeutral}

	for _, tc := range []struct {
		name   string
		checks []codehost.Check
		want   codehost.CheckState
		passed bool
	}{
		{"no checks at all is not a pass", nil, codehost.CheckNone, false},
		{"only neutral checks vouch for nothing", []codehost.Check{skip, skip}, codehost.CheckNone, false},
		{"all success", []codehost.Check{ok, ok}, codehost.CheckSuccess, true},
		{"success with a neutral", []codehost.Check{ok, skip}, codehost.CheckSuccess, true},
		{"one pending holds a pass back", []codehost.Check{ok, wait}, codehost.CheckPending, false},
		{"a failure beats success", []codehost.Check{ok, bad}, codehost.CheckFailure, false},
		{"a failure beats pending", []codehost.Check{wait, bad}, codehost.CheckFailure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codehost.NewChecks("abc", "main", tc.checks)
			assert.Equal(t, tc.want, got.State)
			assert.Equal(t, tc.passed, got.Passed())
			assert.Equal(t, "abc", got.HeadSHA)
			assert.Equal(t, tc.checks, got.Checks)
		})
	}
}

func TestCheckStateStrings(t *testing.T) {
	assert.Equal(t, "none", codehost.CheckNone.String())
	assert.Equal(t, "failure", codehost.CheckFailure.String())
}
