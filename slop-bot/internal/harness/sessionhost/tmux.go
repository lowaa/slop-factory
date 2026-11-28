package sessionhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultSocket is slop-bot's own tmux server: agents never share the user's.
const DefaultSocket = "slop-bot"

// diedChannel is the tmux wait-for channel a session's death is signalled on.
const diedChannel = "slop-bot-died-"

// sessionName is what slop-bot names sessions: it keeps every name safe in a
// tmux target, a wait-for channel and the pane-died hook's shell command.
var sessionName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TmuxOptions configure a Tmux host.
type TmuxOptions struct {
	// Binary is the tmux executable; "" looks tmux up on PATH.
	Binary string
	// Socket is the server's name (tmux -L); "" is DefaultSocket. Tests use
	// a unique one each.
	Socket string
	// TmpDir is TMUX_TMPDIR, where the socket lives; "" is tmux's default.
	TmpDir string
	// ExitRecheck bounds each WaitExit (see there); 0 is a second.
	ExitRecheck time.Duration
	Logger      *slog.Logger
}

// Tmux is a SessionHost on slop-bot's own tmux server.
type Tmux struct {
	run     *runner
	logger  *slog.Logger
	recheck time.Duration
	// env is the absolute env(1), which keeps a one-element argv away from
	// tmux's `sh -c`, and clears TMUX for an attach.
	env string
	// socketPath is the server's socket, for an attach that must not depend
	// on the person's TMUX_TMPDIR.
	socketPath string
	// startMu serialises Start: it sets update-environment, a server-global
	// option, for the session it is creating.
	startMu sync.Mutex
	bufSeq  atomic.Uint64
}

// NewTmux returns a host on opts' server; it starts the server lazily.
func NewTmux(opts *TmuxOptions) (*Tmux, error) {
	bin := opts.Binary
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("tmux"); err != nil {
			return nil, fmt.Errorf("sessionhost: tmux is not installed: %w", err)
		}
	}
	// The pane-died hook runs bin through a shell; a path that needs quoting
	// isn't worth the risk of getting it wrong.
	if strings.ContainsAny(bin, "'\"$`\\ \n") {
		return nil, fmt.Errorf("sessionhost: tmux path %q needs shell quoting", bin)
	}
	socket := opts.Socket
	if socket == "" {
		socket = DefaultSocket
	}
	if !sessionName.MatchString(socket) {
		return nil, fmt.Errorf("sessionhost: invalid socket name %q", socket)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	recheck := opts.ExitRecheck
	if recheck <= 0 {
		recheck = time.Second
	}
	env, err := exec.LookPath("env")
	if err != nil {
		return nil, fmt.Errorf("sessionhost: env is not installed: %w", err)
	}
	return &Tmux{
		run:    &runner{bin: bin, socket: socket, tmpdir: opts.TmpDir},
		logger: logger, recheck: recheck, env: env,
		socketPath: socketPath(opts.TmpDir, socket),
	}, nil
}

// socketPath is where tmux puts a -L socket: $TMUX_TMPDIR (else /tmp, its
// default), resolved as tmux resolves it, then tmux-<uid>/<name>. The
// runner never passes an inherited TMUX_TMPDIR, so only tmpdir counts.
func socketPath(tmpdir, socket string) string {
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	if real, err := filepath.EvalSymlinks(tmpdir); err == nil {
		tmpdir = real
	}
	return filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), socket)
}

// ensureServer starts the server, if it isn't running, with slop-bot's
// settings. It runs with an empty environment: a pane's environment is the
// server's global one plus the session's, and the global one is copied from
// whichever client started the server. Empty, it can't leak one agent's
// environment (its token) into another's.
func (t *Tmux) ensureServer(ctx context.Context) error {
	// pane-died signals the session's wait-for channel. wait-for doesn't
	// expand formats, so run-shell does it, in the background so a slow
	// shell never stalls the server.
	died := fmt.Sprintf(`run-shell -b '%s -S "#{socket_path}" wait-for -S "%s#{session_name}"'`, t.run.bin, diedChannel)
	_, err := t.run.do(ctx, &call{env: []string{}, args: chain(
		[]string{"start-server"},
		// Without sessions the server would exit at once, and the next
		// client, with an agent's environment, would start it again.
		[]string{"set-option", "-s", "exit-empty", "off"},
		// Modified keys (shift-enter, ...) reach the harness as it expects.
		[]string{"set-option", "-s", "extended-keys", "on"},
		// A dead pane stays, so its exit status can be read.
		[]string{"set-option", "-g", "remain-on-exit", "on"},
		[]string{"set-hook", "-g", "pane-died", died},
	)})
	return err
}

