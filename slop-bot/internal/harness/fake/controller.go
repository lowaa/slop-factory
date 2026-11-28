package fake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Controller is the test's side of every fake harness a test starts: each
// fake connects to it and becomes a Session.
type Controller struct {
	l        net.Listener
	dir      string
	sessions chan *Session
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
	// closed ends readers stuck handing on reports nobody reads.
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewController listens on a fresh socket. Its path is short: socket paths
// have a length limit that test temp dirs can exceed.
func NewController() (*Controller, error) {
	dir, err := os.MkdirTemp("", "fakectl")
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", filepath.Join(dir, "c.sock"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	c := &Controller{l: l, dir: dir, sessions: make(chan *Session, 16), closed: make(chan struct{})}
	c.wg.Add(1)
	go c.accept()
	return c, nil
}

// Socket is what the fake harness is pointed at (EnvControl).
func (c *Controller) Socket() string { return c.l.Addr().String() }

func (c *Controller) accept() {
	defer c.wg.Done()
	for {
		conn, err := c.l.Accept()
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns = append(c.conns, conn)
		c.mu.Unlock()
		s := &Session{conn: conn, enc: json.NewEncoder(conn), reports: make(chan Report, 64)}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			s.readReports(c.sessions, c.closed)
		}()
	}
}

// Next waits for the next fake to start.
func (c *Controller) Next(ctx context.Context) (*Session, error) {
	select {
	case s := <-c.sessions:
		return s, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("fake: no harness started: %w", ctx.Err())
	}
}

// Close stops listening and disconnects every fake, which makes them exit.
// Calling it again returns the first call's result.
func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		err := c.l.Close()
		close(c.closed)
		c.mu.Lock()
		for _, conn := range c.conns {
			_ = conn.Close()
		}
		c.mu.Unlock()
		c.wg.Wait()
		c.closeErr = errors.Join(err, os.RemoveAll(c.dir))
	})
	return c.closeErr
}

// Session is one running fake harness.
type Session struct {
	conn    net.Conn
	enc     *json.Encoder
	mu      sync.Mutex
	reports chan Report
	// ID, Resumed and TUI come from the fake's started report.
	ID      string
	Resumed bool
	TUI     bool
}

func (s *Session) readReports(started chan<- *Session, closed <-chan struct{}) {
	defer func() { _ = s.conn.Close() }()
	defer close(s.reports)
	sc := bufio.NewScanner(s.conn)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	first := true
	for sc.Scan() {
		var r Report
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if first && r.Rep == RepStarted {
			first = false
			s.ID, s.Resumed, s.TUI = r.ID, r.Resumed, r.TUI
			select {
			case started <- s:
			case <-closed:
				return
			}
			continue
		}
		select {
		case s.reports <- r:
		case <-closed:
			return
		}
	}
}

// Do sends a command and waits for the fake to acknowledge it (or, for run,
// to report the result, which it returns).
func (s *Session) Do(ctx context.Context, c Command) (*Report, error) {
	s.mu.Lock()
	err := s.enc.Encode(c)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	want := RepAck
	if c.Cmd == CmdRun {
		want = RepRan
	}
	r, err := s.Expect(ctx, want)
	if err != nil {
		return nil, err
	}
	if r.Err != "" {
		return r, fmt.Errorf("fake: %s: %s", c.Cmd, r.Err)
	}
	return r, nil
}

// Expect waits for the next report of kind rep. Reports of other kinds that
// arrive first are an error: a test states what it expects, in order.
func (s *Session) Expect(ctx context.Context, rep string) (*Report, error) {
	select {
	case r, ok := <-s.reports:
		if !ok {
			return nil, fmt.Errorf("fake: session ended waiting for %s", rep)
		}
		if r.Rep != rep {
			return &r, fmt.Errorf("fake: got report %q (%+v), want %q", r.Rep, r, rep)
		}
		return &r, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("fake: waiting for %s: %w", rep, ctx.Err())
	}
}
