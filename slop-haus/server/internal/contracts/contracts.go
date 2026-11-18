// Package contracts loads slop-haus/contracts/*.json, the vocabulary Go, Python,
// TypeScript and bash share. It reads the files from the source tree, so it is
// for tests, which assert each side's own constants equal the manifest.
package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

type Tools struct {
	Prefix string `json:"prefix"`
	Roles  struct {
		Console          []string `json:"console"`
		Steward          []string `json:"steward"`
		DeniedToHeadless []string `json:"denied_to_headless"`
		DeniedToSteward  []string `json:"denied_to_steward"`
	} `json:"roles"`
	DeniedBuiltinTools []string `json:"denied_builtin_tools"`
}

type Phases struct {
	SlotReasons []string `json:"slot_reasons"`
	SlotPhases  []string `json:"slot_phases"`
}

type Paths struct {
	WorkspacePaths  map[string]string `json:"workspace_paths"`
	PythonOnlyPaths map[string]string `json:"python_only_paths"`
}

type Timeout struct {
	Value int `json:"value"`
}

type Timeouts struct {
	ToolCallMS          Timeout `json:"tool_call_ms"`
	TokenAndStopScriptS Timeout `json:"token_and_stop_script_s"`
}

type EnvVar struct {
	Setter      string   `json:"setter"`
	Readers     []string `json:"readers"`
	Meaning     string   `json:"meaning"`
	FalsyValues []string `json:"falsy_values"`
}

type Env struct {
	Env map[string]EnvVar `json:"env"`
}

// Dir is the slop-haus/contracts directory.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts")
}

// Load reads one manifest into out.
func Load(t testing.TB, name string, out any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), name))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, out), name)
}

func LoadTools(t testing.TB) *Tools {
	t.Helper()
	var v Tools
	Load(t, "tools.json", &v)
	return &v
}

func LoadPhases(t testing.TB) *Phases {
	t.Helper()
	var v Phases
	Load(t, "phases.json", &v)
	return &v
}

func LoadPaths(t testing.TB) *Paths {
	t.Helper()
	var v Paths
	Load(t, "paths.json", &v)
	return &v
}

func LoadTimeouts(t testing.TB) *Timeouts {
	t.Helper()
	var v Timeouts
	Load(t, "timeouts.json", &v)
	return &v
}

func LoadEnv(t testing.TB) *Env {
	t.Helper()
	var v Env
	Load(t, "env.json", &v)
	return &v
}
