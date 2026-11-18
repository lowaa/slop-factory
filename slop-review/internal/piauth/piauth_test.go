package piauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePi is a stand-in pi executable. Each run appends its arguments to
// argsFile, prints stdoutFile to stdout and stderrFile to stderr, and
// exits with the code in exitFile -- all rewritable between calls.
type fakePi struct {
	dir  string
	path string
}

func newFakePi(t *testing.T) *fakePi {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := `#!/bin/sh
echo "$*" >> "` + dir + `/args"
cat "` + dir + `/stdout" 2>/dev/null
cat "` + dir + `/stderr" >&2 2>/dev/null
exit "$(cat "` + dir + `/exit" 2>/dev/null || echo 0)"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return &fakePi{dir: dir, path: path}
}

func (f *fakePi) set(t *testing.T, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644))
}

func (f *fakePi) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "args"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSource(f *fakePi, c *clock) *TokenSource {
	return New(&Options{Command: []string{f.path}, MinExpiry: 20 * time.Minute, Now: c.now})
}

func TestToken_RunsPiAndCachesForHalfTheMinimumExpiry(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	c := &clock{t: time.Unix(1000, 0)}
	s := newSource(f, c)

	tok, err := s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
	assert.Equal(t, []string{"auth print-bearer-token --provider openai --min-expiry 20m"}, f.calls(t))

	f.set(t, "stdout", "tok-2\n")
	c.t = c.t.Add(9 * time.Minute)
	tok, err = s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok, "still within MinExpiry/2: no second pi run")
	assert.Len(t, f.calls(t), 1)

	c.t = c.t.Add(2 * time.Minute)
	tok, err = s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok, "past MinExpiry/2 pi is asked again")
	assert.Len(t, f.calls(t), 2)
}

func TestToken_NotLoggedInSurfacesPisExplanation(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stderr", "\x1b[31mError: Provider \"openai\" is not configured with an OAuth bearer token\x1b[39m\n")
	f.set(t, "exit", "1")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})

	_, err := s.Token(context.Background())
	require.ErrorIs(t, err, ErrNoToken)
	assert.Contains(t, err.Error(), `Provider "openai" is not configured with an OAuth bearer token`)
	assert.NotContains(t, err.Error(), "\x1b[", "colour codes are stripped")
}

func TestToken_NonZeroExitNeverRepeatsStdout(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "half-printed-secret\n")
	f.set(t, "exit", "3")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})

	_, err := s.Token(context.Background())
	require.ErrorIs(t, err, ErrNoToken)
	assert.NotContains(t, err.Error(), "half-printed-secret")
	assert.Contains(t, err.Error(), "exit status 3")
}

func TestToken_RejectsOutputThatIsNotOneToken(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "some chatter\ntok\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})

	_, err := s.Token(context.Background())
	require.ErrorIs(t, err, ErrNoToken)
	assert.NotContains(t, err.Error(), "chatter")
}

func TestToken_MissingPi(t *testing.T) {
	s := New(&Options{Command: []string{filepath.Join(t.TempDir(), "no-such-pi")}})

	_, err := s.Token(context.Background())
	assert.ErrorIs(t, err, ErrNoToken)
}

func TestMissingPi_SaysSoInsteadOfARawExecError(t *testing.T) {
	for name, command := range map[string]string{
		"not on PATH": "no-such-pi-on-path",
		"bad path":    filepath.Join(t.TempDir(), "no-such-pi"),
	} {
		t.Run(name, func(t *testing.T) {
			s := New(&Options{Command: []string{command}})

			for what, err := range map[string]error{
				"Check": s.Check(context.Background()),
				"Token": func() error { _, err := s.Token(context.Background()); return err }(),
			} {
				require.ErrorIs(t, err, ErrNoToken, what)
				assert.Contains(t, err.Error(), "pi is not installed or not on PATH", what)
			}
		})
	}
}

func TestToken_StderrIsScrubbedOfTokens(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "cached-token\n")
	c := &clock{t: time.Unix(1000, 0)}
	s := newSource(f, c)
	_, err := s.Token(context.Background())
	require.NoError(t, err)

	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	f.set(t, "stderr", "Error: refresh failed for cached-token and "+jwt+"\n")
	f.set(t, "exit", "1")
	c.t = c.t.Add(time.Hour)

	_, err = s.Token(context.Background())
	require.ErrorIs(t, err, ErrNoToken)
	assert.NotContains(t, err.Error(), "cached-token")
	assert.NotContains(t, err.Error(), jwt)
	assert.Contains(t, err.Error(), "refresh failed for [redacted]")
}

func TestRefresh_ReturnsANewTokenFromPi(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})
	stale, err := s.Token(context.Background())
	require.NoError(t, err)

	f.set(t, "stdout", "tok-2\n")
	tok, err := s.Refresh(context.Background(), stale)
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok)
	assert.Len(t, f.calls(t), 2, "a refresh runs pi even inside the cache window")

	tok, err = s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok, "the refreshed token is what's cached")
}

func TestRefresh_SameTokenFromPiIsAnError(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})
	stale, err := s.Token(context.Background())
	require.NoError(t, err)

	_, err = s.Refresh(context.Background(), stale)
	assert.ErrorIs(t, err, ErrSameToken)
}

func TestRefresh_SameTokenIsNotServedAgain(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})
	stale, err := s.Token(context.Background())
	require.NoError(t, err)
	_, err = s.Refresh(context.Background(), stale)
	require.ErrorIs(t, err, ErrSameToken)

	f.set(t, "stdout", "tok-2\n")
	tok, err := s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok, "a rejected token isn't reused from the cache")
}

func TestToken_ConcurrentCallersShareOnePiRun(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	tokens := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tok, err := s.Token(context.Background())
			assert.NoError(t, err)
			tokens[i] = tok
		}()
	}
	close(start)
	wg.Wait()

	assert.Len(t, f.calls(t), 1, "concurrent sessions must not each run pi")
	for _, tok := range tokens {
		assert.Equal(t, "tok-1", tok)
	}
}

func TestRefresh_AlreadyReplacedByAnotherCallerSkipsPi(t *testing.T) {
	f := newFakePi(t)
	f.set(t, "stdout", "tok-1\n")
	s := newSource(f, &clock{t: time.Unix(1000, 0)})
	stale, err := s.Token(context.Background())
	require.NoError(t, err)
	f.set(t, "stdout", "tok-2\n")
	_, err = s.Refresh(context.Background(), stale)
	require.NoError(t, err)

	tok, err := s.Refresh(context.Background(), stale)
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok)
	assert.Len(t, f.calls(t), 2, "the second caller rejected on tok-1 reuses tok-2 rather than running pi again")
}

func TestFormatDuration(t *testing.T) {
	assert.Equal(t, "20m", formatDuration(20*time.Minute))
	assert.Equal(t, "91s", formatDuration(90*time.Second+500*time.Millisecond))
}

func TestCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout, stderr, exit string
		wantErr              string
	}{
		"ready":     {stdout: `{"status":"ready","provider":"openai","authType":"oauth"}`},
		"not ready": {stdout: `{"status":"not_ready","provider":"openai","reason":"credentials_not_configured"}`, exit: "1", wantErr: "not_ready (credentials_not_configured)"},
		"api key":   {stdout: `{"status":"ready","provider":"openai","authType":"api_key"}`, wantErr: "api_key, not Sign in with ChatGPT"},
		"old pi":    {stderr: "Unknown auth command", exit: "1", wantErr: "Unknown auth command"},
		"silent pi": {wantErr: "pi 0.99.1 or newer"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakePi(t)
			f.set(t, "stdout", tc.stdout)
			f.set(t, "stderr", tc.stderr)
			if tc.exit != "" {
				f.set(t, "exit", tc.exit)
			}
			err := newSource(f, &clock{t: time.Unix(1000, 0)}).Check(context.Background())
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, []string{"auth check --provider openai --json"}, f.calls(t))
				return
			}
			require.ErrorIs(t, err, ErrNoToken)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.NotContains(t, err.Error(), "not installed or not on PATH")
		})
	}
}
