package artifact_store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// LocalStore is the ArtifactStore implementation backed by the local
// filesystem, rooted at baseDir -- for local dev/tests that don't want to
// stand up an S3-compatible server just to read/write artifacts.
type LocalStore struct {
	baseDir string
}

var (
	_ ArtifactStore = (*LocalStore)(nil)
	_ Deleter       = (*LocalStore)(nil)
	_ Lister        = (*LocalStore)(nil)
)

// NewLocalStore roots a LocalStore at baseDir, creating it (and any missing
// parents) if it doesn't already exist.
func NewLocalStore(baseDir string) (*LocalStore, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("artifact_store: create base dir %s: %w", baseDir, err)
	}
	return &LocalStore{baseDir: baseDir}, nil
}

// objectPath namespaces key under tenantID's own subdirectory -- mirrors
// S3Store's objectKey. filepath.FromSlash normalizes key (which callers may
// build with "/" regardless of host OS, the way object storage keys always
// are) to this OS's separator before joining.
func (l *LocalStore) objectPath(tenantID uuid.UUID, key string) string {
	return filepath.Join(l.baseDir, tenantID.String(), filepath.FromSlash(key))
}

func (l *LocalStore) Put(ctx context.Context, tenantID uuid.UUID, key string, body io.Reader, size int64, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dest := l.objectPath(tenantID, key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("artifact_store: create dir for %s/%s: %w", tenantID, key, err)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("artifact_store: create %s/%s: %w", tenantID, key, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, body); err != nil {
		return fmt.Errorf("artifact_store: write %s/%s: %w", tenantID, key, err)
	}
	return nil
}

func (l *LocalStore) Get(ctx context.Context, tenantID uuid.UUID, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.Open(l.objectPath(tenantID, key))
	if err != nil {
		return nil, fmt.Errorf("artifact_store: get %s/%s: %w", tenantID, key, err)
	}
	return f, nil
}

func (l *LocalStore) Delete(ctx context.Context, tenantID uuid.UUID, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(l.objectPath(tenantID, key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("artifact_store: delete %s/%s: %w", tenantID, key, err)
	}
	return nil
}

func (l *LocalStore) List(ctx context.Context, fn func(Object) error) error {
	err := filepath.WalkDir(l.baseDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(l.baseDir, p)
		if err != nil {
			return err
		}
		obj := splitObjectPath(filepath.ToSlash(rel))
		obj.LastModified = info.ModTime()
		return fn(obj)
	})
	if err != nil {
		return fmt.Errorf("artifact_store: list %s: %w", l.baseDir, err)
	}
	return nil
}
