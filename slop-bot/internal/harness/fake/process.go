package fake

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// Main is the fake harness process. It returns the exit code.
func Main(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	f := &proc{out: json.NewEncoder(stdout), getenv: getenv}
	id, resumed := getenv(EnvID), false
	if len(args) == 2 && args[0] == "--resume" {
		id, resumed = args[1], true
	}
	if id == "" {
		// Distinct per conversation, as a real harness's are: transcripts
		// are kept by id.
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		id = "fake-" + hex.EncodeToString(b)
	}
	f.id = id
	if dir := getenv(EnvTranscripts); dir != "" {
		// Appended to, so a resumed conversation keeps one transcript, as
		// the real harnesses do.
		t, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the test names the directory
		if err != nil {
			_, _ = fmt.Fprintln(stdout, `{"ev":"error"}`)
			return 70
		}
		defer func() { _ = t.Close() }()
		f.transcript = json.NewEncoder(t)
	}
	var cmds <-chan Command
	if sock := getenv(EnvControl); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			_, _ = fmt.Fprintln(stdout, `{"ev":"error"}`)
			return 70
		}
		defer func() { _ = conn.Close() }()
		f.ctl = json.NewEncoder(conn)
		cmds = readCommands(conn)
	}
	msgs := readStdin(stdin)
	f.report(Report{Rep: RepStarted, ID: id, Resumed: resumed})
	return f.loop(msgs, cmds)
}

type proc struct {
	mu     sync.Mutex
	out    *json.Encoder
	ctl    *json.Encoder
	getenv func(string) string
	id     string
	// transcript, when kept, gets each entry before its event is written,
	// so whoever reads it on the event finds it there.
	transcript *json.Encoder
	calls      int

	inTurn           bool
	ignoreInterrupts bool
	ignoreEOF        bool
}

func (f *proc) loop(msgs <-chan *inMsg, cmds <-chan Command) int {
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				msgs = nil
				f.report(Report{Rep: RepStdinClosed})
				if !f.ignoreEOF {
					return 0
				}
				continue
			}
			f.handle(m, cmds == nil)
		case c, ok := <-cmds:
			if !ok {
				// The controller went away: the test is over.
				return 0
			}
			if code, exit := f.command(&c); exit {
				return code
			}
		}
	}
}

func (f *proc) handle(m *inMsg, autonomous bool) {
	switch m.Op {
	case "hello":
		f.write(outMsg{Ev: "hello", ID: f.id})
	case "user":
		f.record(transcript.Entry{Kind: transcript.User, Text: m.Text})
		f.report(Report{Rep: RepUser, Text: m.Text, From: m.From, MidTurn: f.inTurn})
		if autonomous {
			f.write(outMsg{Ev: "turn_start"})
			f.record(transcript.Entry{Kind: transcript.Assistant, Text: "echo: " + m.Text})
			f.write(outMsg{Ev: "text", Text: "echo: " + m.Text})
			f.write(outMsg{Ev: "turn_end", Outcome: "completed"})
		}
	case "interrupt":
		f.report(Report{Rep: RepInterrupt})
		if f.inTurn && !f.ignoreInterrupts {
			f.inTurn = false
			f.write(outMsg{Ev: "turn_end", Outcome: "interrupted"})
		}
	}
}

// command runs one test command; it returns an exit code and true when the
// fake should exit.
func (f *proc) command(c *Command) (int, bool) {
	switch c.Cmd {
	case CmdTurnStart:
		f.inTurn = true
		f.write(outMsg{Ev: "turn_start"})
	case CmdTurnEnd:
		f.inTurn = false
		outcome := c.Outcome
		if outcome == "" {
			outcome = "completed"
		}
		f.write(outMsg{Ev: "turn_end", Outcome: outcome})
	case CmdToolStart:
		f.calls++
		f.record(transcript.Entry{Kind: transcript.ToolCall, Tool: c.Tool, CallID: f.callID(), Text: c.Text})
		f.write(outMsg{Ev: "tool_start", Tool: c.Tool})
	case CmdToolEnd:
		f.record(transcript.Entry{Kind: transcript.ToolResult, Tool: c.Tool, CallID: f.callID(), Text: c.Text})
		f.write(outMsg{Ev: "tool_end", Tool: c.Tool})
	case CmdText:
		f.record(transcript.Entry{Kind: transcript.Assistant, Text: c.Text})
		f.write(outMsg{Ev: "text", Text: c.Text})
	case CmdUsage:
		f.record(transcript.Entry{Kind: transcript.UsageKind, Usage: &transcript.Usage{Input: 100, Output: 10}})
		f.write(outMsg{Ev: "usage", Input: 100, Output: 10})
	case CmdLimit:
		f.write(outMsg{Ev: "limit", Used: c.Used, ResetsAt: c.ResetsAt})
	case CmdRun:
		f.run(c.Argv)
	case CmdIgnoreInterrupts:
		f.ignoreInterrupts = c.On
	case CmdIgnoreEOF:
		f.ignoreEOF = c.On
	case CmdExit:
		f.report(Report{Rep: RepAck, Ack: c.Cmd})
		return c.Code, true
	default:
		f.report(Report{Rep: RepAck, Ack: c.Cmd, Err: "unknown command " + strconv.Quote(c.Cmd)})
		return 0, false
	}
	if c.Cmd != CmdRun {
		f.report(Report{Rep: RepAck, Ack: c.Cmd})
	}
	return 0, false
}

func (f *proc) run(argv []string) {
	if len(argv) == 0 {
		f.report(Report{Rep: RepRan, Err: "no argv"})
		return
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) // #nosec G204 -- the test scripts this command
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		f.report(Report{Rep: RepRan, Err: err.Error()})
		return
	}
	f.report(Report{Rep: RepRan, Code: code, Stdout: stdout.String(), Stderr: stderr.String()})
}

func (f *proc) callID() string { return "call-" + strconv.Itoa(f.calls) }

// record appends e to the transcript, stamped now: the fake is a separate
// process, with no clock of the test's to read.
func (f *proc) record(e transcript.Entry) {
	if f.transcript == nil {
		return
	}
	e.At = time.Now().UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.transcript.Encode(&e)
}

func (f *proc) write(m outMsg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.out.Encode(m)
}

func (f *proc) report(r Report) {
	if f.ctl == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.ctl.Encode(r)
}

func readStdin(r io.Reader) <-chan *inMsg {
	ch := make(chan *inMsg)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			m := &inMsg{}
			if json.Unmarshal(sc.Bytes(), m) == nil {
				ch <- m
			}
		}
	}()
	return ch
}

func readCommands(r io.Reader) <-chan Command {
	ch := make(chan Command)
	go func() {
		defer close(ch)
		dec := json.NewDecoder(r)
		for {
			var c Command
			if dec.Decode(&c) != nil {
				return
			}
			ch <- c
		}
	}()
	return ch
}
