package fake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// escapeTime is how long a lone ESC waits for the rest of an escape
// sequence before it counts as the Escape key, as every TUI (and tmux
// itself) does.
const escapeTime = 25 * time.Millisecond

type tuiOptions struct {
	id      string
	resumed bool
	stdin   io.Reader
	stdout  io.Writer
	getenv  func(string) string
	rep     *reporter
	cmds    <-chan Command
}

// tui is the fake's TUI mode: a prompt line and a busy/idle footer on the
// terminal, keys and bracketed pastes read from it, and hooks run for what
// happens, as a TUI harness under the tuidriver kit.
type tui struct {
	*tuiOptions
	hookCmd []string
	keys    keyParser

	input  string
	status string
	inTurn bool
	// Scripted misbehaviour.
	ignoreInterrupts bool
	quietCancel      bool
	dropSubmits      int
}

func runTUI(o *tuiOptions) int {
	t := &tui{tuiOptions: o}
	if v := o.getenv(EnvHookCmd); v != "" {
		if err := json.Unmarshal([]byte(v), &t.hookCmd); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "fakeharness: %s: %v\n", EnvHookCmd, err)
			return 2
		}
	}
	defer rawMode(o.stdin)()
	// Bracketed paste: a pasted newline is text, not a submit.
	t.write("\x1b[?2004h")
	defer t.write("\x1b[?2004l")
	chunks := readChunks(o.stdin)
	t.rep.report(Report{Rep: RepStarted, ID: t.id, Resumed: t.resumed, TUI: true})
	t.hook(HookSessionStart, &HookPayload{Resumed: t.resumed})
	t.draw()
	return t.loop(chunks)
}

func (t *tui) loop(chunks <-chan []byte) int {
	var escTimer <-chan time.Time
	for {
		var evs []keyEvent
		select {
		case b, ok := <-chunks:
			if !ok {
				return 0 // the terminal went away
			}
			escTimer = nil
			evs = t.keys.feed(b)
			if t.keys.pendingEsc() {
				escTimer = time.After(escapeTime)
			}
		case <-escTimer:
			escTimer = nil
			evs = t.keys.flushEsc()
		case c, ok := <-t.cmds:
			if !ok {
				return 0 // the controller went away: the test is over
			}
			if code, exit := t.command(&c); exit {
				return code
			}
			continue
		}
		if code, exit := t.handleKeys(evs); exit {
			return code
		}
	}
}

func (t *tui) handleKeys(evs []keyEvent) (int, bool) {
	if len(evs) == 0 {
		return 0, false
	}
	for _, ev := range evs {
		switch ev.kind {
		case keyText:
			t.input += ev.text
		case keyPaste:
			text := strings.ReplaceAll(strings.ReplaceAll(ev.text, "\r\n", "\n"), "\r", "\n")
			t.rep.report(Report{Rep: RepPaste, Text: text})
			t.input += text
		case keyBackspace:
			if r := []rune(t.input); len(r) > 0 {
				t.input = string(r[:len(r)-1])
			}
		case keyEnter:
			if code, exit := t.submit(); exit {
				return code, true
			}
		case keyEscape:
			t.cancel()
		}
	}
	t.draw()
	return 0, false
}

func (t *tui) submit() (int, bool) {
	if t.dropSubmits > 0 {
		t.dropSubmits--
		t.rep.report(Report{Rep: RepKey, Key: "Enter", Dropped: true})
		return 0, false
	}
	t.rep.report(Report{Rep: RepKey, Key: "Enter"})
	text := t.input
	t.input = ""
	switch strings.TrimSpace(text) {
	case "":
		return 0, false
	case TUIQuit:
		t.rep.report(Report{Rep: RepQuit})
		return 0, true
	}
	t.status = ""
	t.hook(HookPromptSubmit, &HookPayload{Prompt: text})
	t.rep.report(Report{Rep: RepUser, Text: text, MidTurn: t.inTurn, Via: ViaKeys})
	if !t.rep.controlled() {
		t.startTurn()
		t.hook(HookText, &HookPayload{Text: "echo: " + text})
		t.endTurn("completed")
	}
	return 0, false
}

