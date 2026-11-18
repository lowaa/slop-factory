package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithCredentials_ErrorHidesDSN(t *testing.T) {
	_, err := WithCredentials("postgres://owner:s3cret@db:5432/x\x7f", "app", "pw")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestWithCredentials_RefusesKeywordValueDSN(t *testing.T) {
	_, err := WithCredentials("host=db user=owner password=s3cret dbname=x", "app", "pw")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
}