// Start implements SessionHost. The environment reaches the session through
// the client's own environment (update-environment), never its argv, where
// every user on the host could read a token.
func (t *Tmux) Start(ctx context.Context, spec *StartSpec) error {
	if !sessionName.MatchString(spec.Name) {
		return fmt.Errorf("sessionhost: invalid session name %q", spec.Name)
	}
	if len(spec.Argv) == 0 {
		return errors.New("sessionhost: no command")
	}
	if err := t.ensureServer(ctx); err != nil {
		return err
	}
	names := envNames(spec.Env)
	newSession := []string{"new-session", "-d", "-s", spec.Name, "-x", "200", "-y", "50"}
	if spec.Dir != "" {
		newSession = append(newSession, "-c", spec.Dir)
	}
	// tmux runs a one-element command through `sh -c`; through env(1) it is
	// exec'd as given, like any other.
	newSession = append(append(newSession, "--", t.env), spec.Argv...)
	t.startMu.Lock()
	defer t.startMu.Unlock()
	_, err := t.run.do(ctx, &call{env: spec.Env, args: chain(
		[]string{"set-option", "-g", "update-environment", strings.Join(names, " ")},
		newSession,
	)})
	if err != nil {
		return fmt.Errorf("sessionhost: starting %s: %w", spec.Name, err)
	}
	return nil
}

