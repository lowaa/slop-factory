// Package settings holds the server-wide switches the human flips at
// runtime.
//
// It is deliberately not internal/config. Config is read once from the
// environment at startup and a change to it needs a restart; a setting has
// to take effect while the fleet is running and survive the restart it
// might cause. DATABASE_URL belongs in one, "should agents start new work"
// in the other.
//
// Every setting the server knows about is declared in the registry below,
// and that declaration is the only place its type and its default live. A
// missing row means the default, so a fresh database and one with no
// history for a setting behave identically.
package settings

import (
	"errors"
	"fmt"
	"sync"
	"time"

	gosettings "github.com/lowaa/slop-factory/go-settings"
)

// The value model is shared with every other settings scope; these are the
// same types under this package's names.
type (
	Type  = gosettings.Type
	Value = gosettings.Value
)

const (
	TypeStr   = gosettings.TypeStr
	TypeInt   = gosettings.TypeInt
	TypeBool  = gosettings.TypeBool
	TypeFloat = gosettings.TypeFloat
)

var (
	StrValue   = gosettings.StrValue
	IntValue   = gosettings.IntValue
	BoolValue  = gosettings.BoolValue
	FloatValue = gosettings.FloatValue
)

// TicketProcessingEnabled gates whether the fleet starts *new* work.
//
// Off, the Stop hook stops telling idle agents to go claim something and
// the claim path itself refuses, so "no new work starts" is a guarantee
// rather than a tendency. An agent already holding a ticket is still told
// to finish or release it: switching this off means "start nothing new",
// not "abandon what you are doing", because the context a stopped session
// takes with it is not recoverable.
const TicketProcessingEnabled = "ticket_processing_enabled"

// ProvisionFromBoard lets the board's desired-state document drive a
// supervisor's agents. Off, the supervisor's read of the document is empty, so
// nothing that edits it can reach a host until an owner turns this on.
const ProvisionFromBoard = "provision_from_board"

// WorkspaceReconcile lets a supervisor bring its slots' checkouts in line with
// the workspace manifest. Off, the supervisor leaves them alone.
const WorkspaceReconcile = "workspace_reconcile"

// IntegrationLockDeadHolderAfter is how many minutes the integration lock's
// holder may go without a heartbeat before the board flags it as dead. Only a
// flag: freeing the lock stays a person's call.
const IntegrationLockDeadHolderAfter = "integration_lock.dead_holder_after"

const (
	DefaultDeadHolderAfterMinutes = 60
	MinDeadHolderAfterMinutes     = 5
	MaxDeadHolderAfterMinutes     = 1440
)

// IntegrationLockPriorityAgingMinutes is the step of the integration lock
// queue's aging: a waiter moves up one priority rank for every this many
// minutes it has waited, so a low-priority waiter is not starved while the lock
// stays saturated.
const IntegrationLockPriorityAgingMinutes = "integration_lock.priority_aging_minutes"

const (
	DefaultPriorityAgingMinutes = 30
	MinPriorityAgingMinutes     = 5
	MaxPriorityAgingMinutes     = 240
)

// MergeQueueBatchSize is how many queued changes the merge queue tests in one
// CI run. At 1 every change is tested alone, as before batching existed. It
// only applies while merge_method is merge_queue.
const MergeQueueBatchSize = "merge_queue_batch_size"

const (
	DefaultMergeQueueBatchSize = 1
	MinMergeQueueBatchSize     = 1
	MaxMergeQueueBatchSize     = 3
)

// Spec is one setting's declaration. A package that owns a setting hands its
// Specs to Register at wiring time, so this package does not have to know the
// package's rules.
type Spec struct {
	Definition gosettings.Definition
	// OwnerOnly restricts changing the setting to a tenant owner.
	OwnerOnly bool
	// Validate applies the rules the type cannot express, after the name and
	// type have been checked. Nil accepts any value of the right type. An error
	// it returns is reported as ErrInvalidSetting.
	Validate func(Value) error
}

// ErrInvalidSetting is a value of the right type that a setting's rules refuse.
var ErrInvalidSetting = errors.New("settings: invalid setting")

// invalidSetting reports a rule's refusal as ErrInvalidSetting without
// rewording the rule's own message.
type invalidSetting struct{ err error }

func (e *invalidSetting) Error() string        { return e.err.Error() }
func (e *invalidSetting) Unwrap() error        { return e.err }
func (e *invalidSetting) Is(target error) bool { return target == ErrInvalidSetting }

// validate applies the setting's own rules. It runs after the registry has
// checked the name and type.
func validate(name string, v Value) error {
	known.mu.RLock()
	spec := known.specs[name]
	known.mu.RUnlock()
	if spec.Validate == nil {
		return nil
	}
	if err := spec.Validate(v); err != nil {
		return &invalidSetting{err: err}
	}
	return nil
}

