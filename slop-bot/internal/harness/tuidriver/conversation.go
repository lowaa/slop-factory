package tuidriver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
)

// conversationTag is the session tag holding the conversation id, so Adopt
// recovers it from the session itself.
const conversationTag = "slop-bot-conversation"

// Defaults for Options' timings.
const (
	defaultPollInterval   = time.Second
	defaultConfirmTimeout = 5 * time.Second
	defaultRenderTimeout  = 5 * time.Second
	// renderPoll is how often a paste's rendering is checked.
	renderPoll = 10 * time.Millisecond
)

var (
	// ErrAttached and ErrUnsubmitted are the core's (see harness.Conversation).
	ErrAttached    = harness.ErrAttached
	ErrUnsubmitted = harness.ErrUnsubmitted
	// ErrUnconfirmed means keys were sent, retried, and the harness never
	// showed that it acted on them.
	ErrUnconfirmed = errors.New("tuidriver: the harness didn't confirm")
	// ErrClosed means the conversation was closed: the session lives on,
	// for Adopt, but this Conversation no longer drives it.
	ErrClosed = errors.New("tuidriver: the conversation was closed")
	errExited = errors.New("tuidriver: the session has exited")
)

// Options configure one conversation.
type Options struct {
	Profile *Profile
	Host    sessionhost.SessionHost
	Hub     HookHub
	// Session is the session's name, unique on Host.
	Session string
	// Pointer is the text typed in place of messages that can't be injected,
	// e.g. "2 new messages: run `slop-bot inbox`". The daemon composes it.
	Pointer func(msgs []*harness.Message) string
	Logger  *slog.Logger
	Clock   clock.Clock
	// PollInterval is how often the screen markers are read: the fallback
	// for what hooks don't say. A marker overrides the hooks' turn state
	// only after a PollInterval with no hook.
	PollInterval time.Duration
	// ConfirmTimeout is how long a submit, cancel or quit waits for the
	// harness to act before it is retried or given up on.
	ConfirmTimeout time.Duration
	// RenderTimeout bounds the wait for a paste to show on screen, and a
	// graceful Stop's wait for the idle prompt before it types the quit.
	RenderTimeout time.Duration
}

func (o *Options) normalised() (*Options, error) {
	n := *o
	if n.Profile == nil || n.Host == nil || n.Hub == nil || n.Pointer == nil || n.Session == "" {
		return nil, errors.New("tuidriver: Profile, Host, Hub, Pointer and Session are required")
	}
	if n.Profile.Markers == nil || n.Profile.ParseHook == nil || n.Profile.Reply == nil {
		return nil, fmt.Errorf("tuidriver: %s's profile needs Markers, ParseHook and Reply", n.Profile.Harness)
	}
	if n.Logger == nil {
		n.Logger = slog.New(slog.DiscardHandler)
	}
	n.Logger = n.Logger.With("harness", n.Profile.Harness, "session", n.Session)
	n.Clock = clock.Or(n.Clock)
	if n.PollInterval <= 0 {
		n.PollInterval = defaultPollInterval
	}
	if n.ConfirmTimeout <= 0 {
		n.ConfirmTimeout = defaultConfirmTimeout
	}
	if n.RenderTimeout <= 0 {
		n.RenderTimeout = defaultRenderTimeout
	}
	return &n, nil
}

