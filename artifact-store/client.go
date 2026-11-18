package artifact_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// NewClient builds an S3 client pointed at an S3-compatible endpoint
// (rustfs in dev/prod) rather than real AWS. BaseEndpoint + UsePathStyle are
// what make that redirection work -- rustfs, like most self-hosted S3
// alternatives, doesn't support virtual-hosted-style addressing
// (bucket.host/key), only path-style (host/bucket/key).
func NewClient(ctx context.Context, endpoint, region, accessKeyID, secretAccessKey string) (*s3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("artifact_store: load aws config: %w", err)
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	}), nil
}

// EnsureBucket creates bucket if it doesn't already exist. Idempotent (an
// "already owned by you"/"already exists" response is treated as success),
// so it's safe to call on every server startup -- mirrors
// migrations.EnsureRole's same-shaped idempotent provisioning.
func EnsureBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return nil
	}

	var alreadyOwned *types.BucketAlreadyOwnedByYou
	var alreadyExists *types.BucketAlreadyExists
	if errors.As(err, &alreadyOwned) || errors.As(err, &alreadyExists) {
		return nil
	}
	return fmt.Errorf("artifact_store: ensure bucket %s: %w", bucket, err)
}
