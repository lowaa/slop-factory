package telemetry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/telemetry"
)

var fixedNow = time.Unix(1_700_000_000, 0)

type attr = map[string]any

func str(k, v string) attr { return attr{"key": k, "value": attr{"stringValue": v}} }

func record(severity int, attrs ...attr) attr {
	return attr{
		"timeUnixNano":         "1",
		"observedTimeUnixNano": "2",
		"severityNumber":       severity,
		"body":                 attr{"stringValue": "boom"},
		"attributes":           attrs,
		"traceId":              "deadbeef",
	}
}

func payload(resourceAttrs []attr, records ...attr) []byte {
	b, _ := json.Marshal(attr{"resourceLogs": []any{attr{
		"resource":  attr{"attributes": resourceAttrs},
		"scopeLogs": []any{attr{"scope": attr{"name": "evil"}, "logRecords": records}},
	}}})
	return b
}

func newPolicy(t *testing.T, granted func(context.Context) bool) *telemetry.Policy {
	t.Helper()
	cfg := &telemetry.Config{
		ServiceName:           "svc-frontend",
		OperationalAttributes: []string{"error.type", "http.route"},
		Now:                   func() time.Time { return fixedNow },
	}
	if granted != nil {
		cfg.Consented = map[string]*telemetry.ConsentedTier{"analytics": {Attributes: []string{"screen"}, Granted: granted}}
	}
	p, err := telemetry.New(cfg)
	require.NoError(t, err)
	return p
}

type outRec struct {
	Body       string
	Severity   int
	Time       string
	Attributes map[string]string
	Raw        map[string]any
}

func decode(t *testing.T, body []byte) (resource map[string]string, scope string, recs []outRec) {
	t.Helper()
	var doc struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []struct {
					Key   string
					Value map[string]any
				}
			}
			ScopeLogs []struct {
				Scope      struct{ Name string }
				LogRecords []map[string]any
			}
		}
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	require.Len(t, doc.ResourceLogs, 1)
	resource = map[string]string{}
	for _, a := range doc.ResourceLogs[0].Resource.Attributes {
		resource[a.Key], _ = a.Value["stringValue"].(string)
	}
	require.Len(t, doc.ResourceLogs[0].ScopeLogs, 1)
	scope = doc.ResourceLogs[0].ScopeLogs[0].Scope.Name
	for _, r := range doc.ResourceLogs[0].ScopeLogs[0].LogRecords {
		o := outRec{Attributes: map[string]string{}, Raw: r}
		o.Body = r["body"].(map[string]any)["stringValue"].(string)
		o.Severity = int(r["severityNumber"].(float64))
		o.Time = r["timeUnixNano"].(string)
		for _, a := range r["attributes"].([]any) {
			kv := a.(map[string]any)
			for _, v := range kv["value"].(map[string]any) {
				o.Attributes[kv["key"].(string)] = toS(v)
			}
		}
		recs = append(recs, o)
	}
	return resource, scope, recs
}

