package githubapp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// declaredOperations reads the Operation constants out of codehost's source,
// so an operation added there is seen here without anyone remembering to list it.
func declaredOperations(t *testing.T) []codehost.Operation {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../credential.go", nil, 0)
	require.NoError(t, err)
	var ops []codehost.Operation
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Values) != 1 {
			return true
		}
		if ident, ok := spec.Type.(*ast.Ident); !ok || ident.Name != "Operation" {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		require.True(t, ok)
		val, err := strconv.Unquote(lit.Value)
		require.NoError(t, err)
		ops = append(ops, codehost.Operation(val))
		return true
	})
	return ops
}

func TestManifest_RequestsExactlyTheUnionOfWhatEveryOperationNeeds(t *testing.T) {
	ops := append(declaredOperations(t), operationVerify)
	require.GreaterOrEqual(t, len(ops), 6)

	want := Permissions{}
	for _, op := range ops {
		perms, err := permissionsFor(op)
		require.NoError(t, err, string(op))
		for name, level := range perms {
			if want[name] != "write" {
				want[name] = level
			}
		}
	}

	board, err := url.Parse("https://board.test")
	require.NoError(t, err)
	manifest, err := buildManifest(board, "")
	require.NoError(t, err)
	assert.Equal(t, want, manifest.DefaultPermissions)
	assert.ElementsMatch(t, ops, allOperations)
}

func TestManifest_WithoutAHookURLHasNoWebhookAndIsPrivateAndPointsAtTheBoard(t *testing.T) {
	board, err := url.Parse("https://board.test")
	require.NoError(t, err)
	m, err := buildManifest(board, "")
	require.NoError(t, err)
	assert.False(t, m.HookAttributes.Active)
	assert.False(t, m.Public)
	assert.Empty(t, m.DefaultEvents)
	assert.Equal(t, "https://board.test/workspace/github-app/created", m.RedirectURL)
	assert.Equal(t, "https://board.test/workspace/github-app/installed", m.SetupURL)
	assert.LessOrEqual(t, len(m.Name), 34)
}

func TestManifest_WithAHookURLDeliversTheReviewEventsThere(t *testing.T) {
	board, err := url.Parse("https://board.test")
	require.NoError(t, err)
	m, err := buildManifest(board, "https://hooks.test/hooks/github-app")
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.test/hooks/github-app", m.HookAttributes.URL)
	assert.True(t, m.HookAttributes.Active)
	assert.Equal(t, []string{"pull_request", "pull_request_review"}, m.DefaultEvents)
	assert.False(t, m.Public)
	// The events slice is the manifest's own: editing it must not edit the package's.
	m.DefaultEvents[0] = "x"
	assert.Equal(t, "pull_request", AppWebhookEvents[0])
}
