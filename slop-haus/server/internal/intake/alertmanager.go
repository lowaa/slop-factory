package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// alertmanagerAdapter reads Alertmanager's webhook payload, version 4:
//
//   - groupKey is the key: one ticket per alert group.
//   - status firing is an open -- which adds to the group's ticket while it
//     is unfinished, so a re-notification or a new alert in the group is an
//     update -- and resolved is a resolve, a comment that closes nothing.
//   - the common labels and annotations make the title, body and context;
//     generatorURL, or else externalURL, the link.
//   - the labels slophaus_status and slophaus_priority are requests, judged
//     by the source's trust and caps like a native payload's.
//
// Alertmanager sends whatever the monitored systems label their alerts
// with, so nothing here is trusted: it is truncated to the same limits and
// shown as external content like any sender's text. Unlike the native
// adapter it never rejects over size -- an alert that cannot be shortened
// would be one nobody hears about -- and ignores fields it does not know,
// since Alertmanager adds them.
type alertmanagerAdapter struct{}

// maxListedAlerts bounds the alerts a body lists; the rest are counted.
const maxListedAlerts = 10

type amPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []amAlert         `json:"alerts"`
}

type amAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

func (alertmanagerAdapter) Parse(body []byte) (*Event, error) {
	var p amPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("not an Alertmanager webhook: %w", err)
	}
	if p.Version != "4" {
		return nil, errors.New("only Alertmanager's webhook payload version 4 is supported")
	}
	if strings.TrimSpace(p.GroupKey) == "" {
		return nil, errors.New("the payload has no groupKey")
	}
	kind := EventOpen
	switch p.Status {
	case "firing":
	case "resolved":
		kind = EventResolve
	default:
		return nil, errors.New("status must be firing or resolved")
	}

	ev := &Event{
		Key:   alertKey(p.GroupKey),
		Kind:  kind,
		Title: Truncate(alertTitle(&p), MaxTitle),
		Body:  Truncate(alertBody(&p), MaxBody),
		Link:  alertLink(&p),
	}
	if s := ticket.Status(p.CommonLabels["slophaus_status"]); s == ticket.StatusOpen || s == ticket.StatusPreparing {
		ev.Status = s
	}
	if pr := model.Priority(p.CommonLabels["slophaus_priority"]); pr.Valid() {
		ev.Priority = pr
	}
	ctx, err := alertContext(&p)
	if err != nil {
		return nil, err
	}
	ev.Context = ctx
	return ev, nil
}

// alertKey is the group key, or a hash of it when it is too long to be a
// key: a group of many labels has a long one, and truncating would merge
// groups that share a prefix.
func alertKey(groupKey string) string {
	k := strings.TrimSpace(groupKey)
	if len(k) <= MaxKey {
		return k
	}
	sum := sha256.Sum256([]byte(groupKey))
	return "am:" + hex.EncodeToString(sum[:16])
}

func alertTitle(p *amPayload) string {
	name := oneLine(firstOf(p.CommonLabels["alertname"], p.GroupLabels["alertname"]))
	summary := oneLine(firstOf(p.CommonAnnotations["summary"], p.CommonAnnotations["message"]))
	switch {
	case name != "" && summary != "":
		return name + ": " + summary
	case name != "":
		return name
	case summary != "":
		return summary
	}
	return "Alertmanager alert"
}

func alertBody(p *amPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** -- %d alert(s)", p.Status, len(p.Alerts)+p.TruncatedAlerts)
	if p.Receiver != "" {
		fmt.Fprintf(&b, ", receiver `%s`", oneLine(p.Receiver))
	}
	b.WriteString("\n")
	if d := firstOf(p.CommonAnnotations["description"], p.CommonAnnotations["message"]); d != "" {
		fmt.Fprintf(&b, "\n%s\n", d)
	}
	if len(p.CommonLabels) > 0 {
		b.WriteString("\nCommon labels:\n")
		for _, k := range sortedKeys(p.CommonLabels) {
			fmt.Fprintf(&b, "- %s = %s\n", oneLine(k), oneLine(p.CommonLabels[k]))
		}
	}
	if len(p.Alerts) > 0 {
		b.WriteString("\nAlerts:\n")
	}
	for i := range p.Alerts {
		if i == maxListedAlerts {
			fmt.Fprintf(&b, "- ... and %d more\n", len(p.Alerts)-maxListedAlerts)
			break
		}
		a := &p.Alerts[i]
		fmt.Fprintf(&b, "- %s %s", oneLine(a.Status), distinctLabels(a.Labels, p.CommonLabels))
		if a.StartsAt != "" {
			fmt.Fprintf(&b, " since %s", oneLine(a.StartsAt))
		}
		if a.Status == "resolved" && a.EndsAt != "" {
			fmt.Fprintf(&b, ", resolved %s", oneLine(a.EndsAt))
		}
		b.WriteString("\n")
	}
	if p.TruncatedAlerts > 0 {
		fmt.Fprintf(&b, "- Alertmanager left out %d more\n", p.TruncatedAlerts)
	}
	return b.String()
}

// distinctLabels are an alert's labels the group's common ones do not
// already say, which is what tells alerts in one group apart.
func distinctLabels(labels, common map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(labels) {
		if common[k] != labels[k] {
			parts = append(parts, oneLine(k)+"="+oneLine(labels[k]))
		}
	}
	if len(parts) == 0 {
		return "(same labels as the group)"
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func alertLink(p *amPayload) string {
	candidates := []string{p.ExternalURL}
	if len(p.Alerts) > 0 {
		candidates = append([]string{p.Alerts[0].GeneratorURL}, candidates...)
	}
	for _, c := range candidates {
		u, err := url.Parse(strings.TrimSpace(c))
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && len(c) <= MaxLink {
			return strings.TrimSpace(c)
		}
	}
	return ""
}

// alertContext is the small structured part: enough to tell alerts apart
// without the body, and always under MaxContext.
func alertContext(p *amPayload) (json.RawMessage, error) {
	ctx := map[string]any{
		"status": p.Status, "alerts": len(p.Alerts) + p.TruncatedAlerts,
	}
	for _, k := range []string{"alertname", "severity", "namespace", "project"} {
		if v := Truncate(p.CommonLabels[k], 100); v != "" {
			ctx[k] = v
		}
	}
	if p.Receiver != "" {
		ctx["receiver"] = Truncate(p.Receiver, 100)
	}
	raw, err := json.Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("encode context: %w", err)
	}
	return raw, nil
}

// firstOf is the first value with anything visible in it.
func firstOf(values ...string) string {
	for _, v := range values {
		if oneLine(v) != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
