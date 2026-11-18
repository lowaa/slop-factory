package llmusage_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

func TestValidCommand(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"git", true},
		{"git status", true},
		{"go test", true},
		{"deploy.sh", true},
		{"ansible-playbook", true},
		{"a.b_c-d", true},
		{strings.Repeat("a", 32), true},
		{"", false},
		{"Git", false},
		{"git  status", false},
		{" git", false},
		{"git status x", false},
		{"git/status", false},
		{strings.Repeat("a", 33), false},
		{"git ", false},
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			assert.Equal(t, c.want, llmusage.ValidCommand(c.command))
		})
	}
}

func TestCommandLabel(t *testing.T) {
	cases := []struct{ command, want string }{
		{"", ""},
		{"ls", "ls"},
		{"git status", "git status"},
		{"git frobnicate", "git"},
		{"make test", "make"},
		{"rogue-binary", "other"},
		{"rogue-binary sub", "other"},
		{"other", "other"},
		{"kubectl get", "kubectl get"},
	}
	for _, c := range cases {
		t.Run(c.command, func(t *testing.T) {
			assert.Equal(t, c.want, llmusage.CommandLabel(c.command))
		})
	}
}

func TestCommandLabel_IsBounded(t *testing.T) {
	inputs := []string{"ls", "git status", "git zzz", "go test", "rogue", "rogue x", "make all", "uv run", "other", "a.b_c-d", "deploy.sh build"}
	for _, in := range inputs {
		label := llmusage.CommandLabel(in)
		assert.True(t, label == "" || llmusage.ValidCommand(label), "label %q for %q", label, in)
	}
	for program := range llmusage.SubcommandPrograms {
		label := llmusage.CommandLabel(program + " not-a-listed-subcommand")
		assert.Equal(t, program, label)
	}
}

type bashFixture struct {
	Description        string                                `json:"description"`
	Rules              []string                              `json:"rules"`
	SubcommandPrograms map[string]llmusage.SubcommandProgram `json:"subcommand_programs"`
	Cases              []bashFixtureCase                     `json:"cases"`
}

type bashFixtureCase struct {
	Name        string   `json:"name"`
	CommandLine string   `json:"command_line"`
	Expected    []string `json:"expected"`
}

func loadBashFixture(t *testing.T) *bashFixture {
	t.Helper()
	data, err := os.ReadFile("fixtures/bash_commands.json")
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f bashFixture
	require.NoError(t, dec.Decode(&f))
	return &f
}

func TestBashCommandsFixture(t *testing.T) {
	f := loadBashFixture(t)

	t.Run("subcommand programs match the allowlist", func(t *testing.T) {
		require.Equal(t, llmusage.SubcommandPrograms, f.SubcommandPrograms)
	})

	require.NotEmpty(t, f.Cases)

	seen := map[string]bool{}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			require.NotEmpty(t, c.Name)
			assert.False(t, seen[c.Name], "duplicate case name")
			seen[c.Name] = true

			assert.NotEmpty(t, c.CommandLine)
			require.NotEmpty(t, c.Expected)
			for _, e := range c.Expected {
				assert.True(t, llmusage.ValidCommand(e), "expected %q", e)
				if program, _, two := strings.Cut(e, " "); two {
					assert.Contains(t, llmusage.SubcommandPrograms, program, "only subcommand programs get a subcommand: %q", e)
				}
			}
		})
	}

	t.Run("covers the tricky shapes", func(t *testing.T) {
		covers := func(match func(string) bool) bool {
			for _, c := range f.Cases {
				if match(c.CommandLine) {
					return true
				}
			}
			return false
		}
		contains := func(sub string) func(string) bool {
			return func(s string) bool { return strings.Contains(s, sub) }
		}
		shapes := map[string]func(string) bool{
			"env assignment": contains("="),
			"sudo":           contains("sudo "),
			"timeout":        contains("timeout "),
			"env wrapper":    contains("env "),
			"absolute path":  func(s string) bool { return strings.HasPrefix(s, "/") || strings.Contains(s, " /") },
			"&&":             contains("&&"),
			";":              contains(";"),
			"|":              contains("|"),
		}
		for name, match := range shapes {
			assert.True(t, covers(match), "no fixture case covers %s", name)
		}
	})
}
