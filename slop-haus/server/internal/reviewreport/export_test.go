package reviewreport

// SetRequestBarrier installs the test-only barrier inside RequestReview's
// transaction, and returns a function restoring the previous value. See
// requestBarrier in request_store.go.
func SetRequestBarrier(fn func()) func() {
	previous := requestBarrier
	requestBarrier = fn
	return func() { requestBarrier = previous }
}
