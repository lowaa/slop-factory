package telemetry_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/telemetry"
)

const hexSession = "0123456789abcdef0123456789abcdef"

var analyticsCatalog = telemetry.EventSpecs{
	"screen_view": {"screen": {Type: "enum", Values: []string{"dashboard", "settings"}}},
	"screen_time": {
		"screen":     {Type: "enum", Values: []string{"dashboard"}},
		"visible_ms": {Type: "int", Max: 1_800_000, Step: 5000},
	},
	"filter_used": {
		"has_text": {Type: "bool"},
		"bucket":   {Type: "enum", Values: []string{"0", "6-20"}, Optional: true},
	},
}

var interactionCatalog = telemetry.EventSpecs{
	"pointer_sample": {"region": {Type: "enum", Values: []string{"chart"}}},
}

type grants struct{ analytics, interaction bool }

func newEventPolicy(t *testing.T, g *grants) *telemetry.Policy {
	t.Helper()
	p, err := telemetry.New(&telemetry.Config{
		ServiceName:           "svc-frontend",
		OperationalAttributes: []string{"error.type"},
		SessionID:             regexp.MustCompile(`^[0-9a-f]{32}$`),
		CallerTimestamps:      true,
		Now:                   func() time.Time { return fixedNow },
		Consented: map[string]*telemetry.ConsentedTier{
			"analytics":             {Events: analyticsCatalog, Granted: func(context.Context) bool { return g.analytics }},
			"interaction_recording": {Events: interactionCatalog, Granted: func(context.Context) bool { return g.interaction }},
		},
	})
	require.NoError(t, err)
	return p
}

func intAttr(k string, v any) attr { return attr{"key": k, "value": attr{"intValue": v}} }

func event(tier, name string, extra ...attr) attr {
	attrs := append([]attr{str("privacy.tier", tier), str("event.name", name), str("session.id", hexSession)}, extra...)
	return attr{"timeUnixNano": "1700000000123456789", "severityNumber": 9, "attributes": attrs}
}

func screenView(extra ...attr) attr {
	return event("analytics", "screen_view", append([]attr{str("screen", "dashboard")}, extra...)...)
}

func apply(t *testing.T, g *grants, records ...attr) *telemetry.Result {
	t.Helper()
	res, err := newEventPolicy(t, g).Apply(context.Background(), payload(nil, records...), nil)
	require.NoError(t, err)
	return res
}

func TestEvents_NeedTheirOwnTiersGrant(t *testing.T) {
	for _, tc := range []struct {
		name        string
		g           grants
		rec         attr
		wantForward int
	}{
		{"analytics, none granted", grants{}, screenView(), 0},
		{"analytics, only interaction granted", grants{interaction: true}, screenView(), 0},
		{"analytics, granted", grants{analytics: true}, screenView(), 1},
		{"interaction, none granted", grants{}, event("interaction_recording", "pointer_sample", str("region", "chart")), 0},
		{"interaction, only analytics granted", grants{analytics: true}, event("interaction_recording", "pointer_sample", str("region", "chart")), 0},
		{"interaction, granted", grants{interaction: true}, event("interaction_recording", "pointer_sample", str("region", "chart")), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := apply(t, &tc.g, tc.rec)
			assert.Equal(t, tc.wantForward, res.Forwarded)
			assert.Equal(t, 1, res.ConsentGated)
		})
	}
}

func TestEvents_AnEventIsOnlyTheEventsOfItsOwnTier(t *testing.T) {
	g := &grants{analytics: true, interaction: true}
	assert.Zero(t, apply(t, g, event("analytics", "pointer_sample", str("region", "chart"))).Forwarded)
	assert.Zero(t, apply(t, g, event("interaction_recording", "screen_view", str("screen", "dashboard"))).Forwarded)
}

func TestEvents_AnEventClaimingToBeOperationalIsDroppedWhateverTheGrant(t *testing.T) {
	g := &grants{analytics: true, interaction: true}
	for name, rec := range map[string]attr{
		"operational":     event("operational", "screen_view", str("screen", "dashboard")),
		"padded name":     attr{"severityNumber": 17, "attributes": []attr{str("privacy.tier", "operational"), str(" Event.Name ", "screen_view")}},
		"error and event": attr{"severityNumber": 17, "attributes": []attr{str("privacy.tier", "operational"), str("event.name", "x"), str("error.type", "E")}},
	} {
		t.Run(name, func(t *testing.T) {
			rec["severityNumber"] = 17
			res := apply(t, g, rec)
			assert.Zero(t, res.Forwarded)
			assert.Equal(t, 1, res.DroppedMislabelled)
		})
	}
}

func TestEvents_ARecordWithTwoTiersIsDropped(t *testing.T) {
	g := &grants{analytics: true}
	rec := screenView(str("privacy.tier", "operational"))
	res := apply(t, g, rec)
	assert.Zero(t, res.Forwarded)
	assert.Equal(t, 1, res.DroppedMalformed)
}