// Conversation is a TUI session.
type Conversation struct {
	opts    *Options
	p       *Profile
	host    sessionhost.SessionHost
	events  chan harness.Event
	exited  chan struct{}
	unroute func()
	// bg runs the watchers; cancel ends them.
	bg     context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// keys serialises everything that types: a paste and its submit can't
	// interleave with a cancel or another paste.
	keys sync.Mutex

	// emitMu orders state changes with the events they emit, across the
	// hook, screen and exit sources. It is taken before mu.
	emitMu       sync.Mutex
	eventsClosed bool

	mu sync.Mutex
	id string
	// tagged is the id last recorded in the session's tag, for Adopt.
	tagged string
	ready  bool
	// readyCh closes once ready.
	readyCh chan struct{}
	inTurn  bool
	// announced says TurnStarted went out for the running turn, which may
	// have been opened by a submit before the harness reported it.
	announced bool
	// turnsStarted counts announced turns, so a submit can tell whether its
	// turn has come and gone already.
	turnsStarted uint64
	// settled is closed and replaced whenever a turn ends, so Stop and
	// Interrupt can wait for "not in a turn".
	settled chan struct{}
	// submitted is closed and replaced whenever the harness takes a prompt.
	submitted chan struct{}
	// pending are messages waiting for an injecting hook, or for idle.
	pending []*harness.Message
	// flushing means a flush is running or about to: one at a time.
	flushing bool
	// hookGen counts hooks, and lastHook is when the last arrived: a screen
	// read that overlaps a hook, or follows it closely, is stale.
	hookGen      uint64
	lastHook     time.Time
	interrupting bool
	stopping     bool
	killed       bool
	closing      bool
	exitErr      error

	finishOnce sync.Once
	closeOnce  sync.Once
}

func newConversation(opts *Options) *Conversation {
	bg, cancel := context.WithCancel(context.Background())
	c := &Conversation{
		opts: opts, p: opts.Profile, host: opts.Host,
		events:    make(chan harness.Event, 64),
		exited:    make(chan struct{}),
		bg:        bg,
		cancel:    cancel,
		readyCh:   make(chan struct{}),
		settled:   make(chan struct{}),
		submitted: make(chan struct{}),
		unroute:   func() {},
	}
	close(c.settled) // not in a turn yet
	return c
}

// Start launches a session, resuming resume's conversation when it is
// non-nil, and returns once the harness is ready for a message.
func Start(ctx context.Context, opts *Options, ws *harness.Workspace, resume *harness.ConversationRef) (*Conversation, error) {
	o, err := opts.normalised()
	if err != nil {
		return nil, err
	}
	spec, err := o.Profile.Command(ws, resume)
	if err != nil {
		return nil, err
	}
	if o.Profile.InstallHooks != nil {
		if err := o.Profile.InstallHooks(ws, spec, o.Hub.Command(o.Session)); err != nil {
			return nil, fmt.Errorf("tuidriver: installing %s's hooks: %w", o.Profile.Harness, err)
		}
	}
	c := newConversation(o)
	if resume != nil {
		c.id = resume.ID
	}
	// Routed before the session starts, so its first hook isn't missed.
	if err := c.route(); err != nil {
		c.cancel()
		return nil, err
	}
	err = o.Host.Start(ctx, &sessionhost.StartSpec{
		Name: o.Session, Argv: append([]string{spec.Path}, spec.Args...), Env: spec.Env, Dir: spec.Dir,
	})
	if err != nil {
		c.unroute()
		c.cancel()
		return nil, err
	}
	// A resumed id is known now, and its hooks may never repeat it.
	c.recordID(ctx)
	c.watch()
	if err := c.awaitReady(ctx); err != nil {
		_ = c.kill(c.boundedContext())
		return nil, err
	}
	return c, nil
}

// recordID tags the session with the conversation id, if it changed since
// the last successful tag, so Adopt reads it back after a restart.
func (c *Conversation) recordID(ctx context.Context) {
	c.mu.Lock()
	id, tagged := c.id, c.tagged
	c.mu.Unlock()
	if id == "" || id == tagged {
		return
	}
	if err := c.host.SetTag(ctx, c.opts.Session, conversationTag, id); err != nil {
		c.opts.Logger.Warn("tuidriver: recording the conversation id", "err", err)
		return
	}
	c.mu.Lock()
	c.tagged = id
	c.mu.Unlock()
}

// boundedContext bounds the kill a failed or abandoned operation falls back
// to, when the caller's context is already spent.
func (c *Conversation) boundedContext() context.Context {
	ctx, cancel := clock.WithDeadline(context.Background(), c.opts.Clock, c.opts.Clock.Now().Add(c.opts.ConfirmTimeout))
	context.AfterFunc(ctx, cancel)
	return ctx
}

