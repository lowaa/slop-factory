package artifact_store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalStore_PutThenGet_RoundTrips(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantID := uuid.New()

	err = store.Put(context.Background(), tenantID, "abc/file.png", bytes.NewReader([]byte("hello")), 5, "image/png")
	require.NoError(t, err)

	got, err := store.Get(context.Background(), tenantID, "abc/file.png")
	require.NoError(t, err)
	defer got.Close()

	data, err := io.ReadAll(got)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestLocalStore_Get_NamespacesByTenant(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantA, tenantB := uuid.New(), uuid.New()

	require.NoError(t, store.Put(context.Background(), tenantA, "same/key", bytes.NewReader([]byte("a's content")), 0, "text/plain"))
	require.NoError(t, store.Put(context.Background(), tenantB, "same/key", bytes.NewReader([]byte("b's content")), 0, "text/plain"))

	gotA, err := store.Get(context.Background(), tenantA, "same/key")
	require.NoError(t, err)
	defer gotA.Close()
	dataA, err := io.ReadAll(gotA)
	require.NoError(t, err)
	assert.Equal(t, "a's content", string(dataA))

	gotB, err := store.Get(context.Background(), tenantB, "same/key")
	require.NoError(t, err)
	defer gotB.Close()
	dataB, err := io.ReadAll(gotB)
	require.NoError(t, err)
	assert.Equal(t, "b's content", string(dataB))
}

func TestLocalStore_Get_WrapsMissingKeyError(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)

	_, err = store.Get(context.Background(), uuid.New(), "does/not/exist.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_store: get")
}

func TestNewLocalStore_CreatesBaseDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "does", "not", "exist", "yet")

	_, err := NewLocalStore(dir)
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func putBytes(t *testing.T, store *LocalStore, tenantID uuid.UUID, key, content string) {
	t.Helper()
	require.NoError(t, store.Put(context.Background(), tenantID, key, bytes.NewReader([]byte(content)), int64(len(content)), "text/plain"))
}

func TestLocalStore_Delete_RemovesOnlyThatTenantsObject(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantA, tenantB := uuid.New(), uuid.New()
	putBytes(t, store, tenantA, "same/key", "a")
	putBytes(t, store, tenantB, "same/key", "b")

	require.NoError(t, store.Delete(context.Background(), tenantA, "same/key"))

	_, err = store.Get(context.Background(), tenantA, "same/key")
	require.Error(t, err)
	got, err := store.Get(context.Background(), tenantB, "same/key")
	require.NoError(t, err)
	defer got.Close()
	data, err := io.ReadAll(got)
	require.NoError(t, err)
	assert.Equal(t, "b", string(data))
}

func TestLocalStore_Delete_MissingKeySucceeds(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, store.Delete(context.Background(), uuid.New(), "does/not/exist.png"))
}

func TestLocalStore_List_YieldsEveryObject(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantA, tenantB := uuid.New(), uuid.New()
	putBytes(t, store, tenantA, "abc/file.png", "1")
	putBytes(t, store, tenantA, "top.txt", "2")
	putBytes(t, store, tenantB, "abc/file.png", "3")

	type ref struct {
		tenant uuid.UUID
		key    string
	}
	var got []ref
	require.NoError(t, store.List(context.Background(), func(o Object) error {
		assert.False(t, o.LastModified.IsZero())
		got = append(got, ref{o.TenantID, o.Key})
		return nil
	}))

	assert.ElementsMatch(t, []ref{
		{tenantA, "abc/file.png"},
		{tenantA, "top.txt"},
		{tenantB, "abc/file.png"},
	}, got)
}

func TestLocalStore_List_ReportsFileOutsideTenantFolderWithNilTenant(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalStore(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644))

	var got []Object
	require.NoError(t, store.List(context.Background(), func(o Object) error {
		got = append(got, o)
		return nil
	}))

	require.Len(t, got, 1)
	assert.Equal(t, uuid.Nil, got[0].TenantID)
	assert.Equal(t, "stray.txt", got[0].Key)
}

func TestLocalStore_Delete_RejectsKeyReachingAnotherTenant(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantA, tenantB := uuid.New(), uuid.New()
	putBytes(t, store, tenantB, "k", "b")

	err = store.Delete(context.Background(), tenantA, "../"+tenantB.String()+"/k")

	require.ErrorIs(t, err, ErrInvalidKey)
	got, err := store.Get(context.Background(), tenantB, "k")
	require.NoError(t, err)
	defer got.Close()
	data, err := io.ReadAll(got)
	require.NoError(t, err)
	assert.Equal(t, "b", string(data))
}

func TestLocalStore_List_StopsAndReturnsCallbackError(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	require.NoError(t, err)
	tenantID := uuid.New()
	putBytes(t, store, tenantID, "one", "1")
	putBytes(t, store, tenantID, "two", "2")

	stop := errors.New("stop")
	calls := 0
	err = store.List(context.Background(), func(Object) error {
		calls++
		return stop
	})

	require.ErrorIs(t, err, stop)
	assert.Equal(t, 1, calls)
}
