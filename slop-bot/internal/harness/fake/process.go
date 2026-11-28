package fake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
)

// Main is the fake harness process. It returns the exit code. Its modes:
//
//	fakeharness [--resume <id>]          the rpc protocol on stdin/stdout
//	fakeharness --tui [--resume <id>]    a TUI on the terminal
//	fakeharness --hook-relay <socket> <session> <event>
//	                                     a hook, relayed to a HookHub
func Main(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	if len(args) > 0 && args[0] == "--hook-relay" {
		return relayHook(args[1:], stdin, stdout)
	}
	tui := len(args) > 0 && args[0] == "--tui"
	if tui {
		args = args[1:]
	}
	id, resumed := getenv(EnvID), false
	if len(args) == 2 && args[0] == "--resume" {
		id, resumed = args[1], true
	}
	if id == "" {
		id = "fake-conversation"
	}
	rep := &reporter{}
	var cmds <-chan Command
	if sock := getenv(EnvControl); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			_, _ = fmt.Fprintln(stdout, `{"ev":"error"}`)
			return 70
		}
		defer func() { _ = conn.Close() }()
		rep.enc = json.NewEncoder(conn)
		cmds = readCommands(conn)
	}
	if tui {
		return runTUI(&tuiOptions{id: id, resumed: resumed, stdin: stdin, stdout: stdout, getenv: getenv, rep: rep, cmds: cmds})
	}
	f := &proc{out: json.NewEncoder(stdout), rep: rep, id: id}
	msgs := readStdin(stdin)
	rep.report(Report{Rep: RepStarted, ID: id, Resumed: resumed})
	return f.loop(msgs, cmds)
}

// reporter sends reports to the controller, when there is one.
type reporter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (r *reporter) report(rep Report) {
	if r.enc == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.enc.Encode(rep)
}

func (r *reporter) ack(cmd string) { r.report(Report{Rep: RepAck, Ack: cmd}) }

func (r *reporter) controlled() bool { return r.enc != nil }

// run runs a command as an agent's tool call would, and reports its result.
func (r *reporter) run(argv []string) {
	if len(argv) == 0 {
		r.report(Report{Rep: RepRan, Err: "no argv"})
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
		r.report(Report{Rep: RepRan, Err: err.Error()})
		return
	}
	r.report(Report{Rep: RepRan, Code: code, Stdout: stdout.String(), Stderr: stderr.String()})
}

func unknownCommand(r *reporter, cmd string) {
	r.report(Report{Rep: RepAck, Ack: cmd, Err: "unknown command " + strconv.Quote(cmd)})
}

type proc struct {
	mu  sync.Mutex
	out *json.Encoder
	rep *reporter
	id  string

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
				f.rep.report(Report{Rep: RepStdinClosed})
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
		f.rep.report(Report{Rep: RepUser, Text: m.Text, From: m.From, MidTurn: f.inTurn})
		if autonomous {
			f.write(outMsg{Ev: "turn_start"})
			f.write(outMsg{Ev: "text", Text: "echo: " + m.Text})
			f.write(outMsg{Ev: "turn_end", Outcome: "completed"})
		}
	case "interrupt":
		f.rep.report(Report{Rep: RepInterrupt})
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
		f.write(outMsg{Ev: "turn_end", Outcome: outcomeOr(c.Outcome)})
	case CmdToolStart:
		f.write(outMsg{Ev: "tool_start", Tool: c.Tool})
	case CmdToolEnd:
		f.write(outMsg{Ev: "tool_end", Tool: c.Tool})
	case CmdText:
		f.write(outMsg{Ev: "text", Text: c.Text})
	case CmdUsage:
		f.write(outMsg{Ev: "usage", Input: 100, Output: 10})
	case CmdRun:
		f.rep.run(c.Argv)
		return 0, false
	case CmdIgnoreInterrupts:
		f.ignoreInterrupts = c.On
	case CmdIgnoreEOF:
		f.ignoreEOF = c.On
	case CmdExit:
		f.rep.ack(c.Cmd)
		return c.Code, true
	default:
		unknownCommand(f.rep, c.Cmd)
		return 0, false
	}
	f.rep.ack(c.Cmd)
	return 0, false
}

func outcomeOr(outcome string) string {
	if outcome == "" {
		return "completed"
	}
	return outcome
}

func (f *proc) write(m outMsg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.out.Encode(m)
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