func (t *tui) cancel() {
	t.rep.report(Report{Rep: RepInterrupt})
	if !t.inTurn {
		t.input = ""
		return
	}
	if t.ignoreInterrupts {
		return
	}
	t.inTurn = false
	t.status = "interrupted"
	if !t.quietCancel {
		t.hook(HookTurnEnd, &HookPayload{Outcome: "interrupted"})
	}
}

func (t *tui) startTurn() {
	t.inTurn = true
	t.status = ""
	t.hook(HookTurnStart, &HookPayload{})
}

// endTurn runs the turn_end hook, whose reply can keep the turn going with
// injected messages, as agy's Stop hook does.
func (t *tui) endTurn(outcome string) {
	reply := t.hook(HookTurnEnd, &HookPayload{Outcome: outcome})
	if reply.Continue && len(reply.Inject) > 0 && outcome != "interrupted" {
		t.injected(reply.Inject)
		return
	}
	t.inTurn = false
	t.status = ""
}

func (t *tui) injected(msgs []InjectedMessage) {
	for _, m := range msgs {
		t.rep.report(Report{Rep: RepUser, Text: m.Text, From: m.From, MidTurn: t.inTurn, Via: ViaHook})
	}
}

// command runs one test command; it returns an exit code and true when the
// fake should exit.
func (t *tui) command(c *Command) (int, bool) {
	switch c.Cmd {
	case CmdTurnStart:
		t.startTurn()
	case CmdTurnEnd:
		t.endTurn(outcomeOr(c.Outcome))
	case CmdToolStart:
		t.status = "tool: " + c.Tool
		t.hook(HookToolStart, &HookPayload{Tool: c.Tool})
	case CmdToolEnd:
		t.status = ""
		t.hook(HookToolEnd, &HookPayload{Tool: c.Tool})
	case CmdText:
		t.status = c.Text
		t.hook(HookText, &HookPayload{Text: c.Text})
	case CmdUsage:
		t.hook(HookUsage, &HookPayload{Input: 100, Output: 10})
	case CmdStep:
		t.injected(t.hook(HookStep, &HookPayload{}).Inject)
	case CmdRun:
		t.rep.run(c.Argv)
		return 0, false
	case CmdIgnoreInterrupts:
		t.ignoreInterrupts = c.On
	case CmdIgnoreHangup:
		signal.Ignore(syscall.SIGHUP)
	case CmdQuietCancel:
		t.quietCancel = c.On
	case CmdDropSubmits:
		t.dropSubmits = c.Count
	case CmdExit:
		t.rep.ack(c.Cmd)
		return c.Code, true
	default:
		unknownCommand(t.rep, c.Cmd)
		return 0, false
	}
	// The screen is drawn after the hook, as a harness reports before it
	// redraws: the kit trusts a hook over a screen read close to it.
	t.draw()
	t.rep.ack(c.Cmd)
	return 0, false
}

// hook runs the hook command for event, and reports its reply.
func (t *tui) hook(event string, p *HookPayload) *HookReply {
	reply := &HookReply{}
	if len(t.hookCmd) == 0 {
		return reply
	}
	p.ConversationID = t.id
	in, err := json.Marshal(p)
	if err != nil {
		t.rep.report(Report{Rep: RepHook, Event: event, Err: err.Error()})
		return reply
	}
	argv := append(append([]string(nil), t.hookCmd[1:]...), event)
	cmd := exec.Command(t.hookCmd[0], argv...) // #nosec G204 -- the hook command is the test's
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.rep.report(Report{Rep: RepHook, Event: event, Err: fmt.Sprintf("%v: %s", err, stderr.String())})
		return reply
	}
	if out := bytes.TrimSpace(stdout.Bytes()); len(out) > 0 {
		if err := json.Unmarshal(out, reply); err != nil {
			t.rep.report(Report{Rep: RepHook, Event: event, Reply: string(out), Err: err.Error()})
			return &HookReply{}
		}
	}
	t.rep.report(Report{Rep: RepHook, Event: event, Reply: stdout.String()})
	return reply
}