// List implements SessionHost.
func (t *Tmux) List(ctx context.Context) ([]string, error) {
	out, err := t.run.do(ctx, &call{args: []string{"list-sessions", "-F", "#{session_name}"}})
	if errors.Is(err, ErrNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// SendKeys implements SessionHost.
func (t *Tmux) SendKeys(ctx context.Context, session string, keys ...string) error {
	_, err := t.run.do(ctx, &call{args: append([]string{"send-keys", "-t", pane(session)}, keys...)})
	return err
}

// SendText implements SessionHost.
func (t *Tmux) SendText(ctx context.Context, session, text string) error {
	_, err := t.run.do(ctx, &call{args: []string{"send-keys", "-l", "-t", pane(session), "--", text}})
	return err
}

// Paste implements SessionHost. -p brackets the paste (when the harness has
// asked for bracketed paste, as every TUI harness does), and the buffer is
// named per paste, so concurrent pastes can't swap texts.
func (t *Tmux) Paste(ctx context.Context, session, text string) error {
	buf := fmt.Sprintf("slop-bot-%d", t.bufSeq.Add(1))
	if _, err := t.run.do(ctx, &call{stdin: strings.NewReader(text), args: []string{"load-buffer", "-b", buf, "-"}}); err != nil {
		return err
	}
	_, err := t.run.do(ctx, &call{args: []string{"paste-buffer", "-p", "-d", "-b", buf, "-t", pane(session)}})
	if err != nil {
		_, _ = t.run.do(ctx, &call{args: []string{"delete-buffer", "-b", buf}})
	}
	return err
}

// Capture implements SessionHost.
func (t *Tmux) Capture(ctx context.Context, session string) (string, error) {
	return t.run.do(ctx, &call{args: []string{"capture-pane", "-p", "-J", "-t", pane(session)}})
}

// Status implements SessionHost.
//
// tmux 3.4 sometimes loses the SIGCHLD of a pane's process (seen in about a
// third of quick exits under parallel tests): the process stays an unreaped
// zombie, so the pane has no exit status and pane-died never fires, though
// the pane's terminal has closed (#{pane_dead}). Any later child exit makes
// tmux reap every zombie, so Status, finding a closed pane without a status,
// runs a child (run-shell true) and reads again.
func (t *Tmux) Status(ctx context.Context, session string) (*Status, error) {
	st, closed, err := t.status(ctx, session)
	if err != nil || st.Dead || !closed {
		return st, err
	}
	rctx, cancel := context.WithTimeout(ctx, t.recheck)
	_, _ = t.run.do(rctx, &call{args: []string{"run-shell", "true"}})
	cancel()
	st, _, err = t.status(ctx, session)
	return st, err
}

// status reads the pane once; closed means its terminal has closed, which
// counts as dead only once tmux has the exit status.
func (t *Tmux) status(ctx context.Context, session string) (*Status, bool, error) {
	out, err := t.run.do(ctx, &call{args: []string{
		"display-message", "-p", "-t", pane(session),
		"#{session_name}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}\t#{session_attached}",
	}})
	if err != nil {
		return nil, false, err
	}
	f := strings.Split(strings.TrimRight(out, "\n"), "\t")
	// display-message doesn't fail on a missing target: it expands the
	// formats without one.
	if f[0] != session {
		return nil, false, fmt.Errorf("%w: %s", ErrNoSession, session)
	}
	if len(f) != 5 {
		return nil, false, fmt.Errorf("sessionhost: unexpected status %q", out)
	}
	st := &Status{}
	st.Attached, _ = strconv.Atoi(f[4])
	closed := f[1] == "1"
	if closed {
		if sig, err := strconv.Atoi(f[3]); err == nil && sig > 0 {
			st.Dead, st.ExitStatus = true, 128+sig
		} else if code, err := strconv.Atoi(f[2]); err == nil {
			st.Dead, st.ExitStatus = true, code
		}
	}
	return st, closed, nil
}

// WaitExit implements SessionHost, on the channel the pane-died hook (or
// Kill) signals. A signal sent while nobody waits is kept by tmux, so a
// death between a Status check and WaitExit isn't missed; one kept from an
// earlier session of the same name makes WaitExit return early.
//
// Each wait is bounded by the recheck interval, because the signal can
// fail to come: tmux 3.4 can lose a pane's SIGCHLD (see Status), and so
// never fire pane-died. The caller's Status check is what makes the exit
// certain; the signal only makes it prompt.
func (t *Tmux) WaitExit(ctx context.Context, session string) error {
	channel := diedChannel + session
	wctx, cancel := context.WithTimeout(ctx, t.recheck)
	defer cancel()
	_, err := t.run.do(wctx, &call{args: []string{"wait-for", channel}})
	switch {
	case errors.Is(err, ErrNoServer):
		return nil
	case err != nil && wctx.Err() != nil:
		// tmux keeps a killed client's wait registered, and the next signal
		// goes to it instead of being kept for a later wait. So the abandoned
		// wait is released by signalling it: the caller's Status check stands
		// in for whatever that signal might have been.
		rctx, rcancel := context.WithTimeout(context.Background(), t.recheck)
		defer rcancel()
		_, _ = t.run.do(rctx, &call{args: []string{"wait-for", "-S", channel}})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	return err
}

// SetTag implements SessionHost, as a tmux user option.
func (t *Tmux) SetTag(ctx context.Context, session, name, value string) error {
	_, err := t.run.do(ctx, &call{args: []string{"set-option", "-t", pane(session), "@" + name, value}})
	return err
}

// Tag implements SessionHost.
func (t *Tmux) Tag(ctx context.Context, session, name string) (string, error) {
	out, err := t.run.do(ctx, &call{args: []string{"show-options", "-v", "-q", "-t", pane(session), "@" + name}})
	return strings.TrimRight(out, "\n"), err
}

// Kill implements SessionHost. Killing a session fires no pane-died, so Kill
// signals the session's channel itself.
func (t *Tmux) Kill(ctx context.Context, session string) error {
	_, err := t.run.do(ctx, &call{args: chain(
		[]string{"kill-session", "-t", target(session)},
		[]string{"wait-for", "-S", diedChannel + session},
	)})
	return err
}

// KillServer ends the server and every session on it.
func (t *Tmux) KillServer(ctx context.Context) error {
	_, err := t.run.do(ctx, &call{args: []string{"kill-server"}})
	if errors.Is(err, ErrNoServer) {
		return nil
	}
	return err
}

// AttachCommand implements SessionHost. It names the socket by path, so the
// person's TMUX_TMPDIR doesn't matter, and clears TMUX, which tmux would
// otherwise take for nesting inside the person's own tmux.
func (t *Tmux) AttachCommand(session string) []string {
	return []string{t.env, "-u", "TMUX", t.run.bin, "-S", t.socketPath, "attach-session", "-t", target(session)}
}

// target names a session exactly: tmux otherwise matches a prefix, so
// "agent-1" could hit "agent-12".
func target(session string) string { return "=" + session }

// pane is the session's (only) pane. Commands that take a pane target
// (set-option and show-options among them) reject a bare "=session".
func pane(session string) string { return "=" + session + ":" }

// separator stands for chain's command separator until runner.do writes the
// arguments out; anything else that ends in ";" is escaped there.
const separator = "\x00;"

// chain joins tmux commands into one invocation, run in order by the server.
func chain(cmds ...[]string) []string {
	var out []string
	for i, c := range cmds {
		if i > 0 {
			out = append(out, separator)
		}
		out = append(out, c...)
	}
	return out
}

// literal keeps an argument from ending a tmux command: tmux takes a
// trailing ";" on any argument as a separator (and runs what follows as
// commands), and "\;" as a literal one.
func literal(arg string) string {
	if arg == separator {
		return ";"
	}
	if strings.HasSuffix(arg, ";") {
		return arg[:len(arg)-1] + `\;`
	}
	return arg
}

func envNames(env []string) []string {
	var names []string
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ErrNoServer means the tmux server isn't running, so no session exists.
var ErrNoServer = errors.New("sessionhost: tmux server not running")

var (
	// Only an absent socket: "error connecting to ... (Permission denied)"
	// is a failure, not a missing server.
	noServer = []string{"no server running", "(No such file or directory)", "(Connection refused)", "server exited unexpectedly"}
	// "no current target" is what a server left with no sessions says.
	noSession = []string{"can't find session", "can't find pane", "can't find window", "session not found", "no such session", "no current target"}
)

// runner runs the tmux binary against one server.
type runner struct {
	bin, socket, tmpdir string
}

type call struct {
	args  []string
	stdin io.Reader
	// env is the client's environment; nil is the minimal one.
	env []string
}

func (r *runner) do(ctx context.Context, c *call) (string, error) {
	args := []string{"-L", r.socket, "-f", "/dev/null", "-u"}
	for _, a := range c.args {
		args = append(args, literal(a))
	}
	cmd := exec.CommandContext(ctx, r.bin, args...) // #nosec G204 -- tmux is run with slop-bot's own arguments; session names are validated
	cmd.Env = r.env(c.env)
	cmd.Stdin = c.stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case containsAny(msg, noServer):
			return "", fmt.Errorf("%w: %s", ErrNoServer, msg)
		case containsAny(msg, noSession):
			return "", fmt.Errorf("%w: %s", ErrNoSession, msg)
		case ctx.Err() != nil:
			return "", ctx.Err()
		}
		return "", fmt.Errorf("sessionhost: tmux %s: %w: %s", c.args[0], err, msg)
	}
	return stdout.String(), nil
}

// env is a client's environment. The client must agree with the server on
// TMUX_TMPDIR, and must not see the user's own TMUX, which would make
// tmux think it is nested.
func (r *runner) env(base []string) []string {
	if base == nil {
		base = []string{"PATH=" + os.Getenv("PATH")}
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_TMPDIR=") {
			out = append(out, kv)
		}
	}
	if r.tmpdir != "" {
		out = append(out, "TMUX_TMPDIR="+r.tmpdir)
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