func toS(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func TestApply_ForcesServiceNameAndRebuildsResource(t *testing.T) {
	p := newPolicy(t, nil)
	res, err := p.Apply(context.Background(), payload(
		[]attr{str("service.name", "slop-haus-api"), str("host.name", "secret-node"), str("deployment.environment", "x")},
		record(17, str("privacy.tier", "operational")),
	), nil)
	require.NoError(t, err)
	resource, scope, recs := decode(t, res.Body)
	assert.Equal(t, map[string]string{"service.name": "svc-frontend"}, resource, "no client resource attribute may survive")
	assert.Equal(t, "svc-frontend", scope)
	require.Len(t, recs, 1)
	assert.NotContains(t, recs[0].Raw, "traceId")
}

func TestApply_OperationalAllowlistStripsEverythingElse(t *testing.T) {
	p := newPolicy(t, nil)
	res, err := p.Apply(context.Background(), payload(nil, record(17,
		str("privacy.tier", "operational"),
		str("error.type", "TypeError"),
		str("http.route", "/tickets/{id}"),
		str("user.id", "u1"),
		str("session.id", "abc-123"),
		str("form.value", "Hemoglobin 13.5 for Jane Doe"),
		str("file.name", "results.pdf"),
		attr{"key": "error.type", "value": attr{"stringValue": "second wins nothing"}},
		attr{"key": "http.route", "value": attr{"arrayValue": attr{"values": []any{}}}},
	)), nil)
	require.NoError(t, err)
	_, _, recs := decode(t, res.Body)
	require.Len(t, recs, 1)
	assert.Equal(t, map[string]string{
		"privacy.tier": "operational", "error.type": "TypeError", "http.route": "/tickets/{id}",
		"session.id": "abc-123",
	}, recs[0].Attributes)
	assert.Equal(t, 5, res.StrippedAttributes, "user.id, form.value, file.name, duplicate error.type, array http.route")
	assert.NotContains(t, string(res.Body), "Hemoglobin")
}

func TestApply_UserIDComesFromTheVerifiedCaller(t *testing.T) {
	p := newPolicy(t, nil)
	// A report claiming to be about someone else.
	body := payload(nil, record(17, str("privacy.tier", "operational"), str("user.id", "someone-else")))

	res, err := p.Apply(context.Background(), body, &telemetry.Caller{UserID: "verified-user"})
	require.NoError(t, err)
	_, _, recs := decode(t, res.Body)
	assert.Equal(t, "verified-user", recs[0].Attributes["user.id"])
	assert.NotContains(t, string(res.Body), "someone-else")

	res, err = p.Apply(context.Background(), body, nil)
	require.NoError(t, err)
	_, _, recs = decode(t, res.Body)
	assert.NotContains(t, recs[0].Attributes, "user.id", "an anonymous caller has no user id")
	assert.NotContains(t, string(res.Body), "someone-else")
}

func TestApply_OperationalIsErrorsOnly(t *testing.T) {
	p := newPolicy(t, nil)
	res, err := p.Apply(context.Background(), payload(nil,
		record(9, str("privacy.tier", "operational"), str("error.type", "info")),
		record(13, str("privacy.tier", "operational"), str("error.type", "warn")),
		record(17, str("privacy.tier", "operational"), str("error.type", "error")),
	), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Forwarded)
	assert.Equal(t, 2, res.DroppedSeverity)
	_, _, recs := decode(t, res.Body)
	require.Len(t, recs, 1)
	assert.Equal(t, 17, recs[0].Severity)
	assert.Equal(t, "error", recs[0].Attributes["error.type"])
}

func TestApply_MissingOrUnknownTierFailsClosed(t *testing.T) {
	p := newPolicy(t, nil)
	res, err := p.Apply(context.Background(), payload(nil,
		record(17),
		record(17, str("privacy.tier", "nonsense")),
		record(17, str("privacy.tier", "analytics")),
		record(17, attr{"key": "privacy.tier", "value": attr{"intValue": "1"}}),
	), nil)
	require.NoError(t, err)
	assert.Nil(t, res.Body)
	assert.Equal(t, 2, res.DroppedNoTier)
	assert.Equal(t, 2, res.DroppedUnknownTier)
}

func TestApply_ConsentedTier(t *testing.T) {
	granted := false
	p := newPolicy(t, func(context.Context) bool { return granted })
	body := payload(nil, record(9, str("privacy.tier", "analytics"), str("screen", "board"), str("user.id", "u1")))

	res, err := p.Apply(context.Background(), body, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.DroppedConsent, "no consent, no record")

	granted = true
	res, err = p.Apply(context.Background(), body, nil)
	require.NoError(t, err)
	_, _, recs := decode(t, res.Body)
	require.Len(t, recs, 1)
	assert.Equal(t, map[string]string{"privacy.tier": "analytics", "screen": "board"}, recs[0].Attributes, "no user id on a consented tier")

	res, err = p.Apply(context.Background(), body, &telemetry.Caller{ConsentUnknown: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.DroppedConsent, "consent unknown (credential refused) fails closed even when granted")
}

func TestNew_RejectsIdentityOnConsentedTier(t *testing.T) {
	_, err := telemetry.New(&telemetry.Config{ServiceName: "s", Consented: map[string]*telemetry.ConsentedTier{
		"analytics": {Attributes: []string{"user.id"}, Granted: func(context.Context) bool { return true }},
	}})
	assert.Error(t, err)
	_, err = telemetry.New(&telemetry.Config{ServiceName: "s", Consented: map[string]*telemetry.ConsentedTier{
		"operational": {Granted: func(context.Context) bool { return true }},
	}})
	assert.Error(t, err, "operational can't be redefined as consent-gated")
	_, err = telemetry.New(&telemetry.Config{})
	assert.Error(t, err)
	_, err = telemetry.New(&telemetry.Config{ServiceName: "s", OperationalAttributes: []string{"user.id"}})
	assert.Error(t, err, "user.id is the verified caller's, not the payload's")
	for _, key := range []string{"tenant.id", "enduser.id"} {
		_, err = telemetry.New(&telemetry.Config{ServiceName: "s", OperationalAttributes: []string{key}})
		assert.Error(t, err, "%s would be the payload's word for who the report is about", key)
	}
}

func TestApply_TimestampsAreTheServers(t *testing.T) {
	p := newPolicy(t, nil)
	res, err := p.Apply(context.Background(), payload(nil, record(17, str("privacy.tier", "operational"))), nil)
	require.NoError(t, err)
	_, _, recs := decode(t, res.Body)
	assert.Equal(t, "1700000000000000000", recs[0].Time)
	assert.Equal(t, "1700000000000000000", recs[0].Raw["observedTimeUnixNano"])
}

func TestApply_BoundsSizeAndCount(t *testing.T) {
	p := newPolicy(t, nil)
	var recs []attr
	for i := 0; i < 150; i++ {
		recs = append(recs, record(17, str("privacy.tier", "operational"), str("error.type", strings.Repeat("é", 20000))))
	}
	res, err := p.Apply(context.Background(), payload(nil, recs...), nil)
	require.NoError(t, err)
	assert.Equal(t, 100, res.Forwarded)
	assert.Equal(t, 50, res.DroppedOverLimit)
	_, _, out := decode(t, res.Body)
	assert.LessOrEqual(t, len(out[0].Attributes["error.type"]), 16*1024)
}

func TestApply_Malformed(t *testing.T) {
	p := newPolicy(t, nil)
	for _, body := range []string{`not json`, `{}`, `{"resourceLogs":"x"}`, `[]`} {
		_, err := p.Apply(context.Background(), []byte(body), nil)
		assert.ErrorIs(t, err, telemetry.ErrMalformed, body)
	}
	res, err := p.Apply(context.Background(), []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":["x", 3]}]}]}`), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.DroppedMalformed)
}
