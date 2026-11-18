package reporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// TokenSource supplies the bearer token for one request. It is asked before
// every request rather than once at start-up: a token from a command may
// have expired since the previous one.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Key names whose token this is, stably across refreshes, so a cache
	// kept per tenant doesn't miss every time the token itself changes.
	Key() string
}

// StaticToken is a fixed token, as given in SLOP_REVIEW_REPORT_TOKEN.
func StaticToken(token string) TokenSource { return staticToken(token) }

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }
func (s staticToken) Key() string                           { return "token:" + string(s) }

const (
	// tokenCommandTimeout bounds one run of the command: a token refresh
	// that hangs must not hold up the review's own exit.
	tokenCommandTimeout = 15 * time.Second
	// maxTokenCommandOutput is far more than any bearer token needs; a
	// command printing more is not printing a token.
	maxTokenCommandOutput = 16 << 10
)

// CommandToken runs an executable for each token. See NewCommandToken.
type CommandToken struct {
	argv    []string
	timeout time.Duration
}

// NewCommandToken builds a TokenSource from command: the path of an
// executable (or a name on PATH), optionally followed by arguments, split
// on whitespace. It is run directly, never through a shell, so there is no
// quoting: an argument containing whitespace isn't supported, and neither is
// a path with whitespace unless the whole string is the executable's path --
// point it at a wrapper script instead.
func NewCommandToken(command string) (*CommandToken, error) {
	argv := strings.Fields(command)
	// A workspace can live under a path with spaces; when the whole string
	// is itself an executable file, it is the command, unsplit.
	if info, err := os.Stat(strings.TrimSpace(command)); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		argv = []string{strings.TrimSpace(command)}
	}
	if len(argv) == 0 {
		return nil, errors.New("reporter: the token command is empty")
	}
	return &CommandToken{argv: argv, timeout: tokenCommandTimeout}, nil
}

// Key is the command line: the same command means the same tenant.
func (c *CommandToken) Key() string { return "command:" + strings.Join(c.argv, " ") }

// Token runs the command and returns its stdout, trimmed. A failing
// command, empty output or output that isn't a single token is an error.
// What the command printed is never quoted in an error -- stdout is the
// token -- and its stderr is, cut to one line and with the token scrubbed
// in case it echoes it.
func (c *CommandToken) Token(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	stdout := &limitedBuffer{max: maxTokenCommandOutput}
	stderr := &limitedBuffer{max: maxTokenCommandOutput}
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Without a bound, a child holding the pipes open would make Wait block
	// past the timeout.
	cmd.WaitDelay = time.Second

	runErr := cmd.Run()
	token := strings.TrimSpace(stdout.buf.String())
	detail := func() string {
		line := scrub(firstLine(stderr.buf.Bytes()), token)
		if line == "" {
			return ""
		}
		return ": " + line
	}

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && runErr != nil:
		return "", fmt.Errorf("token command %q timed out after %s%s", c.argv[0], c.timeout, detail())
	case runErr != nil:
		return "", fmt.Errorf("token command %q failed: %s%s", c.argv[0], scrub(runErr.Error(), token), detail())
	case stdout.overflowed:
		return "", fmt.Errorf("token command %q printed more than %d bytes: that is not a token", c.argv[0], maxTokenCommandOutput)
	case token == "":
		return "", fmt.Errorf("token command %q printed nothing%s", c.argv[0], detail())
	case strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.IndexFunc(token, unicode.IsControl) >= 0:
		return "", fmt.Errorf("token command %q printed more than a single token", c.argv[0])
	}
	return token, nil
}

func scrub(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "[redacted]")
}

// limitedBuffer keeps the first max bytes and notes that it dropped the
// rest, so a runaway command can't fill memory. It always reports the full
// write as taken, so the command isn't failed by a short write.
type limitedBuffer struct {
	buf        bytes.Buffer
	max        int
	overflowed bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.max - b.buf.Len(); n > room {
		b.overflowed = true
		p = p[:room]
	}
	b.buf.Write(p)
	return n, nil
}
