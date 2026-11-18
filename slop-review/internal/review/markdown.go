package review

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var severityOrder = []Severity{SeverityBlocker, SeverityMajor, SeverityMinor, SeverityNit}

var severityHeading = map[Severity]string{
	SeverityBlocker: "Blockers",
	SeverityMajor:   "Major",
	SeverityMinor:   "Minor",
	SeverityNit:     "Nits",
}

// RenderMarkdown renders r as a human-readable markdown report, grouping
// findings by severity (blockers first) rather than the order the model
// emitted them in.
func RenderMarkdown(r Review) string {
	var b strings.Builder

	heading := verdictHeading(r.Verdict)
	if r.Incomplete && r.Verdict != VerdictIncomplete {
		heading += " (incomplete: part of the change went unreviewed, see Warnings)"
	}
	fmt.Fprintf(&b, "# Review: %s\n\n", heading)
	if r.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", r.Summary)
	}

	if len(r.Findings) == 0 {
		b.WriteString("No findings.\n")
	} else {
		byServerity := groupBySeverity(r.Findings)
		for _, sev := range severityOrder {
			findings := byServerity[sev]
			if len(findings) == 0 {
				continue
			}
			fmt.Fprintf(&b, "## %s\n\n", severityHeading[sev])
			for _, f := range findings {
				renderFinding(&b, f)
			}
		}
	}

	if len(r.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return b.String()
}

func renderFinding(b *strings.Builder, f Finding) {
	location := f.File
	if f.Line > 0 {
		location = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	attribution := ""
	if len(f.Personas) > 0 {
		attribution = fmt.Sprintf(" _(%s)_", strings.Join(f.Personas, ", "))
	}
	// The id is what `slop-review label` takes.
	id := ""
	if f.ID != uuid.Nil {
		id = fmt.Sprintf(" (id: `%s`)", f.ID)
	}
	fmt.Fprintf(b, "- **%s**: %s%s%s\n", location, f.Summary, attribution, id)
	if f.Detail != "" {
		fmt.Fprintf(b, "  %s\n", f.Detail)
	}
}

func groupBySeverity(findings []Finding) map[Severity][]Finding {
	out := make(map[Severity][]Finding, len(severityOrder))
	for _, f := range findings {
		out[f.Severity] = append(out[f.Severity], f)
	}
	return out
}

func verdictHeading(v Verdict) string {
	switch v {
	case VerdictApprove:
		return "Approved"
	case VerdictApproveWithComments:
		return "Approved, with comments"
	case VerdictRequestChanges:
		return "Changes requested"
	case VerdictIncomplete:
		return "Incomplete"
	default:
		return string(v)
	}
}
