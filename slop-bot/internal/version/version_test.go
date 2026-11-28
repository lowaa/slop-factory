package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringDefaultsToDev(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "dev", String())
}