func TestEvents_RecordsOutsideTheCatalogAreDropped(t *testing.T) {
	g := &grants{analytics: true}
	tier := str("privacy.tier", "analytics")
	name := str("event.name", "screen_view")
	sess := str("session.id", hexSession)
	screen := str("screen", "dashboard")
	rec := func(attrs ...attr) attr { return attr{"attributes": attrs} }
	timeRec := func(extra ...attr) attr {
		return event("analytics", "screen_time", append([]attr{str("screen", "dashboard")}, extra...)...)
	}
	cases := map[string]attr{
		"an event not in the catalog": rec(tier, str("event.name", "page_view"), sess),
		"no event name":               rec(tier, sess, screen),
		"event name not a string":     rec(tier, intAttr("event.name", "1"), sess, screen),
		"no session id":               rec(tier, name, screen),
		"session id not random hex":   rec(tier, name, str("session.id", "alice@example.com"), screen),
		"session id as a number":      rec(tier, name, intAttr("session.id", "1"), screen),
		"a missing required attr":     rec(tier, name, sess),
		"an enum off the list":        rec(tier, name, sess, str("screen", "/reports/4b2c")),
		"an enum as a number":         rec(tier, name, sess, intAttr("screen", "3")),
		"an attribute given twice":    rec(tier, name, sess, screen, str("screen", "settings")),
		"a value with two types":      rec(tier, name, sess, attr{"key": "screen", "value": attr{"stringValue": "dashboard", "intValue": "1"}}),
		"a time off the step":         timeRec(intAttr("visible_ms", "5001")),
		"a time over the cap":         timeRec(intAttr("visible_ms", "1805000")),
		"a negative time":             timeRec(intAttr("visible_ms", "-5000")),
		"a time that is text":         timeRec(str("visible_ms", "5000")),
		"a time that is a double":     timeRec(attr{"key": "visible_ms", "value": attr{"doubleValue": 5000.0}}),
	}
	for label, r := range cases {
		t.Run(label, func(t *testing.T) {
			res := apply(t, g, r)
			assert.Zero(t, res.Forwarded)
			assert.Equal(t, 1, res.DroppedMalformed)
		})
	}
}

func TestEvents_ValuesOfEveryCatalogType(t *testing.T) {
	res := apply(t, &grants{analytics: true},
		event("analytics", "screen_time", str("screen", "dashboard"), intAttr("visible_ms", 5000)),
		event("analytics", "screen_time", str("screen", "dashboard"), intAttr("visible_ms", "1800000")),
		event("analytics", "filter_used", attr{"key": "has_text", "value": attr{"boolValue": true}}, str("bucket", "6-20")),
		event("analytics", "filter_used", attr{"key": "has_text", "value": attr{"boolValue": false}}),
	)
	_, _, recs := decode(t, res.Body)
	require.Len(t, recs, 4)
	assert.Equal(t, "5000", recs[0].Attributes["visible_ms"])
	assert.Equal(t, "1800000", recs[1].Attributes["visible_ms"])
	assert.Equal(t, "true", recs[2].Attributes["has_text"])
	assert.Equal(t, "6-20", recs[2].Attributes["bucket"])
	assert.NotContains(t, recs[3].Attributes, "bucket", "an optional attribute may be absent")
}

func TestEvents_CarryNothingButTheCatalog(t *testing.T) {
	forged := screenView(
		str("user.id", "7d1e0000-0000-4000-8000-000000000001"),
		str("tenant.id", "tenant-from-record"),
		str("account_id", "acct-from-record"),
		str("email", "alice@example.com"),
		str("http.route", "/reports/4b2c"),
	)
	forged["body"] = attr{"stringValue": "alice@example.com opened /reports/4b2c"}
	forged["traceId"] = "4bf92f3577b34da6a3ce929d0e0e4736"
	forged["severityText"] = "ALICE"
	forged["user"] = "alice"

	res, err := newEventPolicy(t, &grants{analytics: true}).Apply(context.Background(),
		payload([]attr{str("user.id", "u-resource"), str("service.name", "other")}, forged), nil)
	require.NoError(t, err)

	for _, leaked := range []string{"7d1e0000", "tenant-from", "acct-from", "alice", "4bf92f35", "4b2c", "u-resource", "other", "user", "tenant", "account", "ALICE"} {
		assert.NotContains(t, string(res.Body), leaked)
	}
	_, _, recs := decode(t, res.Body)
	require.Len(t, recs, 1)
	assert.Equal(t, map[string]string{
		"privacy.tier": "analytics", "event.name": "screen_view", "session.id": hexSession, "screen": "dashboard",
	}, recs[0].Attributes)
	assert.Equal(t, "screen_view", recs[0].Body)
	assert.Equal(t, 5, res.StrippedAttributes)
}

