package session

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Description is what a session says about itself: which harness runs it,
// and on what model. It is self-asserted by the agent's own machine, so it
// is fit for showing a human and for routing work, and never for deciding
// what an agent is allowed to do.
type Description struct {
	Harness        string
	HarnessVersion string
	Provider       string
	Model          string
	Labels         map[string]string
	// ConfigRevision is the harness config revision run_remote_control.sh started the
	// run with (see internal/harnessconfig); nil when it did not say.
	ConfigRevision *int64
	// BundleHash is the managed asset bundle the workspace says it installed
	// (workspace.BundleHash); empty when it did not say.
	BundleHash string
}

// Limits on a description. Generous for anything real -- a model id or a
// version string is tens of characters -- and small enough that a sender
// cannot fill a session row, or the Agents screen, with junk.
const (
	MaxDescriptionField = 100
	MaxLabels           = 20
	MaxLabelKey         = 64
	MaxLabelValue       = 200
)

// ParseDescription reads a hook payload's slop_haus object.
//
// Lenient by design: the description is decoration on a hook whose real job
// is the agent loop, so anything malformed is dropped or trimmed rather than
// rejected -- a bad label must never turn a Stop into an error that lets the
// agent quit. Nil means the payload carried nothing usable.
func ParseDescription(raw json.RawMessage) *Description {
	if len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	d := &Description{
		Harness:        stringField(doc["harness"]),
		HarnessVersion: stringField(doc["harness_version"]),
		Provider:       stringField(doc["provider"]),
		Model:          stringField(doc["model"]),
		ConfigRevision: revisionField(doc["config_revision"]),
		BundleHash:     stringField(doc["bundle_hash"]),
	}
	if labels, ok := doc["labels"].(map[string]any); ok {
		d.Labels = make(map[string]string, len(labels))
		for k, v := range labels {
			d.Labels[k] = stringField(v)
		}
	}
	d = d.Sanitized()
	if d.Empty() {
		return nil
	}
	return d
}

// revisionField takes a non-negative whole number, as a JSON number or a
// string of digits -- the environment it comes from only has strings --
// and nothing else.
func revisionField(v any) *int64 {
	var n int64
	switch v := v.(type) {
	case float64:
		// Past 2^53 a float64 no longer holds every whole number, so a
		// larger one may already have been rounded to another revision.
		if v < 0 || v != math.Trunc(v) || v >= 1<<53 {
			return nil
		}
		n = int64(v)
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != v {
			return nil
		}
		n = parsed
	default:
		return nil
	}
	return &n
}

// stringField takes a scalar as text and drops anything nested.
func stringField(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64, bool:
		return fmt.Sprint(v)
	default:
		return ""
	}
}

// Empty reports whether there is nothing to record.
func (d *Description) Empty() bool {
	return d.Harness == "" && d.HarnessVersion == "" && d.Provider == "" && d.Model == "" && len(d.Labels) == 0 &&
		d.ConfigRevision == nil && d.BundleHash == ""
}

// Sanitized returns a copy fit to store and display: control characters
// removed, whitespace trimmed, each field capped, and labels limited to
// MaxLabels -- the alphabetically first, so which ones survive does not
// depend on map order.
func (d *Description) Sanitized() *Description {
	out := &Description{
		Harness:        clean(d.Harness, MaxDescriptionField),
		HarnessVersion: clean(d.HarnessVersion, MaxDescriptionField),
		Provider:       clean(d.Provider, MaxDescriptionField),
		Model:          clean(d.Model, MaxDescriptionField),
		BundleHash:     clean(d.BundleHash, MaxDescriptionField),
		Labels:         map[string]string{},
	}
	if d.ConfigRevision != nil && *d.ConfigRevision >= 0 {
		revision := *d.ConfigRevision
		out.ConfigRevision = &revision
	}
	keys := make([]string, 0, len(d.Labels))
	for k := range d.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out.Labels) == MaxLabels {
			break
		}
		key := clean(k, MaxLabelKey)
		if key == "" {
			continue
		}
		out.Labels[key] = clean(d.Labels[k], MaxLabelValue)
	}
	return out
}

// clean strips control and format characters -- a terminal escape or a
// bidi override has no business in a model name shown to a human -- and
// caps the result at max runes.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > max {
		s = strings.TrimSpace(string(runes[:max]))
	}
	return s
}
