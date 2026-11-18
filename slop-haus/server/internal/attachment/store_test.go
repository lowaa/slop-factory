package attachment_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

func TestReadCredentials(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "s3")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}
	id, secret, err := attachment.ReadCredentials(write("# keys\nS3_ACCESS_KEY_ID=abc\nS3_SECRET_ACCESS_KEY=d=ef\n"))
	require.NoError(t, err)
	assert.Equal(t, "abc", id)
	assert.Equal(t, "d=ef", secret, "a value may hold an equals sign")

	id, secret, err = attachment.ReadCredentials(write("RUSTFS_ACCESS_KEY=r1\nRUSTFS_SECRET_KEY=r2\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"r1", "r2"}, []string{id, secret}, "the names rustfs's own Secret uses")

	_, _, err = attachment.ReadCredentials(write("S3_ACCESS_KEY_ID=abc\n"))
	assert.Error(t, err, "both keys are needed")
	_, _, err = attachment.ReadCredentials(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
}

func TestTheQuotaDefaultsToFiveGiBAndIsOwnerOnly(t *testing.T) {
	def, err := settings.Default(attachment.QuotaSetting)
	require.NoError(t, err)
	assert.Equal(t, int64(5<<30), def.Int)
	for _, spec := range attachment.SettingSpecs() {
		assert.True(t, spec.OwnerOnly)
		assert.Error(t, spec.Validate(settings.IntValue(-1)))
		assert.NoError(t, spec.Validate(settings.IntValue(0)))
	}
}