// Adopt takes over a session that outlived the daemon that started it, by
// its name (opts.Session). Its state is read off the screen: it starts with
// Ready, then TurnStarted if a turn is running.
func Adopt(ctx context.Context, opts *Options) (*Conversation, error) {
	o, err := opts.normalised()
	if err != nil {
		return nil, err
	}
	if _, err := o.Host.Status(ctx, o.Session); err != nil {
		return nil, err
	}
	id, err := o.Host.Tag(ctx, o.Session, conversationTag)
	if err != nil {
		return nil, err
	}
	screen, err := o.Host.Capture(ctx, o.Session)
	if err != nil {
		return nil, err
	}
	c := newConversation(o)
	c.id = id
	if err := c.route(); err != nil {
		c.cancel()
		return nil, err
	}
	c.apply(func() []harness.Event {
		evs := []harness.Event{c.markReady()}
		if o.Profile.Markers.classify(screen) == screenBusy {
			evs = append(evs, c.turnStarted()...)
		}
		return evs
	})
	c.watch()
	return c, nil
}

func (c *Conversation) route() error {
	unroute, err := c.opts.Hub.Route(c.opts.Session, c.hook)
	if err != nil {
		return fmt.Errorf("tuidriver: routing %s's hooks: %w", c.opts.Session, err)
	}
	var once sync.Once
	c.unroute = func() { once.Do(unroute) }
	return nil
}

// watch starts the exit and screen watchers.
func (c *Conversation) watch() {
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.watchExit()
	}()
	go func() {
		defer c.wg.Done()
		c.watchScreen()
	}()
}

func (c *Conversation) awaitReady(ctx context.Context) error {
	select {
	case <-c.readyCh:
		return nil
	case <-c.exited:
		// exitErr is set before exited closes, and nil for a clean exit.
		if c.exitErr == nil {
			return fmt.Errorf("tuidriver: %s exited before it was ready", c.p.Harness)
		}
		return fmt.Errorf("tuidriver: %s exited before it was ready: %w", c.p.Harness, c.exitErr)
	case <-ctx.Done():
		screen, _ := c.host.Capture(context.Background(), c.opts.Session)
		return fmt.Errorf("tuidriver: %s not ready: %w (screen: %s)", c.p.Harness, ctx.Err(), lastLine(screen))
	}
}

// apply runs a state change and emits the events it returns, in order with
// every other source's.
func (c *Conversation) apply(change func() []harness.Event) {
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	c.mu.Lock()
	evs := change()
	c.mu.Unlock()
	if c.eventsClosed {
		return
	}
	// Sending blocks when the consumer falls behind: back-pressure onto
	// the hooks rather than dropped events.
	for _, ev := range evs {
		if ev.At.IsZero() {
			ev.At = c.opts.Clock.Now()
		}
		c.events <- ev
	}
}

// The state transitions; c.mu is held.

func (c *Conversation) markReady() harness.Event {
	if !c.ready {
		c.ready = true
		close(c.readyCh)
	}
	return harness.Event{Kind: harness.Ready}
}

// openTurn marks a turn running without announcing it: the harness took a
// prompt we typed, and its own turn start may still be on the way. A
// graceful Stop landing in between would otherwise see no turn and quit
// over it.
func (c *Conversation) openTurn() {
	if !c.inTurn {
		c.inTurn = true
		c.settled = make(chan struct{})
	}
}

func (c *Conversation) turnStarted() []harness.Event {
	if c.announced {
		return nil
	}
	c.openTurn()
	c.announced = true
	c.turnsStarted++
	c.signalSubmitted()
	return []harness.Event{{Kind: harness.TurnStarted}}
}

func (c *Conversation) turnEnded(outcome harness.TurnOutcome) []harness.Event {
	if !c.inTurn {
		return nil
	}
	c.inTurn = false
	close(c.settled)
	announced := c.announced
	c.announced = false
	if outcome == "" {
		outcome = harness.TurnCompleted
	}
	if len(c.pending) > 0 {
		c.spawnFlush()
	}
	// A turn opened for a prompt that never started one ends quietly: a
	// TurnEnded without its TurnStarted would mislead the core.
	if !announced {
		return nil
	}
	return []harness.Event{{Kind: harness.TurnEnded, Outcome: outcome}}
}

func (c *Conversation) signalSubmitted() {
	close(c.submitted)
	c.submitted = make(chan struct{})
}

