package otellog

import (
	"errors"
	"net"
	"net/http"
)

// retryingTransport marks a connection that could not be made as
// temporary. otlploghttp retries only errors whose Temporary() is true, and
// the standard library's answer for ECONNREFUSED -- what a collector that is
// restarting gives -- is false, so by default a batch sent while the collector
// is down is dropped on the first attempt and never retried.
type retryingTransport struct {
	next http.RoundTripper
}

func (t *retryingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil && isConnectionFailure(err) {
		return resp, temporaryError{err}
	}
	return resp, err
}

func isConnectionFailure(err error) bool {
	// Dial failures only: nothing was sent. A reset after the request went
	// out may follow the collector ingesting it, and a retry would store the
	// batch twice.
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

type temporaryError struct{ error }

func (temporaryError) Temporary() bool { return true }
func (e temporaryError) Unwrap() error { return e.error }
