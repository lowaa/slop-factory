// Package supervisorpkg serves the supervisor: the Python package in
// slop-haus/supervisor that runs headless workers on a person's machine.
//
// The server is the source of the package's version, as it is for the
// workspace scripts, so a worker machine never runs a supervisor the server
// has not shipped.
package supervisorpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Package is the supervisor as served: a gzipped tar of what running it
// needs, and a version that changes exactly when that does.
type Package struct {
	Archive []byte
	Version string
}

// shippedFiles and shippedTree are what the archive holds, relative to the
// package directory.
// Tests, the test script and caches stay behind: only what `uv run --frozen`
// needs on the worker machine goes over.
var shippedFiles = []string{"pyproject.toml", "uv.lock"}

const shippedTree = "src"

// Load builds the archive from the package directory.
//
// It is deterministic -- sorted entries, no timestamps or owners -- so the
// version is a function of the content and two servers built from the same
// source agree on it.
func Load(dir string) (*Package, error) {
	names := append([]string(nil), shippedFiles...)
	err := filepath.WalkDir(filepath.Join(dir, shippedTree), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".pyc") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the supervisor package at %s: %w", dir, err)
	}
	sort.Strings(names)

	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("reading the supervisor package at %s: %w", dir, err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: path.Clean(name), Mode: 0o644, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(tarball.Bytes())
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	if _, err := gz.Write(tarball.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return &Package{Archive: zipped.Bytes(), Version: hex.EncodeToString(sum[:6])}, nil
}