// Screen markers, which the fake/tui profile matches.
const (
	footerBusy = "[fake] busy (esc to cancel)"
	footerIdle = "[fake] idle"
)

func (t *tui) draw() {
	title := "fake-tui " + t.id
	if t.resumed {
		title += " (resumed)"
	}
	footer := footerIdle
	if t.inTurn {
		footer = footerBusy
	}
	lines := []string{title, t.status, "> " + strings.ReplaceAll(t.input, "\n", " "), footer}
	t.write("\x1b[H\x1b[2J" + strings.Join(lines, "\r\n"))
}

func (t *tui) write(s string) { _, _ = io.WriteString(t.stdout, s) }

// rawMode puts a terminal stdin into raw mode, so every key arrives as it
// is pressed, and returns the restore.
func rawMode(r io.Reader) func() {
	f, ok := r.(*os.File)
	if !ok {
		return func() {}
	}
	fd := int(f.Fd()) // #nosec G115 -- a file descriptor fits an int
	if !term.IsTerminal(fd) {
		return func() {}
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}
	return func() { _ = term.Restore(fd, st) }
}

func readChunks(r io.Reader) <-chan []byte {
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				ch <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

type keyKind int

const (
	keyText keyKind = iota
	keyPaste
	keyEnter
	keyEscape
	keyBackspace
)

type keyEvent struct {
	kind keyKind
	text string
}

type parseState int

const (
	stNormal parseState = iota
	stEsc
	stCSI
	stPaste
)

var pasteEnd = []byte("\x1b[201~")

// keyParser turns terminal input into keys: text, Enter, Escape, Backspace
// and bracketed pastes. Other escape sequences (arrows, ...) are dropped.
type keyParser struct {
	state parseState
	seq   []byte
	paste []byte
}

func (p *keyParser) feed(b []byte) []keyEvent {
	var out []keyEvent
	for _, c := range b {
		out = p.byte(c, out)
	}
	return out
}

func (p *keyParser) byte(c byte, out []keyEvent) []keyEvent {
	switch p.state {
	case stEsc:
		if c == '[' {
			p.state, p.seq = stCSI, p.seq[:0]
			return out
		}
		p.state = stNormal
		return p.byte(c, append(out, keyEvent{kind: keyEscape}))
	case stCSI:
		p.seq = append(p.seq, c)
		if c >= 0x40 && c <= 0x7e {
			p.state = stNormal
			if string(p.seq) == "200~" {
				p.state, p.paste = stPaste, p.paste[:0]
			}
		}
		return out
	case stPaste:
		p.paste = append(p.paste, c)
		if bytes.HasSuffix(p.paste, pasteEnd) {
			p.state = stNormal
			return append(out, keyEvent{kind: keyPaste, text: string(p.paste[:len(p.paste)-len(pasteEnd)])})
		}
		return out
	}
	return p.normal(c, out)
}

func (p *keyParser) normal(c byte, out []keyEvent) []keyEvent {
	switch {
	case c == 0x1b:
		p.state = stEsc
	case c == '\r' || c == '\n':
		out = append(out, keyEvent{kind: keyEnter})
	case c == 0x7f || c == 0x08:
		out = append(out, keyEvent{kind: keyBackspace})
	case c >= 0x20 || c == '\t':
		// Byte by byte: a UTF-8 character's bytes join up in the text.
		if n := len(out); n > 0 && out[n-1].kind == keyText {
			out[n-1].text += string([]byte{c})
		} else {
			out = append(out, keyEvent{kind: keyText, text: string([]byte{c})})
		}
	}
	return out
}

// pendingEsc says the input ended on a lone ESC: the Escape key, unless
// more of a sequence follows within escapeTime.
func (p *keyParser) pendingEsc() bool { return p.state == stEsc }

func (p *keyParser) flushEsc() []keyEvent {
	p.state = stNormal
	return []keyEvent{{kind: keyEscape}}
}
