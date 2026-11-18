package codehost

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Operation is what a caller is about to do on a repository. A credential
// is minted for one, so a token that leaks works for one kind of action on
// one repository, not for everything the tenant's App installation can do.
type Operation string

const (
	// OpenChange opens a change (pull request) on a branch.
	OpenChange Operation = "open_change"
	// MarkReady takes a change out of draft.
	MarkReady Operation = "mark_ready"
	// ReadChecks reads the checks a change has run.
	ReadChecks Operation = "read_checks"
	// CompareBase reads how far a change's head is behind its base branch, and the diff it would bring,
	// which needs the repository's commits, not only its pull requests.
	CompareBase Operation = "compare_base"
	// ReadBranch reads the head commit of a branch. Contents only: it
	// touches no pull request.
	ReadBranch Operation = "read_branch"
	// CreateBranch creates a branch at a commit. Contents write only: it
	// touches no pull request, and cannot write to a protected branch.
	CreateBranch Operation = "create_branch"
	// CloseChange closes a change without merging it. Pull requests only:
	// it writes no contents.
	CloseChange Operation = "close_change"
	// RetargetChange changes a change's base branch. Pull requests only.
	RetargetChange Operation = "retarget_change"
	// MarkDraft puts a change back into draft. Same permissions as MarkReady.
	MarkDraft Operation = "mark_draft"
	// RetitleChange changes a change's title. Pull requests only.
	RetitleChange Operation = "retitle_change"
	// DeleteBranch deletes a branch. Contents write only: it touches no
	// pull request directly, and cannot write to a protected branch.
	DeleteBranch Operation = "delete_branch"
	// MergeBranch merges one branch into another without a change.
	// Contents write only.
	MergeBranch Operation = "merge_branch"
	// UpdateChangeBranch merges a change's base into its head branch.
	// Contents write to push the merge commit, pull requests write to name
	// the change.
	UpdateChangeBranch Operation = "update_change_branch"
	// MergeChange merges a change. The only operation that can write code
	// to a protected branch, so the only one asking for it.
	MergeChange Operation = "merge_change"
)

// Repo is one repository by its owner and name, as the host names it.
type Repo struct {
	Owner string
	Name  string
}

// Token is a short-lived credential for one repository and one Operation.
// Reveal is the only way to its value; every other way of printing,
// logging or encoding one gives a placeholder.
type Token interface {
	Reveal() string
}

// Credentials mints tokens for a tenant's repositories. The tenant is named
// explicitly, not read from ctx: a caller acting for a tenant says so.
//
// Only a host's implementation of Changes (inside this package tree) calls
// TokenFor, once per operation, so a token never leaves it and the one
// credential lookup, by tenant and repository, sits behind the operations.
// A caller outside the tree acts through Changes and never holds a Token.
type Credentials interface {
	// TokenFor returns a token valid for op on repo in tenant's name, or an
	// error wrapping ErrNotConnected when the tenant has no credential.
	TokenFor(ctx context.Context, tenant uuid.UUID, repo Repo, op Operation) (Token, error)
}

// ErrNotConnected means the tenant has no credential for the host.
var ErrNotConnected = errors.New("codehost: no credential connected")