func validateDeadHolderAfter(v Value) error {
	if v.Int < MinDeadHolderAfterMinutes || v.Int > MaxDeadHolderAfterMinutes {
		return fmt.Errorf("%w: dead holder minutes must be %d..%d", ErrInvalidSetting,
			MinDeadHolderAfterMinutes, MaxDeadHolderAfterMinutes)
	}
	return nil
}

func validateMergeQueueBatchSize(v Value) error {
	if v.Int < MinMergeQueueBatchSize || v.Int > MaxMergeQueueBatchSize {
		return fmt.Errorf("%w: merge queue batch size must be %d..%d", ErrInvalidSetting,
			MinMergeQueueBatchSize, MaxMergeQueueBatchSize)
	}
	return nil
}

func validatePriorityAging(v Value) error {
	if v.Int < MinPriorityAgingMinutes || v.Int > MaxPriorityAgingMinutes {
		return fmt.Errorf("%w: priority aging minutes must be %d..%d", ErrInvalidSetting,
			MinPriorityAgingMinutes, MaxPriorityAgingMinutes)
	}
	return nil
}

// known holds the settings declared so far: the ones below, plus whatever
// other packages Register. A name not in it is rejected rather than stored, so
// a typo in a PATCH cannot quietly create a setting that nothing reads.
//
// Registration is process-wide because the store and the typed accessors
// resolve names without a Service in hand; it happens at wiring time, before
// the server takes requests.
var known = newKnown(
	Spec{Definition: gosettings.Definition{Name: TicketProcessingEnabled, Default: BoolValue(true)}},
	Spec{Definition: gosettings.Definition{Name: ProvisionFromBoard, Default: BoolValue(false)}, OwnerOnly: true},
	Spec{Definition: gosettings.Definition{Name: WorkspaceReconcile, Default: BoolValue(false)}, OwnerOnly: true},
	Spec{Definition: gosettings.Definition{Name: IntegrationLockDeadHolderAfter, Default: IntValue(DefaultDeadHolderAfterMinutes)},
		Validate: validateDeadHolderAfter},
	Spec{Definition: gosettings.Definition{Name: IntegrationLockPriorityAgingMinutes, Default: IntValue(DefaultPriorityAgingMinutes)},
		Validate: validatePriorityAging},
	Spec{Definition: gosettings.Definition{Name: MergeQueueBatchSize, Default: IntValue(DefaultMergeQueueBatchSize)},
		OwnerOnly: true, Validate: validateMergeQueueBatchSize},
)

type knownSettings struct {
	mu       sync.RWMutex
	specs    map[string]Spec
	registry *gosettings.Registry
}

func newKnown(specs ...Spec) *knownSettings {
	k := &knownSettings{specs: map[string]Spec{}}
	if err := k.add(specs...); err != nil {
		panic(err) // a static declaration: a failure is a programming error
	}
	return k
}

func (k *knownSettings) add(specs ...Spec) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	next := make(map[string]Spec, len(k.specs)+len(specs))
	for name, s := range k.specs {
		next[name] = s
	}
	for _, s := range specs {
		if _, dup := next[s.Definition.Name]; dup {
			return fmt.Errorf("settings: %q is already registered", s.Definition.Name)
		}
		next[s.Definition.Name] = s
	}
	defs := make([]gosettings.Definition, 0, len(next))
	for _, s := range next {
		defs = append(defs, s.Definition)
	}
	reg, err := gosettings.NewRegistry(defs...)
	if err != nil {
		return err
	}
	k.specs, k.registry = next, reg
	return nil
}

func (k *knownSettings) reg() *gosettings.Registry {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.registry
}

// Register declares another package's settings. Call it once per package at
// wiring time; a duplicate name is an error.
func Register(specs ...Spec) error { return known.add(specs...) }

func ownerOnly(name string) bool {
	known.mu.RLock()
	defer known.mu.RUnlock()
	return known.specs[name].OwnerOnly
}

// Setting is one named value as callers see it.
type Setting struct {
	Name  string
	Value Value
	// UpdatedAt is zero for a setting that has never been written and is
	// therefore reporting its default.
	UpdatedAt time.Time
}

// IsDefault reports whether this setting has never been written.
func (s Setting) IsDefault() bool { return s.UpdatedAt.IsZero() }

var (
	ErrUnknownSetting = gosettings.ErrUnknownSetting
	ErrTypeMismatch   = gosettings.ErrTypeMismatch
)

// Default returns the declared default for name.
func Default(name string) (Value, error) {
	def, ok := known.reg().Lookup(name)
	if !ok {
		return Value{}, gosettings.Unknown(name)
	}
	return def.Default, nil
}

// Names lists every known setting, for the UI and for List. Sorted, so the
// settings list does not reshuffle between reloads.
func Names() []string { return known.reg().Names() }
