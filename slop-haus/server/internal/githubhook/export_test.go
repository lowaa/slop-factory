package githubhook

// SetFinishBarrier installs the test-only barrier between handling a
// delivery and recording it, and returns a function restoring the previous
// value. See finishBarrier in service.go.
func SetFinishBarrier(fn func()) func() {
	previous := finishBarrier
	finishBarrier = fn
	return func() { finishBarrier = previous }
}
