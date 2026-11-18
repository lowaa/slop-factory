// Package artifact_store reads/writes artifact content (images/documents),
// namespaced by tenant, behind a storage-agnostic interface, so callers do
// not call the storage library inline. S3Store backs it with an
// S3-compatible object store; LocalStore backs it with the local
// filesystem, for local dev/tests that don't want to stand up an
// S3-compatible server.
//
// It is its own module so that more than one service can fetch the same
// artifacts; consumers pull it in via go.work rather than through a
// published version.
package artifact_store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ArtifactStore reads/writes artifact content (images/documents uploaded by
// users), keyed by tenant plus an arbitrary key the caller controls --
// internal/artifact composes key from the artifact's own id and filename.
//
// Every method namespaces key under tenantID's own folder within the
// application bucket, so tenant isolation is guaranteed by the store itself
// rather than depending on every caller to build an already-prefixed key.
type ArtifactStore interface {
	// Put uploads size bytes read from body under tenantID/key.
	Put(ctx context.Context, tenantID uuid.UUID, key string, body io.Reader, size int64, contentType string) error

	// Get returns a reader for the content previously stored at
	// tenantID/key. The caller must Close it.
	Get(ctx context.Context, tenantID uuid.UUID, key string) (io.ReadCloser, error)
}

// Deleter removes artifact content. Kept apart from ArtifactStore so the
// many readers/writers of artifacts (and their test doubles) don't have to
// grow a delete they never call.
type Deleter interface {
	// Delete removes the content stored at tenantID/key. Deleting a key
	// that holds nothing succeeds, so a retried delete is safe.
	Delete(ctx context.Context, tenantID uuid.UUID, key string) error
}

// Object is one stored artifact as a listing sees it.
type Object struct {
	// TenantID is the tenant whose folder holds the object, or uuid.Nil for
	// an object outside any tenant's folder, which nothing in this package
	// wrote; Key is then its whole path in the store.
	TenantID     uuid.UUID
	Key          string
	LastModified time.Time
}

// Lister walks every stored artifact across all tenants -- for operator
// tooling such as finding orphaned objects, never for a request path.
type Lister interface {
	// List calls fn for each stored object, in no particular order, and
	// stops at the first error fn returns.
	List(ctx context.Context, fn func(Object) error) error
}

// ErrInvalidKey is returned for a key that isn't a clean relative path:
// joined under a tenant's folder, a ".." segment would reach another
// tenant's objects.
var ErrInvalidKey = errors.New("artifact_store: invalid key")

func checkKey(key string) error {
	if key == "" || path.IsAbs(key) || path.Clean(key) != key || key == ".." || strings.HasPrefix(key, "../") {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return nil
}

// splitObjectPath reverses the tenant namespacing of a store path
// ("<tenant>/<key>", always "/"-separated).
func splitObjectPath(p string) Object {
	tenant, key, ok := strings.Cut(p, "/")
	if !ok || key == "" {
		return Object{Key: p}
	}
	tenantID, err := uuid.Parse(tenant)
	if err != nil || tenantID.String() != tenant || checkKey(key) != nil {
		return Object{Key: p}
	}
	return Object{TenantID: tenantID, Key: key}
}
