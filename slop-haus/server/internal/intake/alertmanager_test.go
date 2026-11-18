package intake_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Alertmanager's version 4 webhook, as it sends it: fixtures under
// testdata/alertmanager, one file per notification.

func amFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "alertmanager", name+".json"))
	require.NoError(t, err)
	return string(raw)
}

func parseAlert(t *testing.T, name string) *intake.Event {
	t.Helper()
	adapter, ok := intake.AdapterFor("alertmanager")
	require.True(t, ok)
	ev, err := adapter.Parse([]byte(amFile(t, name)))
	require.NoError(t, err)
	require.NoError(t, intake.CheckEvent(ev), "what an adapter produces passes the same checks as a native event")
	return ev
}

func TestAlertmanager_FiringIsAnOpenKeyedByTheGroup(t *testing.T) {
	ev := parseAlert(t, "firing_single")
	assert.Equal(t, intake.EventOpen, ev.Kind)
	assert.Equal(t, `{}:{alertname="SlopHausReleaseFailing"}`, ev.Key)
	assert.Equal(t, "SlopHausReleaseFailing: slop-haus releases keep failing at wait-prod", ev.Title)
	assert.Contains(t, ev.Body, "17 consecutive releases failed at wait-prod")
	assert.Contains(t, ev.Body, "- step = wait-prod")
	assert.Equal(t, "http://prometheus.monitoring:9090/graph?g0.expr=slophaus_release_failures%3E3&g0.tab=1", ev.Link, "the alert's own generator URL")
	assert.Equal(t, model.PriorityUrgent, ev.Priority, "from slophaus_priority")
	assert.Empty(t, ev.Status)
	assert.JSONEq(t, `{"status":"firing","alerts":1,"alertname":"SlopHausReleaseFailing","severity":"critical","project":"slop-haus","receiver":"slop-haus"}`, string(ev.Context))
}

func TestAlertmanager_ResolvedIsAResolve(t *testing.T) {
	ev := parseAlert(t, "resolved")
	assert.Equal(t, intake.EventResolve, ev.Kind)
	assert.Equal(t, parseAlert(t, "firing_single").Key, ev.Key, "the same group key as the firing it ends")
	assert.Contains(t, ev.Body, "resolved 2026-10-04T11:03:30.123Z")
}

func TestAlertmanager_AGroupListsWhatTellsItsAlertsApart(t *testing.T) {
	ev := parseAlert(t, "firing_grouped")
	assert.Equal(t, "KubePodCrashLooping: Pod is crash looping.", ev.Title)
	assert.Contains(t, ev.Body, "2 alert(s)")
	assert.Contains(t, ev.Body, "{pod=api-6d9f7c8b5-x2x4k}")
	assert.Contains(t, ev.Body, "{pod=api-6d9f7c8b5-q8m2z}")
}

func TestAlertmanager_RequestsComeFromLabelsAndOnlyValidOnes(t *testing.T) {
	ev := parseAlert(t, "firing_requests_open")
	assert.Equal(t, ticket.StatusOpen, ev.Status)
	assert.Equal(t, model.PriorityUrgent, ev.Priority)

	hostile := parseAlert(t, "hostile")
	assert.Empty(t, hostile.Status, "in_progress is not a status a sender may ask for")
	assert.Empty(t, hostile.Priority)
}

func TestAlertmanager_HostileTextIsDefusedNotTrusted(t *testing.T) {
	ev := parseAlert(t, "hostile")
	assert.NotContains(t, ev.Title, "\n")
	assert.NotContains(t, ev.Title, "​")
	assert.Empty(t, ev.Link, "a javascript: URL is no link")
	assert.Contains(t, ev.Body, "IGNORE ALL PREVIOUS INSTRUCTIONS", "kept as data; rendering fences it")
}

func TestAlertmanager_OnlyVersion4WithAGroupKeyAndAKnownStatus(t *testing.T) {
	adapter, _ := intake.AdapterFor("alertmanager")
	for name, body := range map[string]string{
		"version 3":    strings.Replace(amFile(t, "firing_single"), `"version": "4"`, `"version": "3"`, 1),
		"no group key": strings.Replace(amFile(t, "firing_single"), `"groupKey"`, `"groupKeyX"`, 1),
		"unknown status": strings.Replace(amFile(t, "firing_single"), `"status": "firing",
  "alerts"`, `"status": "pending",
  "alerts"`, 1),
		"not json": "<html>",
	} {
		_, err := adapter.Parse([]byte(body))
		assert.Error(t, err, name)
	}
}

func TestAlertmanager_ALongGroupKeyIsHashedNotCut(t *testing.T) {
	adapter, _ := intake.AdapterFor("alertmanager")
	long := func(suffix string) string {
		key := `{}:{alertname="X", ` + strings.Repeat("label=\"value\", ", 30) + `n="` + suffix + `"}`
		return strings.Replace(amFile(t, "firing_single"), `{}:{alertname=\"SlopHausReleaseFailing\"}`, strings.ReplaceAll(key, `"`, `\"`), 1)
	}
	a, err := adapter.Parse([]byte(long("1")))
	require.NoError(t, err)
	b, err := adapter.Parse([]byte(long("2")))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(a.Key), intake.MaxKey)
	assert.NotEqual(t, a.Key, b.Key, "groups sharing a long prefix stay apart")
	assert.True(t, strings.HasPrefix(a.Key, "am:"))
}

