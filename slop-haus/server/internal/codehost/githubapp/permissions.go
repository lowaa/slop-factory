package githubapp

import (
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// Permissions are an installation token's permissions, by name, each "read"
// or "write". A token carries only these, even if the App was granted more.
type Permissions map[string]string

// operationVerify is what Verify lists the installation's repositories
// with. Not a codehost.Operation: nothing outside this package acts with it.
const operationVerify codehost.Operation = "verify_connection"

// permissionsFor is the least a token needs to do op. Only merging and
// readying write contents; opening reads them, and reading checks writes
// nothing. GitHub
// refuses a token request for a permission the App was not granted, so the
// App must be created with at least the union.
//
// CI results come as check runs (checks) or commit statuses (statuses).
// Woodpecker reports only statuses; other CI reports check
// runs. A token that reads only one sees none of the other's results, which a
// merge gate could mistake for green, so every operation that carries checks
// carries statuses too, and a reader of CI results must look at both.
func permissionsFor(op codehost.Operation) (Permissions, error) {
	switch op {
	case codehost.OpenChange:
		// Opening names its head by branch, which GitHub resolves in the
		// repository's contents: without contents it refuses the request
		// as "Validation Failed", though its permission table lists only
		// pull requests.
		return Permissions{"pull_requests": "write", "contents": "read", "checks": "read", "statuses": "read"}, nil
	case codehost.MarkReady, codehost.MarkDraft:
		// Taking a change out of draft is open to those who can write to the
		// repository, not just to its pull requests: GraphQL's
		// markPullRequestReadyForReview refuses a token without contents
		// write as "Resource not accessible by integration".
		return Permissions{"pull_requests": "write", "contents": "write", "checks": "read", "statuses": "read"}, nil
	case codehost.ReadChecks:
		// Reading never needs write: a token that leaks from a polling path
		// cannot change a pull request.
		return Permissions{"pull_requests": "read", "checks": "read", "statuses": "read"}, nil
	case codehost.CompareBase:
		return Permissions{"pull_requests": "read", "contents": "read"}, nil
	case codehost.ReadBranch:
		return Permissions{"contents": "read"}, nil
	case codehost.CreateBranch:
		return Permissions{"contents": "write"}, nil
	case codehost.CloseChange:
		return Permissions{"pull_requests": "write"}, nil
	case codehost.RetargetChange, codehost.RetitleChange:
		return Permissions{"pull_requests": "write"}, nil
	case codehost.DeleteBranch, codehost.MergeBranch:
		return Permissions{"contents": "write"}, nil
	case codehost.UpdateChangeBranch:
		return Permissions{"pull_requests": "write", "contents": "write"}, nil
	case codehost.MergeChange:
		return Permissions{"pull_requests": "write", "contents": "write"}, nil
	case operationVerify:
		return Permissions{"metadata": "read"}, nil
	default:
		return nil, fmt.Errorf("githubapp: unknown operation %q", op)
	}
}

// allOperations is every operation permissionsFor answers; a test checks it
// against the operations codehost declares, so one cannot be left out of the
// App's manifest.
var allOperations = []codehost.Operation{
	codehost.OpenChange, codehost.MarkReady, codehost.MarkDraft, codehost.ReadChecks, codehost.CompareBase,
	codehost.ReadBranch, codehost.CreateBranch, codehost.CloseChange,
	codehost.RetargetChange, codehost.RetitleChange, codehost.DeleteBranch, codehost.MergeBranch, codehost.UpdateChangeBranch, codehost.MergeChange, operationVerify,
}

// appPermissions is what the App itself must be granted: the union of
// permissionsFor over every operation, the most any one asks for each
// permission. It is what the manifest requests, so the App GitHub creates
// can never lack a permission a token request names, nor hold one none does.
func appPermissions() Permissions {
	union := Permissions{}
	for _, op := range allOperations {
		perms, err := permissionsFor(op)
		if err != nil {
			panic(err)
		}
		for name, level := range perms {
			if union[name] != "write" {
				union[name] = level
			}
		}
	}
	return union
}
