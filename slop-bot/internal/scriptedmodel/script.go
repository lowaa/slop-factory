package scriptedmodel

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

// ErrUnexpectedRequest is what every unexpected-request failure wraps. Its
// message carries a summary of the request.
var ErrUnexpectedRequest = errors.New("unexpected request")

// A gate holds a turn until it is opened. Once open it stays open, so a test
// may release a gate before the request that waits on it arrives. A gate
// belongs to the scenario loaded when it was made: replacing the scenario
// opens and retires it, and the next scenario's gate of that name is new.
type gate struct {
	opened   chan struct{}
	openOnce sync.Once
	held     chan struct{}
	heldOnce sync.Once
	retired  chan struct{}
}

func newGate() *gate {
	return &gate{opened: make(chan struct{}), held: make(chan struct{}), retired: make(chan struct{})}
}

func (g *gate) open()     { g.openOnce.Do(func() { close(g.opened) }) }
func (g *gate) markHeld() { g.heldOnce.Do(func() { close(g.held) }) }

func (g *gate) isOpen() bool {
	select {
	case <-g.opened:
		return true
	default:
		return false
	}
}

// script hands out a scenario's turns and records what it was asked. It is
// API-independent: an adapter turns a wire request into a Request and a Turn
// back into a wire response; this decides which Turn.
type script struct {
	mu        sync.Mutex
	summarise func(*Request) string
	scenario  *Scenario
	next      int
	recording Recording
	gates     map[string]*gate
}

func newScript(summarise func(*Request) string) *script {
	return &script{summarise: summarise, gates: map[string]*gate{}}
}

// gateLocked must be called with mu held.
func (s *script) gateLocked(name string) *gate {
	g, ok := s.gates[name]
	if !ok {
		g = newGate()
		s.gates[name] = g
	}
	return g
}

func (s *script) gate(name string) *gate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gateLocked(name)
}

// replace swaps in a scenario (nil to reset). A request still held at a gate
// belongs to the scenario being replaced: it is let go now rather than left
// to time out into the next one's errors.
func (s *script) replace(scenario *Scenario) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.gates {
		g.open()
		close(g.retired)
	}
	s.gates = map[string]*gate{}
	s.scenario = scenario
	s.next = 0
	s.recording = Recording{}
}

// answer records the request and returns its turn, nil for an auxiliary
// request, and the gate the turn waits at. The gate is taken in the same step,
// so a scenario replaced in between cannot hand the request a gate of the next.
func (s *script) answer(req *Request) (*Turn, *gate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recording.Requests = append(s.recording.Requests, req)
	if req.Auxiliary {
		return nil, nil, nil
	}
	if s.scenario == nil {
		return nil, nil, s.failLocked("no scenario is loaded", req)
	}
	if s.next >= len(s.scenario.Turns) {
		return nil, nil, s.failLocked(fmt.Sprintf("scenario %q has only %d turn(s), and the harness asked for another",
			s.scenario.Name, len(s.scenario.Turns)), req)
	}
	turn := s.scenario.Turns[s.next]
	var missing []string
	for _, call := range turn.ToolCalls {
		if !req.HasTool(call.Name) {
			missing = append(missing, call.Name)
		}
	}
	if len(missing) > 0 {
		return nil, nil, s.failLocked(fmt.Sprintf("turn %d of scenario %q calls tool(s) %q that the harness did not offer",
			s.next+1, s.scenario.Name, missing), req)
	}
	s.next++
	if turn.Gate == "" {
		return turn, nil, nil
	}
	return turn, s.gateLocked(turn.Gate), nil
}

func (s *script) fail(message string, req *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failLocked(message, req)
}

func (s *script) failLocked(message string, req *Request) error {
	detail := message
	if req != nil {
		detail = message + "\n" + s.summarise(req)
	}
	s.recording.Errors = append(s.recording.Errors, detail)
	return fmt.Errorf("%w: %s", ErrUnexpectedRequest, detail)
}

func (s *script) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scenario == nil {
		return 0
	}
	return len(s.scenario.Turns) - s.next
}

// snapshot copies the recording, so a caller can read it while requests are
// still arriving.
func (s *script) snapshot() *Recording {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &Recording{Requests: slices.Clone(s.recording.Requests), Errors: slices.Clone(s.recording.Errors)}
}