// spawnFlush types the pointer for pending messages, once idle.
func (c *Conversation) spawnFlush() {
	if c.closing || c.stopping || c.flushing {
		return
	}
	c.flushing = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.flush(c.bg)
	}()
}

// hook handles one hook invocation, routed from the HookHub.
func (c *Conversation) hook(ctx context.Context, event string, payload []byte) ([]byte, error) {
	call, err := c.p.ParseHook(event, payload)
	if err != nil {
		// Still answer: a harness may read no answer as a refusal.
		c.opts.Logger.Warn("tuidriver: unreadable hook payload", "event", event, "err", err)
		call = &HookCall{}
	}
	call.Event = event
	var inject []*harness.Message
	c.apply(func() []harness.Event {
		c.hookGen++
		c.lastHook = c.opts.Clock.Now()
		if call.ConversationID != "" {
			c.id = call.ConversationID
		}
		if call.Submitted {
			c.signalSubmitted()
		}
		// Not into a turn being cancelled: kept going, it would carry the
		// message past the cancel, which then cancels it too.
		cancelling := call.Inject == InjectTurnEnd && c.interrupting
		if c.p.Injects && call.Inject != InjectNone && !c.stopping && !cancelling && len(c.pending) > 0 {
			inject, c.pending = c.pending, nil
		}
		return c.hookEvents(call, len(inject) > 0)
	})
	// Also retries a tag that failed before.
	c.recordID(ctx)
	reply, err := c.p.Reply(call, inject)
	if err != nil && len(inject) > 0 {
		// Not delivered: hold them again. A turn end this hook would have
		// continued is then left to the screen to report.
		c.mu.Lock()
		c.pending = append(inject, c.pending...)
		c.mu.Unlock()
	}
	return reply, err
}

// hookEvents applies a hook's events to the turn state; c.mu is held.
func (c *Conversation) hookEvents(call *HookCall, injected bool) []harness.Event {
	var evs []harness.Event
	for _, ev := range call.Events {
		if !c.ready && ev.Kind != harness.Ready {
			continue // start-up work (see HookCall.Events)
		}
		switch ev.Kind {
		case harness.Ready:
			if c.id == "" {
				c.opts.Logger.Warn("tuidriver: ready without a conversation id", "event", call.Event)
				continue
			}
			if !c.ready {
				evs = append(evs, c.markReady())
			}
		case harness.TurnStarted:
			evs = append(evs, c.turnStarted()...)
		case harness.TurnEnded:
			if call.Inject == InjectTurnEnd && injected {
				continue // the reply keeps the turn going
			}
			evs = append(evs, c.turnEnded(ev.Outcome)...)
		default:
			evs = append(evs, ev)
		}
	}
	return evs
}

// watchScreen polls the screen markers: the fallback for state the hooks
// don't report, such as a cancel on agy.
func (c *Conversation) watchScreen() {
	tick := c.opts.Clock.NewTicker(c.opts.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.bg.Done():
			return
		case <-tick.C():
			c.readScreen(c.bg)
		}
	}
}

func (c *Conversation) readScreen(ctx context.Context) {
	c.mu.Lock()
	gen := c.hookGen
	c.mu.Unlock()
	screen, err := c.host.Capture(ctx, c.opts.Session)
	if err != nil {
		return // the exit watcher reports a gone session
	}
	st := c.p.Markers.classify(screen)
	cancelled := c.p.Markers.interrupted(screen)
	if st == screenUnknown && cancelled {
		// A cancel can hide the idle footer (agy moves a queued message
		// into the prompt): the cancel itself says the turn is over.
		st = screenIdle
	}
	c.apply(func() []harness.Event {
		// A hook during the read, or just before it, may not have reached
		// the screen yet: the hook is the better witness.
		if st == screenUnknown || c.hookGen != gen || c.opts.Clock.Now().Sub(c.lastHook) < c.opts.PollInterval {
			return nil
		}
		return c.screenEvents(st, cancelled)
	})
	c.retryHeld()
}

