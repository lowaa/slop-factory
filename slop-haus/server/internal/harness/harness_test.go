package harness_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/contracts"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
)

func TestEmbeddedCopyEqualsContract(t *testing.T) {
	embedded, err := os.ReadFile("harnesses.json")
	require.NoError(t, err)
	contract, err := os.ReadFile(filepath.Join(contracts.Dir(), "harnesses.json"))
	require.NoError(t, err)

	var a, b harness.File
	require.NoError(t, json.Unmarshal(embedded, &a))
	require.NoError(t, json.Unmarshal(contract, &b))
	assert.Equal(t, b, a, "copy server/internal/harness/harnesses.json from contracts/harnesses.json")
	assert.Equal(t, b.Harnesses, harness.All(), "the registry loads the embedded copy")
}

func TestLookup(t *testing.T) {
	assert.Equal(t, []string{"claude", "pi", "codex"}, harness.Names())
	assert.True(t, harness.Valid("claude"))
	assert.False(t, harness.Valid("emacs"))
	assert.False(t, harness.Valid(""))
	h, ok := harness.Lookup("pi")
	require.True(t, ok)
	s, ok := h.Setting("thinking")
	require.True(t, ok)
	assert.Contains(t, s.Options, "xhigh")
}

func TestNameSchemaIsTheRegistryEnum(t *testing.T) {
	r := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	ref := harness.Name("").Schema(r)
	assert.Equal(t, "#/components/schemas/Harness", ref.Ref)
	var got []string
	for _, v := range r.Map()["Harness"].Enum {
		got = append(got, v.(string))
	}
	assert.Equal(t, harness.Names(), got)
}
