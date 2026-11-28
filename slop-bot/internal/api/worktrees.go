package api

// Worktree routes, the user's only.
const (
	// PathWorktrees lists every ticket's recorded worktrees.
	PathWorktrees = "/v1/worktrees"
	// PathWorktreesClean cleans up one ticket's worktrees.
	PathWorktreesClean = "/v1/worktrees/{ticket}/clean"
)

// Worktree statuses in GET /v1/worktrees.
const (
	// WorktreeInUse is a live agent's: it is cleaned up when the ticket ends.
	WorktreeInUse = "in_use"
	// WorktreeKept is an ended agent's that holds work Cleanup won't lose.
	WorktreeKept = "kept"
	// WorktreeReleased was handed to a person (`attach --no-resume`), and is
	// left alone for them.
	WorktreeReleased = "released"
	// WorktreeRemovable is an ended agent's that holds nothing a clean would
	// lose.
	WorktreeRemovable = "removable"
	// WorktreeUnchecked couldn't be checked; Error says why.
	WorktreeUnchecked = "unchecked"
)

// WorktreeReason is one thing removing a worktree would lose.
type WorktreeReason struct {
	// Kind is uncommitted, untracked, unpushed, locked, nested or
	// in_progress.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Worktree is one recorded worktree in GET /v1/worktrees.
type Worktree struct {
	Ticket     string           `json:"ticket"`
	Agent      string           `json:"agent"`
	AgentState string           `json:"agent_state"`
	Repo       string           `json:"repo"`
	Path       string           `json:"path"`
	Branch     string           `json:"branch,omitempty"`
	Status     string           `json:"status"`
	Reasons    []WorktreeReason `json:"reasons,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// CleanWorktrees is POST /v1/worktrees/{ticket}/clean.
type CleanWorktrees struct {
	// Force discards what the worktrees hold: the CLI confirms it first.
	Force bool `json:"force,omitempty"`
}

// CleanedWorktree is what a clean did with one worktree.
type CleanedWorktree struct {
	Repo    string           `json:"repo"`
	Path    string           `json:"path"`
	Removed bool             `json:"removed"`
	Reasons []WorktreeReason `json:"reasons,omitempty"`
	Error   string           `json:"error,omitempty"`
}
