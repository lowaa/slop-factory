package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

var quiet = slog.New(slog.DiscardHandler)

// shortDir keeps socket paths under the sun_path limit: t.TempDir() can be
// long enough to exceed it.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sbc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

type tokens map[string]ident.Principal

func (m tokens) AgentForTokenHash(_ context.Context, hash string) (ident.Principal, error) {
	for tok, p := range m {
		if HashToken(tok) == hash {
			return p, nil
		}
	}
	return ident.Principal{}, ErrUnknownToken
}

type whoami struct {
	Kind   int    `json:"kind"`
	Agent  string `json:"agent"`
	Ticket string `json:"ticket"`
}

// serve starts a server with a /whoami route and a streaming route, and
// returns its socket.
func serve(t *testing.T, resolver TokenResolver) (string, *Server) {
	t.Helper()
	sock := filepath.Join(shortDir(t), "run", "s.sock")
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	s := NewServer(quiet)
	s.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r.Context())
		WriteJSON(w, http.StatusOK, whoami{Kind: int(p.Kind), Agent: string(p.Agent), Ticket: string(p.Ticket)})
	})
	s.HandleFunc("GET /fail", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusConflict, errors.New("ticket 7 is locked"))
	})
	s.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, `{"n":1}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	s.Start(l, resolver)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return sock, s
}

func TestNoTokenIsTheUser(t *testing.T) {
	t.Parallel()
	sock, _ := serve(t, tokens{})
	var got whoami
	require.NoError(t, NewClient(sock, "").Do(context.Background(), http.MethodGet, "/whoami", nil, &got))
	assert.Equal(t, int(ident.User), got.Kind)
}

func TestAnAgentTokenIsThatAgent(t *testing.T) {
	t.Parallel()
	tok, err := NewToken()
	require.NoError(t, err)
	sock, _ := serve(t, tokens{tok: ident.AgentPrincipal("a1", "7")})
	var got whoami
	require.NoError(t, NewClient(sock, tok).Do(context.Background(), http.MethodGet, "/whoami", nil, &got))
	assert.Equal(t, whoami{Kind: int(ident.Agent), Agent: "a1", Ticket: "7"}, got)
}

func TestAnUnknownTokenIsRefusedNotTreatedAsTheUser(t *testing.T) {
	t.Parallel()
	sock, _ := serve(t, tokens{})
	err := NewClient(sock, "made-up").Do(context.Background(), http.MethodGet, "/whoami", nil, nil)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
}

func TestErrorsCarryTheDaemonsMessage(t *testing.T) {
	t.Parallel()
	sock, _ := serve(t, tokens{})
	err := NewClient(sock, "").Do(context.Background(), http.MethodGet, "/fail", nil, nil)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Status)
	assert.Equal(t, "ticket 7 is locked", apiErr.Message)
}

func TestSocketAndDirectoryAreOwnerOnly(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(shortDir(t), "run")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	sock := filepath.Join(dir, "s.sock")
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()

	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm(), "a pre-existing loose directory is tightened")
	si, err := os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), si.Mode().Perm())
}

func TestAStaleSocketIsReplacedButALiveOneIsRefused(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortDir(t), "s.sock")
	require.NoError(t, os.WriteFile(sock, nil, 0o600)) // left by a dead daemon
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	_, err = Listen(sock, quiet)
	assert.ErrorContains(t, err, "already listening")
}

func TestOtherUIDsAreDropped(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortDir(t), "s.sock")
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	var calls atomic.Int32
	l.peer = func(*net.UnixConn) (int, error) {
		if calls.Add(1) == 1 {
			return l.uid + 1, nil // the first caller is someone else
		}
		return l.uid, nil
	}
	// Every connection Accept returns is greeted, so a greeting proves the
	// connection got through.
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("hi"))
			_ = c.Close()
		}
	}()

	stranger, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer func() { _ = stranger.Close() }()
	_, err = bufio.NewReader(stranger).ReadByte()
	assert.Error(t, err, "the stranger is closed without a greeting")

	user, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer func() { _ = user.Close() }()
	b, err := bufio.NewReader(user).ReadByte()
	require.NoError(t, err)
	assert.Equal(t, byte('h'), b, "the user's own connection is accepted")
}

func TestPeerUIDIsTheRealUID(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortDir(t), "s.sock")
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	got := make(chan int, 1)
	go func() {
		c, err := l.Listener.Accept()
		if err != nil {
			got <- -2
			return
		}
		uid, _ := peerUID(c.(*net.UnixConn))
		_ = c.Close()
		got <- uid
	}()
	c, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	assert.Equal(t, os.Getuid(), <-got)
}

func TestNoDaemonNamesTheSocket(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortDir(t), "absent.sock")
	err := NewClient(sock, "").Do(context.Background(), http.MethodGet, "/whoami", nil, nil)
	var nr *NotRunningError
	require.ErrorAs(t, err, &nr)
	assert.Contains(t, err.Error(), sock)
}

func TestStopEndsLongLivedStreams(t *testing.T) {
	t.Parallel()
	sock, s := serve(t, tokens{})
	body, err := NewClient(sock, "").Stream(context.Background(), "/stream")
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	r := bufio.NewReader(body)
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":1}`, line)

	// Without cancelling request contexts, Shutdown would wait on the
	// stream until this context's deadline; with no deadline, forever.
	require.NoError(t, s.Stop(context.Background()))
	_, err = r.ReadString('\n')
	assert.Error(t, err, "the stream ends when the server stops")
}

func TestCloseRemovesTheSocket(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortDir(t), "s.sock")
	l, err := Listen(sock, quiet)
	require.NoError(t, err)
	require.NoError(t, l.Close())
	_, err = os.Stat(sock)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestStatusFor(t *testing.T) {
	t.Parallel()
	errLocked := errors.New("locked")
	m := []ErrorMapping{{Err: errLocked, Status: http.StatusConflict}}
	assert.Equal(t, http.StatusConflict, StatusFor(fmt.Errorf("wrap: %w", errLocked), m))
	assert.Equal(t, http.StatusInternalServerError, StatusFor(errors.New("other"), m))
}

func TestHashTokenIsStableAndNotTheToken(t *testing.T) {
	t.Parallel()
	assert.Equal(t, HashToken("x"), HashToken("x"))
	assert.NotEqual(t, "x", HashToken("x"))
	a, err := NewToken()
	require.NoError(t, err)
	b, err := NewToken()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}