// screenEvents applies what the screen shows to the turn state; c.mu is
// held.
func (c *Conversation) screenEvents(st screenState, cancelled bool) []harness.Event {
	switch st {
	case screenBusy:
		if !c.ready {
			return nil // start-up work (see HookCall.Events)
		}
		return c.turnStarted()
	case screenIdle:
		var evs []harness.Event
		if !c.ready && c.id != "" {
			evs = append(evs, c.markReady())
		}
		outcome := harness.TurnCompleted
		if c.interrupting || cancelled {
			outcome = harness.TurnInterrupted
		}
		return append(evs, c.turnEnded(outcome)...)
	}
	return nil
}

// retryHeld retries held messages once idle, on every screen poll: a
// flush that typed nothing (a person attached, tmux failed) leaves them for
// it, and an idle agent has no turn end to trigger another.
func (c *Conversation) retryHeld() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inTurn && len(c.pending) > 0 {
		c.spawnFlush()
	}
}

// watchExit reports the session's end: its process's death, or the session
// going away.
func (c *Conversation) watchExit() {
	for {
		st, err := c.host.Status(c.bg, c.opts.Session)
		switch {
		case c.bg.Err() != nil:
			return // closed: the session lives on without us
		case errors.Is(err, sessionhost.ErrNoSession) || errors.Is(err, sessionhost.ErrNoServer):
			c.finish(c.goneStatus(err))
			return
		case err != nil:
			// Not evidence the session is gone (tmux failed to run, say):
			// reporting an exit would have the core resume a conversation
			// that is still running.
			c.opts.Logger.Warn("tuidriver: reading the session's status", "err", err)
			c.backOff()
			continue
		case st.Dead:
			c.reportDeath(st)
			return
		}
		// It may return early (a signal kept from an earlier session of the
		// same name); the loop checks again.
		if err := c.host.WaitExit(c.bg, c.opts.Session); err != nil && c.bg.Err() == nil {
			c.opts.Logger.Warn("tuidriver: waiting for the session's exit", "err", err)
			c.backOff()
		}
	}
}

// backOff waits a poll interval, or until closed, before the exit watcher
// tries a failing call again.
func (c *Conversation) backOff() {
	select {
	case <-c.bg.Done():
	case <-c.opts.Clock.After(c.opts.PollInterval):
	}
}

func (c *Conversation) reportDeath(st *sessionhost.Status) {
	screen, _ := c.host.Capture(c.bg, c.opts.Session)
	var exitErr error
	if st.ExitStatus != 0 {
		exitErr = fmt.Errorf("%s exited with status %d: %s", c.p.Harness, st.ExitStatus, lastLine(screen))
	}
	// The dead pane was kept only for its status.
	if err := c.host.Kill(c.bg, c.opts.Session); err != nil && !errors.Is(err, sessionhost.ErrNoSession) {
		c.opts.Logger.Warn("tuidriver: removing the dead session", "err", err)
	}
	c.finish(st.ExitStatus, exitErr)
}

func (c *Conversation) goneStatus(err error) (int, error) {
	c.mu.Lock()
	killed := c.killed
	c.mu.Unlock()
	// kill-session hangs the pane up.
	code := 128 + int(syscall.SIGHUP)
	if killed {
		return code, fmt.Errorf("%s session killed", c.p.Harness)
	}
	return code, fmt.Errorf("%s session went away: %w", c.p.Harness, err)
}

// finish reports the exit, last, and closes Events.
func (c *Conversation) finish(code int, exitErr error) {
	c.finishOnce.Do(func() {
		c.unroute()
		c.emitMu.Lock()
		c.mu.Lock()
		if c.inTurn {
			c.inTurn = false
			close(c.settled)
		}
		c.announced = false
		c.exitErr = exitErr
		c.mu.Unlock()
		if !c.eventsClosed {
			c.events <- harness.Event{Kind: harness.Exited, At: c.opts.Clock.Now(), ExitCode: code, Err: exitErr}
			c.eventsClosed = true
			close(c.events)
		}
		c.emitMu.Unlock()
		close(c.exited)
		c.cancel()
	})
}

// Ref implements harness.Conversation.
func (c *Conversation) Ref() harness.ConversationRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return harness.ConversationRef{Harness: c.p.Harness, Driver: Driver, ID: c.id}
}

