package codehost

// CheckState is where one check, or all of a change's checks, stand.
type CheckState int

const (
	// CheckNone is only ever the overall state: no check has said anything
	// that counts. It is not a pass -- see Checks.
	CheckNone CheckState = iota
	CheckPending
	CheckSuccess
	CheckFailure
	// CheckNeutral is a check that ran and neither passed nor failed
	// (skipped, neutral): it does not block, and does not vouch either.
	CheckNeutral
)

func (s CheckState) String() string {
	switch s {
	case CheckNone:
		return "none"
	case CheckPending:
		return "pending"
	case CheckSuccess:
		return "success"
	case CheckFailure:
		return "failure"
	case CheckNeutral:
		return "neutral"
	}
	return "unknown"
}

// Check is one check on a commit, whichever kind of report it came from.
type Check struct {
	Name  string
	State CheckState
	// TargetURL is where the reporting CI says the check lives, when the
	// host's report carries one (a commit status does; a check run may not).
	TargetURL string
}

// Checks are the checks a change had on one head commit.
//
// State is what the merge gate asks. It is Failure if any check failed,
// else Pending if any is pending, else Success if at least one check
// succeeded, else None -- which is also what a change with no checks at all
// has, as does one whose checks are all neutral. None is not Success:
// right after a push CI has not reported yet, and a repository with no CI
// has nothing vouching for the head, so "nothing failed" must not read as
// "passed". Only Passed is true for a pass.
type Checks struct {
	// HeadSHA is the commit these checks are for, to give to Merge.
	HeadSHA string
	// Base is the branch the change targets on the host right now, which
	// is what a merge lands on. It is the host's answer, not what the
	// change was opened with: a change can be retargeted afterwards.
	Base string
	// Title is the change's title as the host has it now, read with the same
	// fetch: what a caller that rewrites a title (a "DO NOT MERGE" prefix)
	// starts from, since RetitleChange takes the whole title.
	Title  string
	Checks []Check
	State  CheckState
}

// Passed is whether the head may be merged on its checks.
func (c *Checks) Passed() bool { return c.State == CheckSuccess }

// NewChecks is the Checks for a head commit of a change onto base with these checks, with State
// worked out by the rules above. Every Changes builds its result with it so
// the rule is one thing, whichever host reported.
func NewChecks(headSHA, base string, checks []Check) *Checks {
	var failed, pending, succeeded bool
	for _, c := range checks {
		switch c.State {
		case CheckFailure:
			failed = true
		case CheckPending:
			pending = true
		case CheckSuccess:
			succeeded = true
		}
	}
	state := CheckNone
	switch {
	case failed:
		state = CheckFailure
	case pending:
		state = CheckPending
	case succeeded:
		state = CheckSuccess
	}
	return &Checks{HeadSHA: headSHA, Base: base, Checks: checks, State: state}
}
