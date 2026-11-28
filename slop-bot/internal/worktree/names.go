package worktree

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// BranchPrefix namespaces every branch slop-bot cuts. A ticket's branches are
// all under BranchPrefix + "t<ticket>/", which is how Cleanup finds them
// without state.db having to record each one.
const BranchPrefix = "slop-bot/"

// FirstBranch is the leaf name of the branch Create cuts, so a ticket's first
// PR is from slop-bot/t<ticket>/work.
const FirstBranch = "work"

const (
	maxTicketLen = 64
	maxRepoLen   = 100
	maxLeafLen   = 100
)

var (
	// No '-': the directory name is t<ticket>-<repo>, and a '-' in the
	// ticket would make t1-a-b both ticket "1" repo "a-b" and ticket "1-a"
	// repo "b". No '.' either, so the id is always a safe ref component.
	ticketRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]*$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	leafRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func validTicket(t ident.TicketID) error {
	s := string(t)
	if len(s) > maxTicketLen || !ticketRe.MatchString(s) {
		return fmt.Errorf("%w: ticket id %q: want letters, digits and '_', at most %d characters",
			ErrInvalidName, s, maxTicketLen)
	}
	return nil
}

func validRepo(name string) error {
	if len(name) > maxRepoLen || !repoRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("%w: repo name %q: want letters, digits, '.', '_' and '-', at most %d characters",
			ErrInvalidName, name, maxRepoLen)
	}
	return nil
}

// validLeaf is a first pass; git check-ref-format has the last word on the
// full branch name.
func validLeaf(name string) error {
	if len(name) > maxLeafLen || !leafRe.MatchString(name) || strings.Contains(name, "..") ||
		strings.Contains(name, "//") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("%w: branch name %q", ErrInvalidName, name)
	}
	return nil
}

// dirName is the worktree's directory under the workspace: t<ticket>-<repo>.
func dirName(ticket ident.TicketID, repo string) string {
	return "t" + string(ticket) + "-" + repo
}

// ticketBranchPrefix is the ref prefix holding all of ticket's branches. The
// trailing slash matters: it keeps t4's branches from matching t42's.
func ticketBranchPrefix(ticket ident.TicketID) string {
	return "refs/heads/" + BranchPrefix + "t" + string(ticket) + "/"
}

// BranchName is the branch slop-bot cuts for ticket with leaf name leaf.
func BranchName(ticket ident.TicketID, leaf string) string {
	return BranchPrefix + "t" + string(ticket) + "/" + leaf
}
