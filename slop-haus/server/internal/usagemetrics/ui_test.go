package usagemetrics

import (
	"fmt"
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUI(t *testing.T) (*UI, *prometheus.Registry) {
	t.Helper()
	ui := NewUI()
	reg := prometheus.NewRegistry()
	reg.MustRegister(ui.Collectors()...)
	return ui, reg
}

func uiCount(t *testing.T, reg *prometheus.Registry, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != UIEventsMetric {
			continue
		}
	metrics:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() != labels[l.GetName()] {
					continue metrics
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return -1
}

func seriesCount(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	n := 0
	for _, f := range families {
		n += len(f.GetMetric())
	}
	return n
}

func TestUILabelsAreThePinnedSet(t *testing.T) {
	ui, _ := newUI(t)
	assert.Equal(t, UILabels, ui.labels, "a new attribute in the catalogue is a new label: add it to UILabels deliberately")
	assert.True(t, sort.StringsAreSorted(ui.labels[1:]))
}

func TestUIEveryAttributeIsAnEnumFlagOrBoundedNumber(t *testing.T) {
	ui, _ := newUI(t)
	for name, attrs := range ui.allow.Events {
		for attr, spec := range attrs {
			switch spec.Type {
			case "enum":
				assert.NotEmpty(t, spec.Values, "%s.%s", name, attr)
			case "bool":
			case "int":
				assert.Positive(t, spec.Max, "%s.%s", name, attr)
				assert.Positive(t, spec.Step, "%s.%s", name, attr)
			default:
				t.Errorf("%s.%s has type %q", name, attr, spec.Type)
			}
		}
	}
}

func TestUICountsAllowedEventsByTheirAttributes(t *testing.T) {
	ui, reg := newUI(t)
	n := ui.Record([]UIEvent{
		{Name: "steward_answer", Attrs: map[string]any{"screen": "ticket", "action": "approve"}},
		{Name: "steward_answer", Attrs: map[string]any{"screen": "ticket", "action": "approve"}},
		{Name: "filter_used", Attrs: map[string]any{"screen": "board", "filter": "ticket_text", "has_text": true}},
		{Name: "bulk_action", Attrs: map[string]any{"screen": "board", "action": "move", "outcome": "done", "count_bucket": "2-5"}},
	})
	assert.Equal(t, 4, n)
	assert.Equal(t, 2.0, uiCount(t, reg, map[string]string{"event": "steward_answer", "screen": "ticket", "action": "approve"}))
	assert.Equal(t, 1.0, uiCount(t, reg, map[string]string{"event": "filter_used", "screen": "board", "filter": "ticket_text", "has_text": "true"}))
	assert.Equal(t, 1.0, uiCount(t, reg, map[string]string{"event": "bulk_action", "screen": "board", "action": "move", "outcome": "done", "count_bucket": "2-5"}))
}

func TestUIDropsWhatTheAllowlistDoesNotName(t *testing.T) {
	ui, reg := newUI(t)
	before := seriesCount(t, reg)
	n := ui.Record([]UIEvent{
		{Name: "made_up", Attrs: map[string]any{"screen": "board"}},
		{Name: "steward_answer", Attrs: map[string]any{"screen": "a-real-ticket-title", "action": "approve"}},
		{Name: "steward_answer", Attrs: map[string]any{"screen": "ticket", "action": "alice@example.com"}},
		{Name: "steward_answer", Attrs: map[string]any{"screen": "ticket"}},
		{Name: "steward_answer", Attrs: map[string]any{"screen": "ticket", "action": 7}},
		{Name: "filter_used", Attrs: map[string]any{"screen": "board", "filter": "ticket_text", "has_text": "yes"}},
		{Name: "screen_time", Attrs: map[string]any{"screen": "board", "visible_ms": 1234.0}},
		{Name: "screen_time", Attrs: map[string]any{"screen": "board", "visible_ms": 99999999.0}},
		{Name: "screen_time", Attrs: map[string]any{"screen": "board", "visible_ms": -5000.0}},
		{Name: "screen_time", Attrs: map[string]any{"screen": "board", "visible_ms": "15000"}},
		{Name: "", Attrs: nil},
	})
	assert.Zero(t, n)
	assert.Equal(t, before, seriesCount(t, reg), "nothing the browser says makes a series")
	assert.Equal(t, 0.0, uiCount(t, reg, map[string]string{"event": "steward_answer"}))
}

func TestUIIgnoresAnAttributeTheEventDoesNotList(t *testing.T) {
	ui, reg := newUI(t)
	n := ui.Record([]UIEvent{{Name: "new_ticket", Attrs: map[string]any{"screen": "board", "action": "open_form", "user_id": "u-1", "filter": "ticket_text", "title": "secret"}}})
	assert.Equal(t, 1, n)
	assert.Equal(t, 1.0, uiCount(t, reg, map[string]string{"event": "new_ticket", "screen": "board", "action": "open_form"}), "the filter label stays empty: new_ticket has no filter")
}

func TestUIScreenTimeFeedsTheHistogramNotTheEventCounter(t *testing.T) {
	ui, reg := newUI(t)
	ui.Record([]UIEvent{{Name: "screen_time", Attrs: map[string]any{"screen": "reports", "visible_ms": 45000.0}}})
	families, err := reg.Gather()
	require.NoError(t, err)
	var sum float64
	for _, f := range families {
		if f.GetName() == UIScreenMetric {
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == LabelScreen && l.GetValue() == "reports" {
						sum = m.GetHistogram().GetSampleSum()
					}
				}
			}
		}
	}
	assert.Equal(t, 45.0, sum)
	assert.Equal(t, -1.0, uiCount(t, reg, map[string]string{"event": "screen_time", "screen": "reports"}))
}

