package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validCaseJSON() string {
	return `{
		"name": "the-case",
		"base": "abc123",
		"head": "def456",
		"patch": "the.patch",
		"commit_message": "the change",
		"review_dir": ".",
		"bugs": [
			{
				"id": "bug-1",
				"kind": "logic",
				"description": "an off by one",
				"locations": [{"file": "a.go", "start_line": 10, "end_line": 12}]
			}
		]
	}`
}

func writeCaseFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoadCase_Valid(t *testing.T) {
	dir := t.TempDir()
	path := writeCaseFile(t, dir, "case.json", validCaseJSON())

	c, err := LoadCase(path)
	require.NoError(t, err)
	assert.Equal(t, "the-case", c.Name)
	assert.Equal(t, "abc123", c.Base)
	assert.Equal(t, "def456", c.Head)
	assert.Equal(t, "the change", c.CommitMessage)
	require.Len(t, c.Bugs, 1)
	assert.Equal(t, "bug-1", c.Bugs[0].ID)
}

func TestLoadCase_PatchPathIsAbsolute(t *testing.T) {
	dir := t.TempDir()
	writeCaseFile(t, dir, "case.json", validCaseJSON())

	c, err := LoadCase(filepath.Join(dir, "case.json"))
	require.NoError(t, err)
	got := c.PatchPath()
	assert.True(t, filepath.IsAbs(got), "PatchPath() = %q, want absolute", got)
	assert.Equal(t, filepath.Join(dir, "the.patch"), got)
}

func TestLoadCase_PatchPathIsAbsolute_RelativeInput(t *testing.T) {
	dir := t.TempDir()
	writeCaseFile(t, dir, "case.json", validCaseJSON())

	// LoadCase is given a relative path from the current directory, not an
	// absolute one -- PatchPath must still resolve to an absolute path.
	t.Chdir(dir)
	c, err := LoadCase("case.json")
	require.NoError(t, err)
	got := c.PatchPath()
	assert.True(t, filepath.IsAbs(got), "PatchPath() = %q, want absolute", got)

	wantDir, err := filepath.Abs(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(wantDir, "the.patch"), got)
}

func TestLoadCase_PatchPathEmptyWhenNoPatch(t *testing.T) {
	dir := t.TempDir()
	path := writeCaseFile(t, dir, "case.json", `{
		"name": "n", "base": "b", "head": "h",
		"bugs": [{"id": "1", "locations": [{"file": "a.go", "start_line": 1, "end_line": 1}]}]
	}`)

	c, err := LoadCase(path)
	require.NoError(t, err)
	assert.Empty(t, c.PatchPath())
}

func TestLoadCase_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeCaseFile(t, dir, "case.json", `{not valid json`)

	_, err := LoadCase(path)
	require.Error(t, err)
}

func TestLoadCase_MissingFile(t *testing.T) {
	_, err := LoadCase(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
}

func TestLoadCase_Validation(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"name": "n",
			"base": "b",
			"head": "h",
			"bugs": []any{
				map[string]any{
					"id": "bug-1",
					"locations": []any{
						map[string]any{"file": "a.go", "start_line": 1, "end_line": 2},
					},
				},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		wantErr string
	}{
		{
			name:   "missing name",
			mutate: func(m map[string]any) { delete(m, "name") },
		},
		{
			name:   "missing base",
			mutate: func(m map[string]any) { delete(m, "base") },
		},
		{
			name:   "missing head",
			mutate: func(m map[string]any) { delete(m, "head") },
		},
		{
			name:   "no bugs",
			mutate: func(m map[string]any) { m["bugs"] = []any{} },
		},
		{
			name: "duplicate bug id",
			mutate: func(m map[string]any) {
				bug := map[string]any{
					"id":        "bug-1",
					"locations": []any{map[string]any{"file": "b.go", "start_line": 1, "end_line": 1}},
				}
				m["bugs"] = append(m["bugs"].([]any), bug)
			},
		},
		{
			name: "bug with no locations",
			mutate: func(m map[string]any) {
				bugs := m["bugs"].([]any)
				bugs[0].(map[string]any)["locations"] = []any{}
			},
		},
		{
			name: "location start_line <= 0",
			mutate: func(m map[string]any) {
				bugs := m["bugs"].([]any)
				locs := bugs[0].(map[string]any)["locations"].([]any)
				locs[0].(map[string]any)["start_line"] = 0
			},
		},
		{
			name: "location end_line < start_line",
			mutate: func(m map[string]any) {
				bugs := m["bugs"].([]any)
				locs := bugs[0].(map[string]any)["locations"].([]any)
				locs[0].(map[string]any)["start_line"] = 5
				locs[0].(map[string]any)["end_line"] = 4
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := base()
			tt.mutate(m)

			data, err := json.Marshal(m)
			require.NoError(t, err)

			dir := t.TempDir()
			path := writeCaseFile(t, dir, "case.json", string(data))

			_, err = LoadCase(path)
			require.Error(t, err)
		})
	}
}
