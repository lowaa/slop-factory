package manifest

import (
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

// AgentLayout is one agent's report, as the proposal reads it.
type AgentLayout struct {
	AgentID   uuid.UUID
	AgentName string
	Layout    agent.Layout
}

// Why there is no proposal.
const (
	ReasonNoReports   = "no_reports"
	ReasonNoRepos     = "no_repos"
	ReasonNoConsensus = "no_consensus"
)

// Disagreement is an agent whose workspace is not what the proposal is
// built on: the odd one out, to fix first.
type Disagreement struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Found     []Repo `json:"found" doc:"What this agent's workspace has, in the same terms as the proposal. Empty when it has no git repo with an origin."`
}

// Proposal is what the agents' reports suggest the manifest should be.
// Only ever shown: the server never stores one, and a person adopts it.
type Proposal struct {
	// Repos is empty when no proposal could be made; Reason says why.
	Repos         []Repo         `json:"repos"`
	Reason        string         `json:"reason,omitempty" enum:"no_reports,no_repos,no_consensus"`
	Disagreements []Disagreement `json:"disagreements"`
}

// group is the agents whose workspaces have the same repos.
type group struct {
	repos  []Repo
	agents []AgentLayout
}

// Propose builds a proposal from what agents reported, and from nothing
// else: not from past pr_urls, which give the repo but neither its path nor
// what is on disk.
//
// Each agent is read as the repos its workspace has: the root alone when the
// root is a git repo (a clone nested inside one is rare, and an owner can
// add it), otherwise its top-level git repos. The proposal is the layout
// most agents have, as long as it is strictly the most common. When it is
// not -- two agents that differ -- picking a side would be a guess, so there
// is no proposal and both are named; when it is, the agents with another
// layout are the odd ones out.
func Propose(reports []AgentLayout) *Proposal {
	p := &Proposal{Repos: []Repo{}, Disagreements: []Disagreement{}}
	if len(reports) == 0 {
		p.Reason = ReasonNoReports
		return p
	}

	groups := map[string]*group{}
	for _, r := range reports {
		repos := reposOf(&r.Layout)
		key := signature(repos)
		if groups[key] == nil {
			groups[key] = &group{repos: repos}
		}
		groups[key].agents = append(groups[key].agents, r)
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	winner, tied := keys[0], false
	for _, k := range keys[1:] {
		switch n, best := len(groups[k].agents), len(groups[winner].agents); {
		case n > best:
			winner, tied = k, false
		case n == best:
			tied = true
		}
	}

	win := groups[winner]
	switch {
	case tied:
		p.Reason = ReasonNoConsensus
	case len(win.repos) == 0:
		p.Reason = ReasonNoRepos
	default:
		p.Repos = win.repos
	}
	for _, k := range keys {
		if k == winner && !tied {
			continue
		}
		for _, a := range groups[k].agents {
			p.Disagreements = append(p.Disagreements, Disagreement{
				AgentID: a.AgentID.String(), AgentName: a.AgentName, Found: groups[k].repos,
			})
		}
	}
	slices.SortFunc(p.Disagreements, func(a, b Disagreement) int { return strings.Compare(a.AgentName, b.AgentName) })
	return p
}

func reposOf(l *agent.Layout) []Repo {
	repos := []Repo{}
	for _, e := range l.Repos {
		if e.Git && e.Origin != "" {
			repos = append(repos, Repo{Path: e.Path, Remote: NormaliseRemote(e.Origin)})
		}
	}
	slices.SortFunc(repos, func(a, b Repo) int { return strings.Compare(a.Path, b.Path) })
	return repos
}

func signature(repos []Repo) string {
	var b strings.Builder
	for _, r := range repos {
		b.WriteString(r.Path + "\x00" + r.Remote + "\x00")
	}
	return b.String()
}
