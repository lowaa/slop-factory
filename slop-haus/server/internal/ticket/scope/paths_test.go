package scope_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func TestNormalizePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain file", in: "server/main.go", want: "server/main.go"},
		{name: "directory keeps its trailing slash", in: "server/internal/", want: "server/internal/"},
		{name: "leading ./ is stripped", in: "./server/main.go", want: "server/main.go"},
		{name: "leading / is stripped", in: "/server/main.go", want: "server/main.go"},
		{name: "duplicate slashes collapse", in: "server//internal///", want: "server/internal/"},
		{name: "interior .. resolves", in: "server/internal/../cmd/", want: "server/cmd/"},
		{name: "backslashes normalize", in: `server\internal\`, want: "server/internal/"},
		{name: "surrounding space is trimmed", in: "  server/main.go  ", want: "server/main.go"},
		{name: "directory dot-slash", in: "./", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "escapes the repo root", in: "../secrets", wantErr: true},
		{name: "resolves to outside the repo root", in: "server/../../secrets", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scope.NormalizePath(tc.in)
			if tc.wantErr {
				require.ErrorIs(t, err, scope.ErrInvalidPath)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The cases a plain string-prefix test gets wrong are the point of this
// function, so they are named individually rather than folded into a
// generic "overlaps/doesn't overlap" pair.
func TestConflicts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "identical files", a: "server/main.go", b: "server/main.go", want: true},
		{name: "identical directories", a: "server/", b: "server/", want: true},
		{name: "directory contains a file", a: "server/internal/", b: "server/internal/core.go", want: true},
		{name: "directory contains a subdirectory", a: "server/", b: "server/internal/", want: true},
		{name: "argument order does not matter", a: "server/internal/core.go", b: "server/internal/", want: true},

		{name: "sibling directories", a: "server/internal/", b: "server/cmd/", want: false},
		{name: "sibling files", a: "server/main.go", b: "server/other.go", want: false},

		// Plain prefixing says true for all three of these.
		{name: "directory prefix is not containment", a: "server/core/", b: "server/coreutil/", want: false},
		{name: "file prefix is not containment", a: "server/main.go", b: "server/main.go.bak", want: false},
		{name: "a file contains nothing", a: "server/main.go", b: "server/main.go/inner.go", want: false},

		// These two pass trivially in Go -- HasPrefix has no wildcards --
		// and that is the point: they pin the comparison to Go string
		// semantics. Were this ever reimplemented as a SQL LIKE pattern,
		// "_" and "%" become wildcards and both cases start reporting a
		// false conflict, with snake_case filenames making the first one
		// pervasive. Deleting these is the signal that the rule has moved
		// somewhere it shouldn't be.
		{name: "underscore is not a wildcard", a: "server/test_file.go", b: "server/testXfile.go", want: false},
		{name: "percent is not a wildcard", a: "server/a%b.go", b: "server/aQQb.go", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, scope.Conflicts(tc.a, tc.b))
			// Conflicts must be symmetric -- a claim path that depended on
			// argument order would grant or refuse work based on row order.
			assert.Equal(t, tc.want, scope.Conflicts(tc.b, tc.a), "not symmetric")
		})
	}
}

func TestConflictsAny_NamesTheConflictingScope(t *testing.T) {
	t.Parallel()
	others := []string{"server/cmd/", "server/internal/core/", "docs/"}

	hit, which := scope.ConflictsAny("server/internal/core/lease.go", others)
	assert.True(t, hit)
	assert.Equal(t, "server/internal/core/", which, "should name what actually blocked it")

	hit, which = scope.ConflictsAny("frontend/src/app.tsx", others)
	assert.False(t, hit)
	assert.Empty(t, which)
}
