package artifact_store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockS3API is a testify mock for the s3API subset S3Store calls, so these
// tests exercise the store's own logic (key namespacing, error wrapping)
// without needing a real S3-compatible server -- that is covered separately
// by e2e tests against a real S3-compatible server.
type mockS3API struct {
	mock.Mock
}

func (m *mockS3API) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*s3.PutObjectOutput)
	return out, args.Error(1)
}

func (m *mockS3API) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*s3.GetObjectOutput)
	return out, args.Error(1)
}

func (m *mockS3API) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*s3.DeleteObjectOutput)
	return out, args.Error(1)
}

func (m *mockS3API) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	args := m.Called(ctx, params)
	out, _ := args.Get(0).(*s3.ListObjectsV2Output)
	return out, args.Error(1)
}

func TestPut_NamespacesKeyByTenantAndSetsMetadata(t *testing.T) {
	api := &mockS3API{}
	tenantID := uuid.New()
	api.On("PutObject", mock.Anything, mock.MatchedBy(func(in *s3.PutObjectInput) bool {
		return *in.Bucket == "artifacts" &&
			*in.Key == tenantID.String()+"/abc/file.png" &&
			*in.ContentType == "image/png" &&
			*in.ContentLength == int64(5)
	})).Return(&s3.PutObjectOutput{}, nil)

	store := newS3Store(api, "artifacts")
	err := store.Put(context.Background(), tenantID, "abc/file.png", bytes.NewReader([]byte("hello")), 5, "image/png")

	require.NoError(t, err)
	api.AssertExpectations(t)
}

func TestPut_WrapsUnderlyingError(t *testing.T) {
	api := &mockS3API{}
	api.On("PutObject", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))

	store := newS3Store(api, "artifacts")
	err := store.Put(context.Background(), uuid.New(), "k", bytes.NewReader(nil), 0, "text/plain")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_store: put")
	assert.Contains(t, err.Error(), "boom")
}

func TestGet_NamespacesKeyByTenantAndReturnsBody(t *testing.T) {
	api := &mockS3API{}
	tenantID := uuid.New()
	body := io.NopCloser(bytes.NewReader([]byte("content")))
	api.On("GetObject", mock.Anything, mock.MatchedBy(func(in *s3.GetObjectInput) bool {
		return *in.Bucket == "artifacts" && *in.Key == tenantID.String()+"/abc/file.png"
	})).Return(&s3.GetObjectOutput{Body: body}, nil)

	store := newS3Store(api, "artifacts")
	got, err := store.Get(context.Background(), tenantID, "abc/file.png")
	require.NoError(t, err)

	data, err := io.ReadAll(got)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
}

func TestGet_WrapsUnderlyingError(t *testing.T) {
	api := &mockS3API{}
	api.On("GetObject", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))

	store := newS3Store(api, "artifacts")
	_, err := store.Get(context.Background(), uuid.New(), "k")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_store: get")
	assert.Contains(t, err.Error(), "boom")
}

func TestObjectKey_DifferentTenantsNamespacedSeparately(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	assert.NotEqual(t, objectKey(tenantA, "same/key"), objectKey(tenantB, "same/key"))
	assert.Equal(t, tenantA.String()+"/same/key", objectKey(tenantA, "same/key"))
}

func TestDelete_NamespacesKeyByTenantAndBucket(t *testing.T) {
	api := &mockS3API{}
	tenantID := uuid.New()
	api.On("DeleteObject", mock.Anything, mock.MatchedBy(func(in *s3.DeleteObjectInput) bool {
		return *in.Bucket == "artifacts" && *in.Key == tenantID.String()+"/abc/file.png"
	})).Return(&s3.DeleteObjectOutput{}, nil)

	store := newS3Store(api, "artifacts")
	require.NoError(t, store.Delete(context.Background(), tenantID, "abc/file.png"))

	api.AssertExpectations(t)
}

func TestDelete_WrapsUnderlyingError(t *testing.T) {
	api := &mockS3API{}
	api.On("DeleteObject", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))

	store := newS3Store(api, "artifacts")
	err := store.Delete(context.Background(), uuid.New(), "k")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_store: delete")
	assert.Contains(t, err.Error(), "boom")
}

