package usagemetrics

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	UIEventsMetric  = "slophaus_ui_events_total"
	UIScreenMetric  = "slophaus_ui_screen_seconds"
	LabelEvent      = "event"
	LabelScreen     = "screen"
	uiScreenTimeEvt = "screen_time"
	uiVisibleMs     = "visible_ms"
)

// UILabels are every label a browser-reported series may carry, the
// catalogue's non-numeric attributes plus the event name. The frontend
// catalogue (frontend/src/usage/catalog.ts) is their source and a test holds
// this list to it; none can name a person, tenant or session.
var UILabels = []string{LabelEvent, "action", "count_bucket", "filter", "has_text", "outcome", LabelScreen}

// uiAllowlist is generated from the frontend catalogue
// (npm run generate:usage_allowlist in slop-haus/frontend); a frontend test
// fails when it is stale.
//
//go:embed ui_allowlist.json
var uiAllowlistJSON []byte

type attrSpec struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Max    int      `json:"max"`
	Step   int      `json:"step"`
	// Optional attributes may be left out, and are then an empty label.
	Optional bool `json:"optional"`
}

type uiAllowlist struct {
	Events map[string]map[string]attrSpec `json:"events"`
}

// UIEvent is one event as the browser reports it. Both fields are the
// browser's own text and are checked against the allowlist before they
// become label values.
type UIEvent struct {
	Name  string
	Attrs map[string]any
}

// UI counts the usage events a signed-in browser reports: how often each
// feature is used and, per screen, how long it is on show. Counts only: an
// event is never logged or stored per user, and the reporting user is not
// known here (see Handler, which holds the limiter).
type UI struct {
	allow  uiAllowlist
	labels []string
	events *prometheus.CounterVec
	times  *prometheus.HistogramVec
	// screens are the values the screen attribute may take.
	screens []string
}

// NewUI builds the counters from the embedded allowlist. It panics if that is
// malformed, which a test would have caught: it is compiled in.
func NewUI() *UI {
	u := &UI{}
	if err := json.Unmarshal(uiAllowlistJSON, &u.allow); err != nil {
		panic(fmt.Sprintf("usagemetrics: ui_allowlist.json: %v", err))
	}
	u.labels = append([]string{LabelEvent}, u.counterAttrs()...)
	screen, ok := u.allow.Events["screen_view"][LabelScreen]
	if !ok {
		panic("usagemetrics: ui_allowlist.json has no screen_view screen")
	}
	u.screens = screen.Values
	u.events = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: UIEventsMetric,
		Help: "Board UI events reported by signed-in browsers, by event and its enum attributes; no identifiers.",
	}, u.labels)
	u.times = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    UIScreenMetric,
		Help:    "Visible, focused time on a board screen per visit stretch; no identifiers.",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1800},
	}, []string{LabelScreen})
	u.start()
	return u
}

// counterAttrs are the attributes that become labels: every one but the
// numeric ones, which the histogram takes instead.
func (u *UI) counterAttrs() []string {
	set := map[string]bool{}
	for _, attrs := range u.allow.Events {
		for name, spec := range attrs {
			if spec.Type != "int" {
				set[name] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// start makes a series per event and one per screen at zero, so a dashboard
// can tell an event never used (the answer to "which features are unused")
// from one that does not exist, and so the first use shows in increase().
func (u *UI) start() {
	for name := range u.allow.Events {
		if name == uiScreenTimeEvt {
			continue
		}
		values := make([]string, len(u.labels))
		values[0] = name
		u.events.WithLabelValues(values...)
	}
	for _, screen := range u.screens {
		u.times.WithLabelValues(screen)
	}
}

// Collectors are what to register for /metrics to serve the series.
func (u *UI) Collectors() []prometheus.Collector {
	return []prometheus.Collector{u.events, u.times}
}

// Record counts the events that match the allowlist and drops the rest. An
// attribute the event does not list is ignored; an event with a missing or
// out-of-range value, or of an unknown name, is dropped whole. It returns how
// many were counted.
func (u *UI) Record(events []UIEvent) int {
	counted := 0
	for _, e := range events {
		spec, ok := u.allow.Events[e.Name]
		if !ok {
			continue
		}
		values, ms, ok := u.check(spec, e.Attrs)
		if !ok {
			continue
		}
		if e.Name == uiScreenTimeEvt {
			u.times.WithLabelValues(values[LabelScreen]).Observe(float64(ms) / 1000)
		} else {
			labels := make([]string, len(u.labels))
			labels[0] = e.Name
			for i, name := range u.labels[1:] {
				labels[i+1] = values[name]
			}
			u.events.WithLabelValues(labels...).Inc()
		}
		counted++
	}
	return counted
}

// check validates the attributes against the event's spec and returns the
// label values, plus the numeric attribute (the visible time) if any.
func (u *UI) check(spec map[string]attrSpec, attrs map[string]any) (labels map[string]string, ms int, ok bool) {
	labels = map[string]string{}
	for name, s := range spec {
		raw, present := attrs[name]
		if !present {
			if s.Optional {
				continue
			}
			return nil, 0, false
		}
		switch s.Type {
		case "enum":
			str, isStr := raw.(string)
			if !isStr || !slices.Contains(s.Values, str) {
				return nil, 0, false
			}
			labels[name] = str
		case "bool":
			b, isBool := raw.(bool)
			if !isBool {
				return nil, 0, false
			}
			labels[name] = fmt.Sprint(b)
		case "int":
			n, isNum := raw.(float64)
			if !isNum || n != math.Trunc(n) || n < 0 || n > float64(s.Max) || int(n)%s.Step != 0 {
				return nil, 0, false
			}
			ms = int(n)
		default:
			return nil, 0, false
		}
	}
	return labels, ms, true
}
