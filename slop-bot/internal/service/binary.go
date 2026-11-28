package service

import (
	"os"
	"os/exec"
	"path/filepath"
)

// BinaryFinder works out which slop-bot binary the service should run.
type BinaryFinder struct {
	// Executable is os.Executable in production.
	Executable func() (string, error)
	// LookPath is exec.LookPath in production.
	LookPath func(string) (string, error)
	// SameFile reports whether two paths are the same file.
	SameFile func(a, b string) bool
}

// NewBinaryFinder is the real one.
func NewBinaryFinder() *BinaryFinder {
	return &BinaryFinder{
		Executable: os.Executable,
		LookPath:   exec.LookPath,
		SameFile: func(a, b string) bool {
			ai, aerr := os.Stat(a)
			bi, berr := os.Stat(b)
			return aerr == nil && berr == nil && os.SameFile(ai, bi)
		},
	}
}

// Find returns the running binary, preferring the name PATH gives it when
// that is the same file. os.Executable resolves symlinks on Linux, and a
// package manager's symlink (Homebrew's bin/slop-bot into a versioned
// Cellar directory) is what keeps the unit working across upgrades.
func (f *BinaryFinder) Find() (string, error) {
	exe, err := f.Executable()
	if err != nil {
		return "", err
	}
	if onPath, err := f.LookPath("slop-bot"); err == nil && filepath.IsAbs(onPath) && f.SameFile(onPath, exe) {
		return onPath, nil
	}
	return exe, nil
}
