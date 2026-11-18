package artifact_store

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// s3API is the subset of *s3.Client this package actually calls, narrowed
// so tests can substitute a mock instead of needing a real S3-compatible
// server (see s3_test.go) -- *s3.Client satisfies it structurally, with no
// explicit assertion needed.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	s3.ListObjectsV2APIClient
}

// S3Store is the ArtifactStore implementation backed by an S3-compatible
// object store.
type S3Store struct {
	client s3API
	bucket string
}

var (
	_ ArtifactStore = (*S3Store)(nil)
	_ Deleter       = (*S3Store)(nil)
	_ Lister        = (*S3Store)(nil)
)

// NewS3Store wraps an already-configured *s3.Client (see NewClient) for
// reading/writing artifacts in bucket.
func NewS3Store(client *s3.Client, bucket string) *S3Store {
	return newS3Store(client, bucket)
}

// newS3Store is the same constructor against the narrower s3API, so tests
// can pass a mock without going through a real *s3.Client.
func newS3Store(client s3API, bucket string) *S3Store {
	return &S3Store{client: client, bucket: bucket}
}

// objectKey namespaces key under tenantID's own folder -- see
// ArtifactStore's doc comment for why this lives in the store rather than
// being left to callers. path.Join (not filepath.Join) since S3 keys always
// use "/", regardless of the host OS this ever runs on.
func objectKey(tenantID uuid.UUID, key string) string {
	return path.Join(tenantID.String(), key)
}

func (s *S3Store) Put(ctx context.Context, tenantID uuid.UUID, key string, body io.Reader, size int64, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey(tenantID, key)),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("artifact_store: put %s/%s: %w", tenantID, key, err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, tenantID uuid.UUID, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey(tenantID, key)),
	})
	if err != nil {
		return nil, fmt.Errorf("artifact_store: get %s/%s: %w", tenantID, key, err)
	}
	return out.Body, nil
}

// Delete relies on S3's DeleteObject already succeeding for a missing key.
func (s *S3Store) Delete(ctx context.Context, tenantID uuid.UUID, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey(tenantID, key)),
	})
	if err != nil {
		return fmt.Errorf("artifact_store: delete %s/%s: %w", tenantID, key, err)
	}
	return nil
}

func (s *S3Store) List(ctx context.Context, fn func(Object) error) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("artifact_store: list %s: %w", s.bucket, err)
		}
		for _, o := range page.Contents {
			obj := splitObjectPath(aws.ToString(o.Key))
			obj.LastModified = aws.ToTime(o.LastModified)
			if err := fn(obj); err != nil {
				return err
			}
		}
	}
	return nil
}
