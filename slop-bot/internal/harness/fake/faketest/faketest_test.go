package faketest

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

func TestReplyGivesUpWhenTheFakeEndsBeforeItsScript(t *testing.T) {
	t.Parallel()
	ctl := controller(t)
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	conn, err := net.Dial("unix", ctl.Socket())
	require.NoError(t, err)
	enc := json.NewEncoder(conn)
	require.NoError(t, enc.Encode(fake.Report{Rep: fake.RepStarted, ID: "gone"}))
	p := &Puppet{ctl: ctl}
	_, _, err = p.Started(ctx)
	require.NoError(t, err)

	require.NoError(t, p.Reply(ctx, "hi"))
	// The fake got the message, then died: no command of the script is acked.
	require.NoError(t, enc.Encode(fake.Report{Rep: fake.RepUser, Text: "hi"}))
	require.NoError(t, conn.Close())

	p.scripts.Wait()
	assert.NoError(t, p.Received(ctx, "hi", false), "it consumed the message before giving up")
}