func TestEvents_CallerTimestampsAreKeptOnlyWhenPlainNanos(t *testing.T) {
	res := apply(t, &grants{analytics: true}, screenView())
	_, _, recs := decode(t, res.Body)
	assert.Equal(t, "1700000000123456789", recs[0].Time, "64-bit nanoseconds stay exact")

	bad := screenView()
	bad["timeUnixNano"] = "alice@example.com"
	bad["observedTimeUnixNano"] = 1700000000123456790
	res = apply(t, &grants{analytics: true}, bad)
	_, _, recs = decode(t, res.Body)
	assert.Equal(t, "1700000000000000000", recs[0].Time, "text is never kept: the server's time stands in")
	assert.Equal(t, "1700000000123456790", recs[0].Raw["observedTimeUnixNano"], "a number as OTLP/JSON may write it")
	assert.NotContains(t, string(res.Body), "alice")
}

func TestNew_ValidatesEventCatalogs(t *testing.T) {
	granted := func(context.Context) bool { return true }
	build := func(specs telemetry.EventSpecs, extra ...*telemetry.ConsentedTier) error {
		tiers := map[string]*telemetry.ConsentedTier{"analytics": {Events: specs, Granted: granted}}
		for i, e := range extra {
			tiers[fmt.Sprintf("t%d", i)] = e
		}
		_, err := telemetry.New(&telemetry.Config{ServiceName: "s", Consented: tiers})
		return err
	}
	require.NoError(t, build(analyticsCatalog))

	for name, specs := range map[string]telemetry.EventSpecs{
		"empty":                       {},
		"sets the tier itself":        {"e": {"privacy.tier": {Type: "bool"}}},
		"sets the session itself":     {"e": {"session.id": {Type: "bool"}}},
		"sets the event name":         {"e": {"event.name": {Type: "bool"}}},
		"identifies an account":       {"e": {"user.id": {Type: "bool"}}},
		"identifies a tenant":         {"e": {"tenant.id": {Type: "bool"}}},
		"an enum with no values":      {"e": {"a": {Type: "enum"}}},
		"an enum value over the clip": {"e": {"a": {Type: "enum", Values: []string{strings.Repeat("x", 129)}}}},
		"an int with no bound":        {"e": {"a": {Type: "int", Step: 1}}},
		"an int with no step":         {"e": {"a": {Type: "int", Max: 1}}},
		"a type nothing implements":   {"e": {"a": {Type: "string"}}},
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, build(specs)) })
	}

	assert.Error(t, build(analyticsCatalog, &telemetry.ConsentedTier{Events: analyticsCatalog, Granted: granted}), "one event in two tiers")
	_, err := telemetry.New(&telemetry.Config{ServiceName: "s", Consented: map[string]*telemetry.ConsentedTier{
		"analytics": {Events: analyticsCatalog, Attributes: []string{"x"}, Granted: granted},
	}})
	assert.Error(t, err, "event tier and attribute tier at once")
}

func TestEvents_SeverityIsAnOtlpLevelWhateverWasSent(t *testing.T) {
	g := &grants{analytics: true}
	for sent, want := range map[int]int{-5: 0, 9: 9, 99: 24, 1 << 40: 24} {
		rec := screenView()
		rec["severityNumber"] = sent
		_, _, recs := decode(t, apply(t, g, rec).Body)
		assert.Equal(t, want, recs[0].Severity, "sent %d", sent)
	}
}

func TestEvents_ARecordWithTooManyAttributesIsDropped(t *testing.T) {
	var extra []attr
	for i := 0; i < 70; i++ {
		extra = append(extra, str(fmt.Sprintf("k%d", i), "v"))
	}
	res := apply(t, &grants{analytics: true}, screenView(extra...))
	assert.Zero(t, res.Forwarded)
	assert.Equal(t, 1, res.DroppedOverLimit)
}

func TestEvents_ThePolicyHasItsOwnCopyOfTheCatalog(t *testing.T) {
	catalog := telemetry.EventSpecs{"e": {"a": {Type: "enum", Values: []string{"ok"}}}}
	p, err := telemetry.New(&telemetry.Config{ServiceName: "s", SessionID: regexp.MustCompile(`^[0-9a-f]{32}$`), Consented: map[string]*telemetry.ConsentedTier{
		"analytics": {Events: catalog, Granted: func(context.Context) bool { return true }},
	}})
	require.NoError(t, err)

	catalog["e"]["a"].Values[0] = "evil"
	catalog["other"] = map[string]telemetry.AttrSpec{}

	res, err := p.Apply(context.Background(), payload(nil, event("analytics", "e", str("a", "ok")), event("analytics", "e", str("a", "evil")), event("analytics", "other")), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Forwarded, "only what the catalog said when the policy was built")
}
