package fake

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

func TestSessionDoReportsAnEndedSession(t *testing.T) {
	t.Parallel()
	s := &Session{enc: json.NewEncoder(io.Discard), acks: make(chan Report)}
	close(s.acks)
	_, err := s.Do(context.Background(), Command{Cmd: CmdText})
	require.ErrorContains(t, err, "session ended waiting for text's ack")
}

func TestSessionDoGivesUpWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	s := &Session{enc: json.NewEncoder(io.Discard), acks: make(chan Report)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Do(ctx, Command{Cmd: CmdText})
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "waiting for text's ack")
}

// A reader stuck handing over a report nobody reads stops when the
// controller closes, and ends the session's channels.
func TestReadReportsStopsOnCloseWhileHandingOverAReport(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	s := &Session{conn: server, reports: make(chan Report), acks: make(chan Report)}
	started, closed, done := make(chan *Session, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		s.readReports(started, closed)
	}()

	enc := json.NewEncoder(client)
	require.NoError(t, enc.Encode(Report{Rep: RepStarted, ID: "x"}))
	<-started
	// A pipe's write returns once the reader has taken the bytes; the reader
	// then blocks on the unbuffered reports channel, which nobody reads.
	require.NoError(t, enc.Encode(Report{Rep: RepKey}))
	close(closed)
	<-done

	_, ok := <-s.reports
	assert.False(t, ok, "the report was not delivered")
	_, ok = <-s.acks
	assert.False(t, ok)
}

func TestCodecOpenReturnsTheReadError(t *testing.T) {
	t.Parallel()
	var sent []byte
	w := rpcdriver.NewWire(writerFunc(func(b []byte) (int, error) { sent = append(sent, b...); return len(b), nil }))
	_, err := codec{}.Open(context.Background(), w, func(context.Context) ([]byte, error) { return nil, io.ErrUnexpectedEOF })
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.JSONEq(t, `{"op":"hello"}`, string(sent), "it greeted before reading")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
