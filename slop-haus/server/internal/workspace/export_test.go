package workspace

import "strings"

// Paths lists every file Files writes, for tests that must cover each of
// them -- a hand-kept list falls behind when a file is added, and a file
// missing from it is one the scripts could carry wrong unnoticed.
func Paths() []string {
	files, err := Files(Params{})
	if err != nil {
		panic(err)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths
}

const StopHookTimeout = stopHookTimeout

// ScriptFor renders the script that writes files, whatever their bodies, for
// tests of how the script carries a body.
func ScriptFor(files []File) string {
	var b strings.Builder
	writeFiles(&b, Params{}, files, setupWrites)
	return b.String()
}

// ScriptFiles is the files a setup or update script for p writes.
func ScriptFiles(p Params) ([]File, error) { return scriptFiles(p) }
