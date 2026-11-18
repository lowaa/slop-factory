package artifact_store

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestSplitObjectPath(t *testing.T) {
	id := uuid.New()
	other := uuid.New()
	upper := "0A1B2C3D-0000-4000-8000-000000000000"

	tests := []struct {
		name string
		path string
		want Object
	}{
		{"canonical uuid prefix", id.String() + "/abc/file.png", Object{TenantID: id, Key: "abc/file.png"}},
		// Only the canonical lowercase form is what objectKey writes, so
		// anything else wasn't written by this package.
		{"uppercase uuid prefix", upper + "/k", Object{Key: upper + "/k"}},
		{"no slash", "stray.txt", Object{Key: "stray.txt"}},
		{"not a uuid", "not-a-uuid/x", Object{Key: "not-a-uuid/x"}},
		{"trailing slash, empty key", id.String() + "/", Object{Key: id.String() + "/"}},
		// A traversing key isn't something this package wrote, so it must not
		// be attributed to either tenant.
		{"key escaping into another tenant", id.String() + "/../" + other.String() + "/x", Object{Key: id.String() + "/../" + other.String() + "/x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, splitObjectPath(tt.path))
		})
	}
}

func TestCheckKey(t *testing.T) {
	for _, key := range []string{"a.png", "abc/file.png"} {
		t.Run("valid "+key, func(t *testing.T) {
			assert.NoError(t, checkKey(key))
		})
	}
	for _, key := range []string{"", "/abs", "..", "../x", "a/../../x", "a/./b", "a//b", "a/"} {
		t.Run("invalid "+key, func(t *testing.T) {
			assert.ErrorIs(t, checkKey(key), ErrInvalidKey)
		})
	}
}