// Events implements harness.Conversation.
func (c *Conversation) Events() <-chan harness.Event { return c.events }

// Exited is closed once the session has gone and Events has closed.
func (c *Conversation) Exited() <-chan struct{} { return c.exited }

// Send implements harness.Conversation. Mid-turn, the message waits for a
// hook that can inject it, or else for the turn to end, and Send returns at
// once. Idle, Send types the pointer and returns once the harness took it,
// or ErrAttached while a person is attached (nothing typed: send it later),
// or ErrUnsubmitted if one attached mid-delivery (the pointer is in their
// prompt: don't send it again).
func (c *Conversation) Send(ctx context.Context, msg *harness.Message) error {
	if err := c.usable(); err != nil {
		return err
	}
	c.keys.Lock()
	defer c.keys.Unlock()
	c.mu.Lock()
	if c.inTurn {
		c.pending = append(c.pending, msg)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	_, err := c.typePointer(ctx, []*harness.Message{msg})
	return err
}

// usable says whether the conversation still drives its session.
func (c *Conversation) usable() error {
	select {
	case <-c.exited:
		return errExited
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return ErrClosed
	}
	return nil
}

// flush types the pointer for the pending messages, if the harness is idle.
func (c *Conversation) flush(ctx context.Context) {
	c.keys.Lock()
	defer c.keys.Unlock()
	c.mu.Lock()
	msgs := c.pending
	if c.inTurn || c.stopping || len(msgs) == 0 {
		c.flushing = false
		c.mu.Unlock()
		return
	}
	c.pending = nil
	c.mu.Unlock()
	pasted, err := c.typePointer(ctx, msgs)
	c.mu.Lock()
	// Before the messages are back, so the next poll can flush them again.
	c.flushing = false
	if err != nil && !pasted {
		// Nothing was typed, so trying again later can't double anything.
		c.pending = append(msgs, c.pending...)
	}
	c.mu.Unlock()
	switch {
	case err == nil:
	case !pasted:
		c.opts.Logger.Info("tuidriver: holding messages", "count", len(msgs), "err", err)
	default:
		// Not retried: the pointer is in the prompt, and a second paste
		// would double it. The messages stay in the inbox it names.
		c.opts.Logger.Warn("tuidriver: dropped held messages", "count", len(msgs), "err", err)
	}
}

// typePointer pastes the pointer for msgs, waits for it to show, then
// submits it, retrying the submit once. pasted says the pointer reached the
// prompt. c.keys is held.
func (c *Conversation) typePointer(ctx context.Context, msgs []*harness.Message) (pasted bool, err error) {
	if err := c.notAttached(ctx); err != nil {
		return false, err
	}
	text := c.opts.Pointer(msgs)
	before, err := c.host.Capture(ctx, c.opts.Session)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	submitted, started := c.submitted, c.turnsStarted
	c.mu.Unlock()
	if err := c.host.Paste(ctx, c.opts.Session, text); err != nil {
		return false, err
	}
	err = c.awaitRendered(ctx, before, text)
	if err == nil {
		err = c.confirmed(ctx, c.p.SubmitKey, submitted)
	}
	switch {
	case errors.Is(err, ErrAttached):
		// Not ErrAttached itself, which promises nothing was typed.
		return true, ErrUnsubmitted
	case err != nil:
		// Whatever failed, the pointer may be in the prompt.
		return true, fmt.Errorf("%w: %w", ErrUnsubmitted, err)
	}
	c.mu.Lock()
	// Unless its turn already started (and maybe ended) meanwhile.
	if c.turnsStarted == started {
		c.openTurn()
	}
	c.mu.Unlock()
	return true, nil
}

// confirmed presses key until done closes: twice at most, because the
// first press can be swallowed (the Enter race: a TUI still handling the
// paste drops it).
func (c *Conversation) confirmed(ctx context.Context, key string, done <-chan struct{}) error {
	for attempt := 1; ; attempt++ {
		if err := c.notAttached(ctx); err != nil {
			return err
		}
		if err := c.host.SendKeys(ctx, c.opts.Session, key); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		case <-c.exited:
			return errExited
		case <-c.bg.Done():
			return c.ended()
		case <-ctx.Done():
			return ctx.Err()
		case <-c.opts.Clock.After(c.opts.ConfirmTimeout):
		}
		if attempt == 2 {
			return fmt.Errorf("%w: %s", ErrUnconfirmed, key)
		}
		c.opts.Logger.Info("tuidriver: unconfirmed, pressing again", "key", key)
	}
}

