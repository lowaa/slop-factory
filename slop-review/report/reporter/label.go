package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
)

// LabelRequest is the body of a label post; the field names are the
// server's.
type LabelRequest struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	FixedIn string `json:"fixed_in,omitempty"`
}

// LabelPath is where a finding is labelled as Claude.
func LabelPath(findingID uuid.UUID) string {
	return "/api/v1/findings/" + findingID.String() + "/labels"
}

// Label posts Claude's verdict on a finding. Unlike Send it does not spool:
// a label is a one-line command whose caller can simply repeat it, and the
// latest label per source is the current one, so repeating is harmless.
func (r *Reporter) Label(ctx context.Context, findingID uuid.UUID, l *LabelRequest) error {
	body, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encode label: %w", err)
	}
	token, err := r.bearer(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+LabelPath(findingID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s", r.redact(fmt.Sprintf("post label: %v", err)))
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("the server has no finding %s: its run may not have been reported (check for a spooled run)", findingID)
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("server refused the token (401) -- check SLOP_REVIEW_REPORT_TOKEN or SLOP_REVIEW_REPORT_TOKEN_COMMAND")
	default:
		return fmt.Errorf("%s", r.redact(fmt.Sprintf("server returned %d: %s", resp.StatusCode, firstLine(respBody))))
	}
}
