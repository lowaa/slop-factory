package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
)

// providerConfigTimeout bounds the fetch at the start of a run: a server
// that is slow to answer must not hold up a review that can run on its
// saved copy instead.
const providerConfigTimeout = 5 * time.Second

// maxProviderConfigBytes is far more than any valid config needs.
const maxProviderConfigBytes = 64 << 10

// ErrNoProviderConfig means the server has no provider config for this
// tenant -- none was ever saved, or the server predates them.
var ErrNoProviderConfig = errors.New("the server has no provider config for this tenant")

// FetchProviderConfig asks the server for the tenant's provider config,
// with the same token and the same URL checks as a report. What comes back
// is validated here, so a caller only ever sees a config naming compiled-in
// providers.
func (r *Reporter) FetchProviderConfig(ctx context.Context) (*providerconfig.Config, error) {
	token, err := r.bearer(ctx)
	if err != nil {
		return nil, err
	}
	// After the token, which has its own limit: the command may take longer
	// than the fetch is allowed.
	ctx, cancel := context.WithTimeout(ctx, providerConfigTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+providerconfig.Path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errors.New(r.redact(fmt.Sprintf("fetch provider config: %v", err)))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderConfigBytes+1))
	if err != nil {
		return nil, errors.New(r.redact(fmt.Sprintf("fetch provider config: read: %v", err)))
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoProviderConfig
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New(r.redact(fmt.Sprintf("fetch provider config: the server refused the token (401): %s -- check SLOP_REVIEW_REPORT_TOKEN or SLOP_REVIEW_REPORT_TOKEN_COMMAND", firstLine(body))))
	case resp.StatusCode != http.StatusOK:
		return nil, errors.New(r.redact(fmt.Sprintf("fetch provider config: server returned %d: %s", resp.StatusCode, firstLine(body))))
	case len(body) > maxProviderConfigBytes:
		return nil, fmt.Errorf("fetch provider config: the answer is over %d bytes", maxProviderConfigBytes)
	}

	var cfg providerconfig.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("fetch provider config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("the server's provider config is not valid, so it is ignored: %w", err)
	}
	return &cfg, nil
}