func TestList_WalksPagesAndParsesObjects(t *testing.T) {
	api := &mockS3API{}
	tenantA, tenantB := uuid.New(), uuid.New()
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	token := "page-2"

	api.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return *in.Bucket == "artifacts" && in.ContinuationToken == nil
	})).Return(&s3.ListObjectsV2Output{
		IsTruncated:           aws.Bool(true),
		NextContinuationToken: aws.String(token),
		Contents:              []s3types.Object{{Key: aws.String(tenantA.String() + "/abc/file.png"), LastModified: &t1}},
	}, nil).Once()
	api.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return in.ContinuationToken != nil && *in.ContinuationToken == token
	})).Return(&s3.ListObjectsV2Output{
		Contents: []s3types.Object{{Key: aws.String(tenantB.String() + "/k"), LastModified: &t2}},
	}, nil).Once()

	var got []Object
	store := newS3Store(api, "artifacts")
	err := store.List(context.Background(), func(o Object) error {
		got = append(got, o)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []Object{
		{TenantID: tenantA, Key: "abc/file.png", LastModified: t1},
		{TenantID: tenantB, Key: "k", LastModified: t2},
	}, got)
	api.AssertExpectations(t)
}

func TestList_ReportsKeysOutsideTenantFoldersWithNilTenant(t *testing.T) {
	api := &mockS3API{}
	api.On("ListObjectsV2", mock.Anything, mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []s3types.Object{
			{Key: aws.String("stray.txt")},
			{Key: aws.String("not-a-uuid/x")},
		},
	}, nil)

	var got []Object
	store := newS3Store(api, "artifacts")
	require.NoError(t, store.List(context.Background(), func(o Object) error {
		got = append(got, o)
		return nil
	}))

	require.Len(t, got, 2)
	assert.Equal(t, uuid.Nil, got[0].TenantID)
	assert.Equal(t, "stray.txt", got[0].Key)
	assert.Equal(t, uuid.Nil, got[1].TenantID)
	assert.Equal(t, "not-a-uuid/x", got[1].Key)
}

func TestList_StopsAndReturnsCallbackError(t *testing.T) {
	api := &mockS3API{}
	api.On("ListObjectsV2", mock.Anything, mock.Anything).Return(&s3.ListObjectsV2Output{
		IsTruncated:           aws.Bool(true),
		NextContinuationToken: aws.String("more"),
		Contents:              []s3types.Object{{Key: aws.String("a")}, {Key: aws.String("b")}},
	}, nil).Once()

	stop := errors.New("stop")
	calls := 0
	store := newS3Store(api, "artifacts")
	err := store.List(context.Background(), func(Object) error {
		calls++
		return stop
	})

	require.ErrorIs(t, err, stop)
	assert.Equal(t, 1, calls)
	// Once() above: a second page request would panic the mock.
	api.AssertNumberOfCalls(t, "ListObjectsV2", 1)
}

func TestList_WrapsListError(t *testing.T) {
	api := &mockS3API{}
	api.On("ListObjectsV2", mock.Anything, mock.Anything).Return(nil, errors.New("boom"))

	store := newS3Store(api, "artifacts")
	err := store.List(context.Background(), func(Object) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_store: list")
	assert.Contains(t, err.Error(), "boom")
}

func TestS3Store_RejectsInvalidKeyWithoutCallingS3(t *testing.T) {
	key := "../" + uuid.New().String() + "/x"
	ctx := context.Background()

	t.Run("Put", func(t *testing.T) {
		api := &mockS3API{}
		err := newS3Store(api, "artifacts").Put(ctx, uuid.New(), key, bytes.NewReader(nil), 0, "text/plain")
		require.ErrorIs(t, err, ErrInvalidKey)
		api.AssertNotCalled(t, "PutObject", mock.Anything, mock.Anything)
	})
	t.Run("Get", func(t *testing.T) {
		api := &mockS3API{}
		_, err := newS3Store(api, "artifacts").Get(ctx, uuid.New(), key)
		require.ErrorIs(t, err, ErrInvalidKey)
		api.AssertNotCalled(t, "GetObject", mock.Anything, mock.Anything)
	})
	t.Run("Delete", func(t *testing.T) {
		api := &mockS3API{}
		err := newS3Store(api, "artifacts").Delete(ctx, uuid.New(), key)
		require.ErrorIs(t, err, ErrInvalidKey)
		api.AssertNotCalled(t, "DeleteObject", mock.Anything, mock.Anything)
	})
}
