package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is the CLI's side of the control socket.
type Client struct {
	http   *http.Client
	socket string
	token  string
}

// NewClient talks to the daemon at socket. token is the agent token, or ""
// for the user.
func NewClient(socket, token string) *Client {
	return &Client{
		http:   &http.Client{Transport: &http.Transport{DialContext: Dialer(socket)}},
		socket: socket,
		token:  token,
	}
}

// APIError is a non-2xx answer from the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// Do sends in (if non-nil) as JSON and decodes the answer into out (if
// non-nil).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	resp, err := c.send(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("control: decoding %s %s: %w", method, path, err)
	}
	return nil
}

// Stream opens a long-lived GET whose body is newline-delimited JSON. The
// caller closes it.
func (c *Client) Stream(ctx context.Context, path string) (io.ReadCloser, error) {
	return c.StreamRequest(ctx, http.MethodGet, path, nil)
}

// StreamRequest is Stream for any method, sending in (if non-nil) as JSON.
func (c *Client) StreamRequest(ctx context.Context, method, path string, in any) (io.ReadCloser, error) {
	resp, err := c.send(ctx, method, path, in)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) send(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	// The host is ignored: the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://slop-bot"+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		var eb ErrorBody
		if json.NewDecoder(resp.Body).Decode(&eb) != nil || eb.Error == "" {
			eb.Error = resp.Status
		}
		return nil, &APIError{Status: resp.StatusCode, Message: eb.Error}
	}
	return resp, nil
}
