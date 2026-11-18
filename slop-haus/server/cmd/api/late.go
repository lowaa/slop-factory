package main

import (
	"errors"
	"fmt"
	"sync"
)

// lateCells are the bindings a wiring cycle forces to happen after the
// consumer is built. Every cell registers here, so verifyAll can refuse to
// start when one was never bound, instead of the gap showing up at the first
// request through it.
type lateCells struct {
	mu    sync.Mutex
	names []string
	bound []func() bool
}

func (c *lateCells) register(name string, isBound func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
	c.bound = append(c.bound, isBound)
}

// verifyAll names every cell never bound.
func (c *lateCells) verifyAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var unbound []error
	for i, isBound := range c.bound {
		if !isBound() {
			unbound = append(unbound, errors.New(c.names[i]))
		}
	}
	if len(unbound) > 0 {
		return fmt.Errorf("wiring: late-bound dependencies never set: %w", errors.Join(unbound...))
	}
	return nil
}

// Late is a dependency that exists only after its consumer is built.
type Late[T any] struct {
	name  string
	mu    sync.RWMutex
	value T
	set   bool
}

// newLate makes a cell checked by cells.verifyAll.
func newLate[T any](cells *lateCells, name string) *Late[T] {
	l := &Late[T]{name: name}
	cells.register(name, l.isBound)
	return l
}

func (l *Late[T]) isBound() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.set
}

// Bind sets the value; binding twice is a wiring bug.
func (l *Late[T]) Bind(v T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.set {
		panic("wiring: late dependency bound twice: " + l.name)
	}
	l.value, l.set = v, true
}

// Get is the bound value. Reading an unbound cell is a wiring bug that
// verifyAll should have refused at startup, so it panics rather than
// returning a zero value that quietly behaves like "none".
func (l *Late[T]) Get() T {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.set {
		panic("wiring: late dependency used before it was bound: " + l.name)
	}
	return l.value
}
