// Package telemetry is the server-side gate on the browser's OTLP/JSON log
// export (go-privacy/README.md §8). The browser labels each record with a
// privacy.tier, but that label is only the app's own say-so, so the proxy
// does not trust it: it rebuilds the payload from the records and
// attributes the tier's policy permits and discards everything else.
//
// Both apps' proxies share it; an app supplies only its Config.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	TierAttribute      = "privacy.tier"
	SessionIDAttribute = "session.id"
	ServiceNameKey     = "service.name"
	UserIDAttribute    = "user.id"
	EventNameAttribute = "event.name"

	// TierOperational is always on and needs no consent.
	TierOperational = "operational"

	// SeverityError is OTLP's SEVERITY_NUMBER_ERROR: the operational tier
	// is errors only, with no breadcrumb stream.
	SeverityError = 17

	maxSeverity = 24
)

// Bounds, so a payload can't be amplified into more than it was: the
// output is built from these, not copied from the input.
const (
	maxRecords       = 100
	maxAttrsScanned  = 64
	maxBodyLen       = 1024
	maxValueLen      = 16 * 1024
	maxKeyLen        = 128
	maxSessionIDLen  = 64
	maxResourceLogs  = 8
	maxScopeLogsEach = 8
)

var defaultSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

var nanosPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// identityKeys may never be allowed on a consented tier: analytics and
// interaction records carry nothing that maps back to an account.
var identityKeys = map[string]bool{UserIDAttribute: true, "tenant.id": true, "enduser.id": true}

// ConsentedTier is a tier that needs the visitor's consent -- its id is the
// consent purpose's.
type ConsentedTier struct {
	// Attributes are the keys a record of this tier may carry, besides the
	// tier and session id. Never an identity key.
	Attributes []string
	// Events, when set, makes this an event tier instead: a record passes
	// only as one of these events (named by event.name, with a session id),
	// rebuilt from the catalog's typed attributes alone. Exclusive with
	// Attributes.
	Events EventSpecs
	// Granted reports whether the purpose is effective for the request.
	Granted func(ctx context.Context) bool
}

// Config says what may pass.
type Config struct {
	// ServiceName is forced as the payload's service.name, whatever the
	// caller sent, so it can never pass as another service's logs.
	ServiceName string
	// OperationalAttributes are the keys an operational record may carry,
	// besides the tier and the session id.
	OperationalAttributes []string
	// Consented are the tiers that pass only with consent, by tier id.
	Consented map[string]*ConsentedTier
	// SessionID is what a session.id value must match to be kept; nil means
	// letters, digits and dashes up to 64. Event tiers drop a record whose
	// session id doesn't match.
	SessionID *regexp.Regexp
	// CallerTimestamps keeps a record's own time (when it is a plain
	// unix-nanosecond count) instead of stamping it, for apps whose events
	// are timed in the browser. The server's time is used for any that is
	// not.
	CallerTimestamps bool
	// Now stamps records; nil means time.Now.
	Now func() time.Time
}

// Policy applies a Config. Immutable once built, so safe for concurrent use.
type Policy struct {
	serviceName      string
	now              func() time.Time
	sessionID        *regexp.Regexp
	callerTimestamps bool
	operational      map[string]bool
	consented        map[string]*consentedTier
}

type consentedTier struct {
	attributes map[string]bool
	events     EventSpecs
	granted    func(ctx context.Context) bool
}

