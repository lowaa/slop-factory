package agentproc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Once stdout has reached its end, the drain bound has nothing to do: it
// returns at once, without killing the group or closing the reader. The
// drain the exit started runs on its own goroutine, which no test waits for,
// so this runs one itself.
func TestADrainedStdoutIsLeftAlone(t *testing.T) {
	t.Parallel()
	p, err := start(context.Background(), sh(t, `echo bye`), time.Hour)
	require.NoError(t, err)
	<-p.Done()
	out, err := io.ReadAll(p.Stdout)
	require.NoError(t, err)
	require.Equal(t, "bye\n", string(out))

	p.drainStdout() // an hour's bound: only the end already seen returns it

	n, err := p.Stdout.Read(make([]byte, 1))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF, "the reader is still open, at its end")
}
