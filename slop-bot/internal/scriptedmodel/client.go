package scriptedmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// A Client drives a scripted model server in another process (a container
// in tier 2) through its control endpoints: the same operations a Server
// offers in-process.
type Client struct {
	base string
	http *http.Client
}

// NewClient talks to the server at baseURL. hc defaults to
// http.DefaultClient; HeldGate blocks for as long as its context allows, so a
// client timeout would cut it short.
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), http: hc}
}

// Load replaces the server's scenario and resets its recording.
func (c *Client) Load(ctx context.Context, scenario *Scenario) error {
	return c.do(ctx, http.MethodPut, "/scenario", mustJSON(scenario), nil)
}

// Reset unloads the scenario and resets the recording.
func (c *Client) Reset(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/scenario", nil, nil)
}

// Release opens a gate.
func (c *Client) Release(ctx context.Context, gate string) error {
	return c.do(ctx, http.MethodPut, "/gates/"+url.PathEscape(gate), nil, nil)
}

// HeldGate returns once a request is waiting at the gate, or when ctx ends.
func (c *Client) HeldGate(ctx context.Context, gate string) error {
	return c.do(ctx, http.MethodGet, "/gates/"+url.PathEscape(gate)+"/held", nil, nil)
}

// Requests is every request the server received since the scenario was loaded.
func (c *Client) Requests(ctx context.Context) ([]*Request, error) {
	var out []*Request
	return out, c.do(ctx, http.MethodGet, "/requests", nil, &out)
}

// Errors is the unexpected-request failures so far.
func (c *Client) Errors(ctx context.Context) ([]string, error) {
	var out []string
	return out, c.do(ctx, http.MethodGet, "/errors", nil, &out)
}

// Remaining is how many scripted turns are unplayed.
func (c *Client) Remaining(ctx context.Context) (int, error) {
	var out struct {
		Remaining int `json:"remaining"`
	}
	return out.Remaining, c.do(ctx, http.MethodGet, "/remaining", nil, &out)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+ControlPrefix+path, reader)
	if err != nil {
		return fmt.Errorf("scripted model %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("scripted model %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("scripted model %s %s: %w", method, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("scripted model %s %s: %s: %s", method, path, resp.Status, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("scripted model %s %s: %w", method, path, err)
	}
	return nil
}