// New checks cfg, so a policy that can never work is found at startup.
func New(cfg *Config) (*Policy, error) {
	if cfg.ServiceName == "" {
		return nil, errors.New("telemetry: policy has no service name")
	}
	p := &Policy{
		serviceName:      cfg.ServiceName,
		now:              cfg.Now,
		sessionID:        cfg.SessionID,
		callerTimestamps: cfg.CallerTimestamps,
		operational:      toSet(cfg.OperationalAttributes),
		consented:        map[string]*consentedTier{},
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.sessionID == nil {
		p.sessionID = defaultSessionIDPattern
	}
	eventOwner := map[string]string{}
	if p.operational[UserIDAttribute] {
		return nil, fmt.Errorf("telemetry: %s is set from the verified caller, not allowed from the payload", UserIDAttribute)
	}
	for _, k := range cfg.OperationalAttributes {
		if identityKeys[k] {
			return nil, fmt.Errorf("telemetry: operational attributes may not include identity attribute %q: the payload can't be trusted to name an account", k)
		}
	}
	for tier, ct := range cfg.Consented {
		if tier == TierOperational || tier == "" || ct == nil || ct.Granted == nil {
			return nil, fmt.Errorf("telemetry: invalid consented tier %q", tier)
		}
		for _, k := range ct.Attributes {
			if identityKeys[k] {
				return nil, fmt.Errorf("telemetry: tier %q may not allow identity attribute %q", tier, k)
			}
		}
		if ct.Events != nil {
			if len(ct.Attributes) > 0 {
				return nil, fmt.Errorf("telemetry: tier %q is either an event tier or an attribute tier", tier)
			}
			if err := ct.Events.Validate(); err != nil {
				return nil, fmt.Errorf("telemetry: tier %q: %w", tier, err)
			}
			for name := range ct.Events {
				if other, dup := eventOwner[name]; dup {
					return nil, fmt.Errorf("telemetry: event %q is in both tier %q and tier %q", name, other, tier)
				}
				eventOwner[name] = tier
			}
		}
		p.consented[tier] = &consentedTier{attributes: toSet(ct.Attributes), events: ct.Events.clone(), granted: ct.Granted}
	}
	return p, nil
}

func toSet(keys []string) map[string]bool {
	s := make(map[string]bool, len(keys))
	for _, k := range keys {
		s[k] = true
	}
	return s
}

// Result is what Apply let through, and what it did not.
type Result struct {
	// Body is the OTLP/JSON to forward; nil when no record passed.
	Body []byte
	// Forwarded is the number of records in Body.
	Forwarded int
	// Dropped counts records, by why.
	DroppedNoTier, DroppedUnknownTier, DroppedSeverity, DroppedConsent, DroppedMalformed, DroppedOverLimit int
	// DroppedMislabelled counts operational records that name an event: the
	// browser does that only for an event tier's record, so one calling
	// itself operational is trying to get round that tier's consent.
	DroppedMislabelled int
	// ConsentGated counts the records that needed a grant, however they
	// ended up, so a caller can tell a batch that depended on who sent it.
	ConsentGated int
	// StrippedAttributes counts attributes removed from records that passed.
	StrippedAttributes int
}

// TotalDropped is every record not forwarded, for whatever reason.
func (r *Result) TotalDropped() int {
	return r.DroppedNoTier + r.DroppedUnknownTier + r.DroppedSeverity + r.DroppedConsent +
		r.DroppedMalformed + r.DroppedOverLimit + r.DroppedMislabelled
}

// ErrMalformed is a body that is not an OTLP/JSON logs document.
var ErrMalformed = errors.New("telemetry: not an OTLP/JSON logs payload")

type anyValue struct {
	StringValue *string         `json:"stringValue"`
	IntValue    json.RawMessage `json:"intValue"`
	DoubleValue *float64        `json:"doubleValue"`
	BoolValue   *bool           `json:"boolValue"`
	// Anything else (arrays, maps, bytes) is not a scalar and not kept.
	ArrayValue  json.RawMessage `json:"arrayValue"`
	KvlistValue json.RawMessage `json:"kvlistValue"`
	BytesValue  json.RawMessage `json:"bytesValue"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type inRecord struct {
	TimeUnixNano         json.RawMessage `json:"timeUnixNano"`
	ObservedTimeUnixNano json.RawMessage `json:"observedTimeUnixNano"`
	SeverityNumber       int             `json:"severityNumber"`
	Body                 anyValue        `json:"body"`
	Attributes           []keyValue      `json:"attributes"`
}

type inScope struct {
	LogRecords []json.RawMessage `json:"logRecords"`
}

type inResource struct {
	ScopeLogs []inScope `json:"scopeLogs"`
}

type inPayload struct {
	ResourceLogs []inResource `json:"resourceLogs"`
}

type outValue map[string]any

type outKV struct {
	Key   string   `json:"key"`
	Value outValue `json:"value"`
}

type outRecord struct {
	TimeUnixNano         string   `json:"timeUnixNano"`
	ObservedTimeUnixNano string   `json:"observedTimeUnixNano"`
	SeverityNumber       int      `json:"severityNumber"`
	SeverityText         string   `json:"severityText,omitempty"`
	Body                 outValue `json:"body"`
	Attributes           []outKV  `json:"attributes"`
}

// Caller is what the server knows of who is sending, which the payload
// can't be trusted to say.
type Caller struct {
	// UserID is the verified signed-in user, if any. It becomes the
	// operational records' user.id: a browser-supplied user.id is dropped,
	// so a report can't be attributed to someone else's account.
	UserID string
	// ConsentUnknown means who the visitor is can't be established -- a
	// credential was presented and refused (authn.CredentialRejected) -- so
	// a record that needs consent fails closed rather than being judged as
	// an anonymous visitor's.
	ConsentUnknown bool
}

// Apply filters body per the policy.
func (p *Policy) Apply(ctx context.Context, body []byte, caller *Caller) (*Result, error) {
	if caller == nil {
		caller = &Caller{}
	}
	var in inPayload
	if err := json.Unmarshal(body, &in); err != nil || in.ResourceLogs == nil {
		return nil, ErrMalformed
	}

	stamp := strconv.FormatInt(p.now().UnixNano(), 10)

	res := &Result{}
	var kept []outRecord
	seen := 0
	for ri, rl := range in.ResourceLogs {
		if ri >= maxResourceLogs {
			res.DroppedOverLimit++
			continue
		}
		for si, sl := range rl.ScopeLogs {
			if si >= maxScopeLogsEach {
				res.DroppedOverLimit++
				continue
			}
			for _, raw := range sl.LogRecords {
				seen++
				if seen > maxRecords {
					res.DroppedOverLimit++
					continue
				}
				var rec inRecord
				if err := json.Unmarshal(raw, &rec); err != nil {
					res.DroppedMalformed++
					continue
				}
				if out, ok := p.filterRecord(ctx, &rec, stamp, caller, res); ok {
					kept = append(kept, *out)
				}
			}
		}
	}
	res.Forwarded = len(kept)
	if len(kept) == 0 {
		return res, nil
	}
	doc := map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": []outKV{{Key: ServiceNameKey, Value: outValue{"stringValue": p.serviceName}}}},
		"scopeLogs": []any{map[string]any{
			"scope":      map[string]any{"name": p.serviceName},
			"logRecords": kept,
		}},
	}}}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	res.Body = out
	return res, nil
}

func (p *Policy) filterRecord(ctx context.Context, rec *inRecord, stamp string, caller *Caller, res *Result) (*outRecord, bool) {
	tier, ok, ambiguous := tierOf(rec)
	switch {
	case ambiguous:
		// Two tiers leave no reading of the record that is safe to pick.
		res.DroppedMalformed++
		return nil, false
	case !ok:
		res.DroppedNoTier++
		return nil, false
	}

	var ct *consentedTier
	var allowed map[string]bool
	if tier == TierOperational {
		if rec.SeverityNumber < SeverityError {
			res.DroppedSeverity++
			return nil, false
		}
		if hasKey(rec, EventNameAttribute) {
			res.DroppedMislabelled++
			return nil, false
		}
		allowed = p.operational
	} else {
		var known bool
		ct, known = p.consented[tier]
		if !known {
			res.DroppedUnknownTier++
			return nil, false
		}
		res.ConsentGated++
		if caller.ConsentUnknown || !ct.granted(ctx) {
			res.DroppedConsent++
			return nil, false
		}
		allowed = ct.attributes
	}

	// OTLP's severity numbers run 1..24 (0 is unspecified); anything else is
	// the caller's text in numeric form.
	severity := min(max(rec.SeverityNumber, 0), maxSeverity)
	out := &outRecord{
		TimeUnixNano:         p.stampOf(rec.TimeUnixNano, stamp),
		ObservedTimeUnixNano: p.stampOf(rec.ObservedTimeUnixNano, stamp),
		SeverityNumber:       severity,
		Body:                 outValue{"stringValue": ""},
		Attributes:           []outKV{},
	}
	out.SeverityText = severityText(severity)
	if s := rec.Body.StringValue; s != nil {
		out.Body = outValue{"stringValue": clip(*s, maxBodyLen)}
	}

	if ct != nil && ct.events != nil {
		return p.buildEvent(ct, tier, rec, out, res)
	}

	have := map[string]bool{}
	scanned := 0
	for _, kv := range rec.Attributes {
		scanned++
		if scanned > maxAttrsScanned {
			res.StrippedAttributes++
			continue
		}
		switch {
		case kv.Key == TierAttribute:
			continue
		case have[kv.Key] || len(kv.Key) > maxKeyLen:
			res.StrippedAttributes++
			continue
		case kv.Key == SessionIDAttribute:
			if s := kv.Value.StringValue; s != nil && p.sessionID.MatchString(*s) {
				have[kv.Key] = true
				out.Attributes = append(out.Attributes, outKV{Key: kv.Key, Value: outValue{"stringValue": *s}})
			} else {
				res.StrippedAttributes++
			}
			continue
		case !allowed[kv.Key]:
			res.StrippedAttributes++
			continue
		}
		v, ok := scalar(&kv.Value)
		if !ok {
			res.StrippedAttributes++
			continue
		}
		have[kv.Key] = true
		out.Attributes = append(out.Attributes, outKV{Key: kv.Key, Value: v})
	}
	// Set by the filter, last, so nothing above can have written them.
	if tier == TierOperational && caller.UserID != "" {
		out.Attributes = append(out.Attributes, outKV{Key: UserIDAttribute, Value: outValue{"stringValue": caller.UserID}})
	}
	out.Attributes = append(out.Attributes, outKV{Key: TierAttribute, Value: outValue{"stringValue": tier}})
	return out, true
}

// buildEvent rebuilds a record of an event tier from its catalog entry. A
// record that is not a well-formed instance of an event of the tier is
// dropped whole rather than trimmed: an event with a value off the catalog
// is not the event the browser means to send.
func (p *Policy) buildEvent(ct *consentedTier, tier string, rec *inRecord, out *outRecord, res *Result) (*outRecord, bool) {
	if len(rec.Attributes) > maxAttrsScanned {
		res.DroppedOverLimit++
		return nil, false
	}
	vals := map[string]*anyValue{}
	for i := range rec.Attributes {
		kv := &rec.Attributes[i]
		if kv.Key == TierAttribute {
			continue
		}
		if _, dup := vals[kv.Key]; dup {
			res.DroppedMalformed++
			return nil, false
		}
		vals[kv.Key] = &kv.Value
	}

	nameVal, ok := vals[EventNameAttribute]
	if !ok || !nameVal.onlyString() {
		res.DroppedMalformed++
		return nil, false
	}
	name := *nameVal.StringValue
	spec, known := ct.events[name]
	if !known {
		res.DroppedMalformed++
		return nil, false
	}
	sessVal, ok := vals[SessionIDAttribute]
	if !ok || !sessVal.onlyString() || !p.sessionID.MatchString(*sessVal.StringValue) {
		res.DroppedMalformed++
		return nil, false
	}

	out.Body = outValue{"stringValue": name}
	out.Attributes = append(out.Attributes,
		outKV{Key: EventNameAttribute, Value: outValue{"stringValue": name}},
		outKV{Key: SessionIDAttribute, Value: outValue{"stringValue": *sessVal.StringValue}},
	)
	for attr, s := range spec {
		v, present := vals[attr]
		if !present {
			if s.Optional {
				continue
			}
			res.DroppedMalformed++
			return nil, false
		}
		outV, ok := s.accept(v)
		if !ok {
			res.DroppedMalformed++
			return nil, false
		}
		out.Attributes = append(out.Attributes, outKV{Key: attr, Value: outV})
	}
	// Attributes the catalog doesn't name are left out, so what the
	// browser's own filter guarantees holds here whatever was sent.
	for key := range vals {
		if _, named := spec[key]; !named && key != EventNameAttribute && key != SessionIDAttribute {
			res.StrippedAttributes++
		}
	}
	out.Attributes = append(out.Attributes, outKV{Key: TierAttribute, Value: outValue{"stringValue": tier}})
	return out, true
}

func (p *Policy) stampOf(raw json.RawMessage, stamp string) string {
	if !p.callerTimestamps || raw == nil {
		return stamp
	}
	// OTLP/JSON writes a uint64 as a string, though a number is also seen.
	s := strings.Trim(string(raw), `"`)
	if !nanosPattern.MatchString(s) {
		return stamp
	}
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return stamp
	}
	return s
}

