package manifest

import (
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

// Status is what an agent's reported layout comes to against the manifest.
type Status string

const (
	// StatusUnknown: the agent has reported nothing, because its workspace
	// has not run update.sh. Never a problem and never blocks.
	StatusUnknown Status = "unknown"
	// StatusReported: there is no manifest to compare the report with.
	StatusReported Status = "reported"
	StatusMatches  Status = "matches"
	StatusMismatch Status = "mismatch"
)

// Problems with one manifest path.
const (
	// ProblemNoRepo: no git repository was reported there. The report lists
	// only git directories, so a missing directory and one that is not a
	// repo look the same, and the message says so.
	ProblemNoRepo      = "no_repo"
	ProblemNoOrigin    = "no_origin"
	ProblemWrongOrigin = "wrong_origin"
	// ProblemRootIsRepo: the workspace root itself is a git repository. The
	// root holds slop-haus's managed files and the repos are subfolders.
	ProblemRootIsRepo = "root_is_repo"
)

// Mismatch is one manifest path whose reported state differs: which path,
// what the manifest wants, and what was found there.
type Mismatch struct {
	Path     string `json:"path"`
	Problem  string `json:"problem" enum:"no_repo,no_origin,wrong_origin,root_is_repo"`
	Expected string `json:"expected" doc:"The manifest's remote for this path."`
	Found    string `json:"found" doc:"The origin reported there, empty when there is none."`
}

// Describe says what is wrong in a sentence, for the board and for an
// agent's claim.
func (m Mismatch) Describe() string {
	where := fmt.Sprintf("%q", m.Path)
	switch m.Problem {
	case ProblemRootIsRepo:
		return "the workspace root must not be a repo: repos go in subfolders"
	case ProblemNoRepo:
		return fmt.Sprintf("%s: expected %s, but no git repository was found there (missing, or not a git repo)", where, m.Expected)
	case ProblemNoOrigin:
		return fmt.Sprintf("%s: expected %s, but the repository there has no origin remote", where, m.Expected)
	default:
		return fmt.Sprintf("%s: expected %s, but its origin is %s", where, m.Expected, m.Found)
	}
}

// Check compares a reported layout with the manifest. Branches and commits
// are not compared -- agents are on different ones by design -- and
// directories the manifest does not list are ignored.
func Check(repos []Repo, layout *agent.Layout) []Mismatch {
	reported := map[string]agent.LayoutEntry{}
	for _, e := range layout.Repos {
		reported[e.Path] = e
	}
	var out []Mismatch
	if layout.Root.Git {
		out = append(out, Mismatch{Path: ".", Problem: ProblemRootIsRepo, Found: layout.Root.Origin})
	}
	for _, r := range repos {
		m := Mismatch{Path: r.Path, Expected: r.Remote}
		e, ok := reported[r.Path]
		switch {
		case !ok || !e.Git:
			m.Problem = ProblemNoRepo
		case e.Origin == "":
			m.Problem = ProblemNoOrigin
		case NormaliseRemote(e.Origin) != r.Remote:
			m.Problem = ProblemWrongOrigin
			m.Found = e.Origin
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}

// Limits on a supervisor's reconcile report: a runaway manifest must not
// make a large heartbeat, so each list keeps a count and the first few.
const (
	MaxReconcileEntries = 20
	MaxReconcileText    = 255
)

// Bounded is a count of everything that happened and the first
// MaxReconcileEntries of them.
type Bounded[T any] struct {
	Count   int `json:"count" doc:"How many there were, including those not listed."`
	Entries []T `json:"entries" doc:"The first few."`
}

// Held is a path the supervisor left alone, and why. Never file contents.
type Held struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Moved is a checkout renamed on disk.
type Moved struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ReconcileReport is what a supervisor's workspace reconcile did and what
// it refused, per workspace. Refusals are states, not events: the same
// refusal appears in every pass until it is resolved, so the board shows
// the current condition without a stream of repeats.
//
// Additive: a supervisor that does not reconcile sends none of it, and the
// zero value reads as "nothing to report".
type ReconcileReport struct {
	StaleRemoved          Bounded[string] `json:"stale_removed" doc:"Checkouts deleted because the manifest no longer lists them."`
	Moved                 Bounded[Moved]  `json:"moved"`
	MovedWithUnpushedWork Bounded[Held]   `json:"moved_with_unpushed_work" doc:"Moved even though they hold unpushed work, which travelled with the checkout."`
	KeptWithUnpushedWork  Bounded[Held]   `json:"kept_with_unpushed_work" doc:"Not removed because of unpushed work."`
	RemoteRepointed       Bounded[string] `json:"remote_repointed"`
	MoveConflict          Bounded[Held]   `json:"move_conflict" doc:"Moves refused whole: a swap, a cycle, or a target that exists."`
}

// NewReconcileReport sanitises a report from the wire: lists are cut to
// MaxReconcileEntries, text to MaxReconcileText runes, and a count never
// reads lower than the entries kept.
func NewReconcileReport(in ReconcileReport) ReconcileReport {
	return ReconcileReport{
		StaleRemoved:          bound(in.StaleRemoved, clipText),
		Moved:                 bound(in.Moved, func(m Moved) Moved { return Moved{From: clipText(m.From), To: clipText(m.To)} }),
		MovedWithUnpushedWork: bound(in.MovedWithUnpushedWork, clipHeld),
		KeptWithUnpushedWork:  bound(in.KeptWithUnpushedWork, clipHeld),
		RemoteRepointed:       bound(in.RemoteRepointed, clipText),
		MoveConflict:          bound(in.MoveConflict, clipHeld),
	}
}

func bound[T any](in Bounded[T], clip func(T) T) Bounded[T] {
	out := Bounded[T]{Count: in.Count, Entries: []T{}}
	for _, e := range in.Entries {
		if len(out.Entries) == MaxReconcileEntries {
			break
		}
		out.Entries = append(out.Entries, clip(e))
	}
	out.Count = max(out.Count, len(in.Entries), 0)
	return out
}

func clipText(s string) string {
	if r := []rune(s); len(r) > MaxReconcileText {
		return string(r[:MaxReconcileText])
	}
	return s
}

func clipHeld(h Held) Held { return Held{Path: clipText(h.Path), Reason: clipText(h.Reason)} }
