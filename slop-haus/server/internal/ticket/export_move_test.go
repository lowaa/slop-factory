package ticket

// SetMoveBarrier installs the test-only barrier after a batch's first move,
// and returns a function restoring the previous value. See moveBarrier in
// move_store.go.
func SetMoveBarrier(fn func()) func() {
	previous := moveBarrier
	moveBarrier = fn
	return func() { moveBarrier = previous }
}
