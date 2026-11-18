package attachment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	artifact_store "github.com/lowaa/slop-factory/artifact-store"
)

// Store keeps attachment content. Every method namespaces the key under the
// tenant's own folder, so separation does not depend on the caller.
type Store interface {
	// Put stores size bytes of body; a seekable body is needed for S3.
	Put(ctx context.Context, tenantID uuid.UUID, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, tenantID uuid.UUID, key string) (io.ReadCloser, error)
	// Delete succeeds for an object that is not there, so a retry is safe.
	Delete(ctx context.Context, tenantID uuid.UUID, key string) error
}

// newLocalStore keeps content under dir, for tests (see export_test.go): no
// deployment runs without an object store.
func newLocalStore(dir string) (Store, error) {
	s, err := artifact_store.NewLocalStore(dir)
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	return s, nil
}

// S3Config is an S3-compatible object store (rustfs in the cluster).
type S3Config struct {
	Endpoint string
	Region   string
	Bucket   string
	// CredentialsFile holds the keys as S3_ACCESS_KEY_ID= and
	// S3_SECRET_ACCESS_KEY= lines (a mounted Secret), so they are in no
	// environment or flag listing. The names rustfs's own Secret uses,
	// RUSTFS_ACCESS_KEY and RUSTFS_SECRET_KEY, are accepted too.
	CredentialsFile string
}

// NewS3Store connects to the object store and creates its bucket if needed.
func NewS3Store(ctx context.Context, cfg *S3Config) (Store, error) {
	id, secret, err := readCredentials(cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	client, err := artifact_store.NewClient(ctx, cfg.Endpoint, cfg.Region, id, secret)
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	if err := artifact_store.EnsureBucket(ctx, client, cfg.Bucket); err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	return artifact_store.NewS3Store(client, cfg.Bucket), nil
}

func readCredentials(path string) (id, secret string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("attachment: read the object store credentials: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch {
		case !ok:
		case k == "S3_ACCESS_KEY_ID" || k == "RUSTFS_ACCESS_KEY":
			id = v
		case k == "S3_SECRET_ACCESS_KEY" || k == "RUSTFS_SECRET_KEY":
			secret = v
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("attachment: read the object store credentials: %w", err)
	}
	if id == "" || secret == "" {
		return "", "", fmt.Errorf("attachment: %s needs S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY lines", path)
	}
	return id, secret, nil
}