func (c *Conversation) notAttached(ctx context.Context) error {
	st, err := c.host.Status(ctx, c.opts.Session)
	if err != nil {
		return err
	}
	if st.Attached > 0 {
		return ErrAttached
	}
	return nil
}

// awaitRendered waits for text to show on screen, so the submit doesn't
// overtake the paste. Nothing signals a redraw, so this polls; it runs on
// the wall clock, not the injected one, because it is a sub-second wait on
// tmux's redraw that no test needs to move.
func (c *Conversation) awaitRendered(ctx context.Context, before, text string) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RenderTimeout)
	defer cancel()
	tick := time.NewTicker(renderPoll)
	defer tick.Stop()
	for {
		screen, err := c.host.Capture(ctx, c.opts.Session)
		if err == nil && c.p.rendered(before, screen, text) {
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("tuidriver: the paste never showed: %w (screen: %s)", ctx.Err(), lastLine(screen))
		}
	}
}

// Interrupt implements harness.Conversation: the cancel key, confirmed by
// the turn ending, from a hook or the screen. Idle, there is nothing to
// interrupt.
func (c *Conversation) Interrupt(ctx context.Context) error {
	if err := c.usable(); err != nil {
		if errors.Is(err, errExited) {
			return nil
		}
		return err
	}
	c.keys.Lock()
	defer c.keys.Unlock()
	c.mu.Lock()
	if !c.inTurn {
		c.mu.Unlock()
		return nil
	}
	settled := c.settled
	c.interrupting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.interrupting = false
		c.mu.Unlock()
	}()
	err := c.confirmed(ctx, c.p.CancelKey, settled)
	if errors.Is(err, errExited) {
		return nil
	}
	return err
}

// Stop implements harness.Conversation: after the turn settles, the
// profile's quit sequence, confirmed by the pane dying; with force, or when
// that fails, the session is killed. Without force it won't type while a
// person is attached: it returns ErrAttached, and the session runs on.
func (c *Conversation) Stop(ctx context.Context, force bool) error {
	if err := c.usable(); err != nil {
		if errors.Is(err, errExited) {
			return nil
		}
		return err
	}
	if force {
		return c.kill(ctx)
	}
	for quitting := false; !quitting; {
		c.mu.Lock()
		settled := c.settled
		c.mu.Unlock()
		select {
		case <-settled:
		case <-c.exited:
			return nil
		case <-c.bg.Done():
			return stopped(c.ended())
		case <-ctx.Done():
			return c.kill(c.boundedContext())
		}
		quitting = c.beginQuit()
	}
	err := c.quit(ctx)
	c.keys.Unlock()
	if errors.Is(err, ErrAttached) {
		c.mu.Lock()
		c.stopping = false
		c.mu.Unlock()
		return err
	}
	if err != nil {
		c.opts.Logger.Warn("tuidriver: quitting", "err", err)
		return c.kill(c.boundedContext())
	}
	select {
	case <-c.exited:
		return nil
	case <-c.bg.Done():
		return stopped(c.ended())
	case <-ctx.Done():
	case <-c.opts.Clock.After(c.opts.ConfirmTimeout):
		c.opts.Logger.Warn("tuidriver: the harness didn't quit; killing it")
	}
	return c.kill(c.boundedContext())
}

// ended is why a wait ended with bg: the session exited, or Close let go of
// it.
func (c *Conversation) ended() error {
	select {
	case <-c.exited:
		return errExited
	default:
		return ErrClosed
	}
}

// stopped is Stop's result for ended's: an exit is what Stop wanted.
func stopped(err error) error {
	if errors.Is(err, errExited) {
		return nil
	}
	return err
}

