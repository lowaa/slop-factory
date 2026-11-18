package conductor

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Activity statuses.
const (
	ActivityRunning = "running"
	ActivityDone    = "done"
	ActivityFailed  = "failed"
)

const (
	MaxActivityToolBytes = 64
	MaxActivityLineBytes = 480
	MaxActivityCallBytes = 128
)

// Activity is one tool call of a turn, as the host reports it and as it is
// stored: the tool's name and the one line that describes the call. Never the
// call's input or output, which can hold secrets and would fill the
// transcript; the host is what keeps them out, and the limits here keep a
// careless host from sending more than a line.
type Activity struct {
	// CallID ties the running report of a call to the one that ends it.
	CallID string `json:"call_id" maxLength:"128"`
	Tool   string `json:"tool" maxLength:"64"`
	Line   string `json:"line" maxLength:"480"`
	Status string `json:"status" enum:"running,done,failed"`
}

func (a *Activity) Validate() error {
	if a.CallID == "" || badText(a.CallID, MaxActivityCallBytes) {
		return invalid("activity needs a call_id of at most %d bytes", MaxActivityCallBytes)
	}
	if a.Tool == "" || badText(a.Tool, MaxActivityToolBytes) {
		return invalid("activity needs a tool of at most %d bytes", MaxActivityToolBytes)
	}
	if badText(a.Line, MaxActivityLineBytes) {
		return invalid("an activity line is text of at most %d bytes", MaxActivityLineBytes)
	}
	switch a.Status {
	case ActivityRunning, ActivityDone, ActivityFailed:
	default:
		return invalid("activity status %q", a.Status)
	}
	return nil
}

// encode is the activity as the transcript event's body.
func (a *Activity) encode() (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(a); err != nil {
		return "", fmt.Errorf("conductor: encode activity: %w", err)
	}
	body := buf.String()[:buf.Len()-1]
	if len(body) > MaxActivityBytes {
		return "", invalid("activity is larger than %d bytes", MaxActivityBytes)
	}
	return body, nil
}