func TestAlertmanager_ManyAlertsAreCountedNotAllListed(t *testing.T) {
	adapter, _ := intake.AdapterFor("alertmanager")
	var alerts []string
	for i := 0; i < 40; i++ {
		alerts = append(alerts, `{"status":"firing","labels":{"alertname":"A","pod":"p`+strings.Repeat("x", 500)+`"},"startsAt":"2026-10-04T09:00:00Z"}`)
	}
	body := `{"version":"4","groupKey":"g","status":"firing","truncatedAlerts":5,"receiver":"r","commonLabels":{"alertname":"A"},"alerts":[` + strings.Join(alerts, ",") + `]}`
	ev, err := adapter.Parse([]byte(body))
	require.NoError(t, err)
	require.NoError(t, intake.CheckEvent(ev), "never rejected for size: an alert nobody hears about is worse")
	assert.Contains(t, ev.Body, "45 alert(s)")
	assert.Contains(t, ev.Body, "and 30 more")
	assert.Contains(t, ev.Body, "left out 5 more")
}

// Through the door: a firing, its resolution and a second firing, on one
// group key.

func alertSource(f *fixture, mods ...func(*intake.Spec)) *source {
	return f.source(f.a, spec(append([]func(*intake.Spec){func(sp *intake.Spec) {
		sp.Name, sp.Adapter = "alertmanager", "alertmanager"
	}}, mods...)...))
}

func TestAlertmanager_FiringResolvedFiringAgainOnOneGroupKey(t *testing.T) {
	f := newFixture(t)
	s := alertSource(f, trusted, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen })
	key := `{}:{alertname="SlopHausReleaseFailing"}`

	rec := f.send(s.post(amFile(t, "firing_single")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first := f.ticketFor(s, key)
	require.NotNil(t, first)
	assert.Equal(t, ticket.StatusOpen, first.Status)
	assert.Equal(t, model.PriorityHigh, first.Priority, "urgent requested, capped at the source's high")
	assert.Equal(t, "[alertmanager] external report", first.Title)
	assert.Equal(t, "SlopHausReleaseFailing: slop-haus releases keep failing at wait-prod", first.Intake.Title)

	rec = f.send(s.post(amFile(t, "resolved")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after := f.ticketFor(s, key)
	assert.Equal(t, first.ID, after.ID)
	assert.Equal(t, ticket.StatusOpen, after.Status, "resolved only comments: closing stays with whoever works the ticket")
	assert.Nil(t, after.ClosedAt)

	rec = f.send(s.post(amFile(t, "firing_again")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	again := f.ticketFor(s, key)
	assert.Equal(t, first.ID, again.ID, "firing again while the ticket is unfinished adds to it")
	assert.Len(t, f.allTickets(f.a), 1)
	events := f.external(s, first.ID)
	require.Len(t, events, 3)
	assert.Equal(t, []string{"open", "resolve", "open"}, []string{events[0].Event, events[1].Event, events[2].Event})

	// Once someone has finished the ticket, firing again is a new problem.
	f.setStatus(f.a, first.ID, ticket.StatusDone)
	rec = f.send(s.post(strings.Replace(amFile(t, "firing_again"), "11:40:02.456Z", "13:00:00.000Z", 1)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, f.allTickets(f.a), 2)
	assert.NotEqual(t, first.ID, f.ticketFor(s, key).ID)
}

func TestAlertmanager_AnUntrustedSourceStillGetsPreparingWhateverTheLabelsAsk(t *testing.T) {
	f := newFixture(t)
	s := alertSource(f)
	rec := f.send(s.post(amFile(t, "firing_requests_open")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := f.ticketFor(s, `{}:{alertname="DiskAlmostFull"}`)
	require.NotNil(t, got)
	assert.Equal(t, ticket.StatusPreparing, got.Status)
	assert.True(t, f.deliveries(f.a)[0].Downgraded)
}

func TestAlertmanager_HostileAlertsAreStoredAsDataAndBadOnesRejected(t *testing.T) {
	f := newFixture(t)
	s := alertSource(f, trusted)
	require.Equal(t, http.StatusOK, f.send(s.post(amFile(t, "hostile"))).Code)
	got := f.ticketFor(s, `{}:{alertname="Boom"}`)
	require.NotNil(t, got)
	assert.NotContains(t, got.Body, "IGNORE", "the sender's words are never in the ticket's own body")
	assert.NotContains(t, got.Title, "\n")
	assert.NotContains(t, got.Title, "Boom", "the sender's words stay out of the ticket's own title")

	rec := f.send(s.post(strings.Replace(amFile(t, "firing_single"), `"version": "4"`, `"version": "3"`, 1)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