// beginQuit takes the keys for the quit sequence, unless a turn started
// meanwhile (a held message's pointer, typed as the last turn ended): then
// Stop waits for that one too. On true, the caller holds c.keys.
func (c *Conversation) beginQuit() bool {
	c.keys.Lock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inTurn {
		c.keys.Unlock()
		return false
	}
	c.stopping = true
	if len(c.pending) > 0 {
		c.opts.Logger.Warn("tuidriver: stopping with undelivered messages", "count", len(c.pending))
	}
	return true
}

// quit types the quit sequence; c.keys is held.
func (c *Conversation) quit(ctx context.Context) error {
	if err := c.notAttached(ctx); err != nil {
		return err // not delayed: a person's typing can keep the screen busy
	}
	c.awaitIdleScreen(ctx)
	if err := c.usable(); err != nil {
		return err // closed or exited during the wait: type nothing
	}
	for _, in := range c.p.Quit {
		if err := c.notAttached(ctx); err != nil {
			return err
		}
		if in.Text != "" {
			if err := c.host.SendText(ctx, c.opts.Session, in.Text); err != nil {
				return err
			}
		}
		if len(in.Keys) > 0 {
			if err := c.host.SendKeys(ctx, c.opts.Session, in.Keys...); err != nil {
				return err
			}
		}
	}
	return nil
}

// awaitIdleScreen waits, up to RenderTimeout, for the screen to show the
// idle prompt. A hook's turn end can come before the TUI has finished the
// turn (agy runs Stop with its turn still on screen), and the quit sequence
// is meant for the idle prompt: mid-turn, a TUI can read the same keys
// otherwise. Past the timeout the quit is typed anyway, and Stop's confirm
// or kill takes over. Like awaitRendered, it polls on the wall clock: a
// sub-second wait on the TUI's redraw.
func (c *Conversation) awaitIdleScreen(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RenderTimeout)
	defer cancel()
	tick := time.NewTicker(renderPoll)
	defer tick.Stop()
	for {
		screen, err := c.host.Capture(ctx, c.opts.Session)
		if err == nil && c.p.Markers.classify(screen) == screenIdle {
			return
		}
		select {
		case <-tick.C:
		case <-c.bg.Done():
			return // closed or exited
		case <-ctx.Done():
			c.opts.Logger.Info("tuidriver: quitting without an idle screen", "screen", lastLine(screen))
			return
		}
	}
}

// kill ends the session and waits for the exit to be reported.
func (c *Conversation) kill(ctx context.Context) error {
	c.mu.Lock()
	if c.closing {
		// Close promised the session lives on, for Adopt.
		c.mu.Unlock()
		return ErrClosed
	}
	c.killed = true
	c.mu.Unlock()
	if err := c.host.Kill(ctx, c.opts.Session); err != nil && !errors.Is(err, sessionhost.ErrNoSession) {
		select {
		case <-c.exited:
			return nil
		default:
		}
		return err
	}
	select {
	case <-c.exited:
		return nil
	case <-c.bg.Done():
		return stopped(c.ended())
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AttachPlan implements harness.Conversation: a person joins the session
// itself.
func (c *Conversation) AttachPlan(context.Context) (*harness.AttachPlan, error) {
	argv := c.host.AttachCommand(c.opts.Session)
	return &harness.AttachPlan{Kind: harness.Join, Command: &harness.LaunchSpec{Path: argv[0], Args: argv[1:]}}, nil
}

// Close lets go of the session without ending it, as a daemon shutting down
// does: the session lives on for Adopt. Events closes with no Exited.
func (c *Conversation) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		c.unroute()
		c.cancel()
		// A hook in flight may be blocked handing an event to a consumer
		// that has stopped reading; drain, so it, and so Close, can finish.
		// Not once exited: then nothing is blocked, and the drain would take
		// the consumer's Exited.
		select {
		case <-c.exited:
		default:
			go func() {
				for range c.events {
				}
			}()
		}
		c.wg.Wait()
		c.emitMu.Lock()
		if !c.eventsClosed {
			c.eventsClosed = true
			close(c.events)
		}
		c.emitMu.Unlock()
	})
	return nil
}

func lastLine(screen string) string {
	lines := strings.Split(strings.TrimRight(screen, " \n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