// Every event has a series at zero from the start, which is how a dashboard
// finds the features nobody uses.
func TestUIEveryEventStartsAtZero(t *testing.T) {
	ui, reg := newUI(t)
	for name := range ui.allow.Events {
		if name == "screen_time" {
			continue
		}
		assert.Equal(t, 0.0, uiCount(t, reg, map[string]string{"event": name}), name)
	}
}

// Whatever is sent, only the pinned labels exist and the number of series
// cannot pass what the allowlist's values multiply to.
func TestUISeriesAreBoundedAndCarryOnlyAllowedLabels(t *testing.T) {
	ui, reg := newUI(t)
	// Each event's values multiplied (an optional attribute may also be
	// absent), plus its series at zero.
	bound := 0
	for name, attrs := range ui.allow.Events {
		if name == "screen_time" {
			continue
		}
		combos := 1
		for _, spec := range attrs {
			options := 0
			switch spec.Type {
			case "enum":
				options = len(spec.Values)
			case "bool":
				options = 2
			}
			if spec.Optional {
				options++
			}
			combos *= max(options, 1)
		}
		bound += combos + 1
	}
	var flood []UIEvent
	for i := 0; i < 5000; i++ {
		flood = append(flood,
			UIEvent{Name: fmt.Sprintf("event-%d", i), Attrs: map[string]any{"screen": fmt.Sprintf("screen-%d", i)}},
			UIEvent{Name: "steward_answer", Attrs: map[string]any{"screen": fmt.Sprintf("screen-%d", i), "action": fmt.Sprintf("a%d", i)}},
			UIEvent{Name: "filter_used", Attrs: map[string]any{"screen": "board", "filter": fmt.Sprintf("f%d", i), fmt.Sprintf("x%d", i): i}},
		)
	}
	ui.Record(flood)
	assert.LessOrEqual(t, testutil.CollectAndCount(ui.events), bound+len(ui.allow.Events))

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.Contains(t, append([]string{"le"}, UILabels...), l.GetName(), f.GetName())
			}
		}
	}
}