func severityText(n int) string {
	switch {
	case n >= SeverityError:
		return "ERROR"
	case n >= 13:
		return "WARN"
	default:
		return "INFO"
	}
}

func hasKey(rec *inRecord, key string) bool {
	for i := range rec.Attributes {
		if strings.EqualFold(strings.TrimSpace(rec.Attributes[i].Key), key) {
			return true
		}
	}
	return false
}

// tierOf reads the record's tier. A tier given more than once is ambiguous.
func tierOf(rec *inRecord) (tier string, ok, ambiguous bool) {
	seen := 0
	for _, kv := range rec.Attributes {
		if kv.Key != TierAttribute {
			continue
		}
		seen++
		if s := kv.Value.StringValue; seen == 1 && s != nil && *s != "" && len(*s) <= maxKeyLen {
			tier, ok = *s, true
		}
	}
	return tier, ok && seen == 1, seen > 1
}

func (v *anyValue) onlyString() bool {
	if v.StringValue == nil {
		return false
	}
	_, ok := scalar(v)
	return ok
}

// scalar re-emits exactly one scalar value, rebuilt rather than copied, so
// nothing else rides along with it.
func scalar(v *anyValue) (outValue, bool) {
	if v.ArrayValue != nil || v.KvlistValue != nil || v.BytesValue != nil {
		return nil, false
	}
	n := 0
	var out outValue
	if v.StringValue != nil {
		n++
		out = outValue{"stringValue": clip(*v.StringValue, maxValueLen)}
	}
	if v.BoolValue != nil {
		n++
		out = outValue{"boolValue": *v.BoolValue}
	}
	if v.DoubleValue != nil {
		n++
		out = outValue{"doubleValue": *v.DoubleValue}
	}
	if v.IntValue != nil {
		n++
		i, err := strconv.ParseInt(strings.Trim(string(v.IntValue), `"`), 10, 64)
		if err != nil {
			return nil, false
		}
		out = outValue{"intValue": strconv.FormatInt(i, 10)}
	}
	return out, n == 1
}

// clip bounds s to n bytes, on a rune boundary and valid UTF-8.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
