package contract_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/contract"
)

const fakeHost = "git.example.test"

// newFakeSubject is a Fake as a repository with no CI workflows, which is
// what the scratch repository the real run uses has.
func newFakeSubject(t *testing.T) *contract.Subject {
	t.Helper()
	repo := codehost.Repo{Owner: "acme", Name: "scratch"}
	ci := codehosttest.NewCI()
	ci.Strict = true
	ci.Script = func(codehost.Repo, string) ([]codehost.Check, bool) { return nil, true }
	f := codehosttest.NewLinkHost(fakeHost)
	f.CI = ci
	f.BranchAt = map[codehosttest.BranchKey]string{{Repo: repo, Branch: "main"}: "base0"}
	commits := 0
	return &contract.Subject{
		Changes: f, Host: fakeHost, Tenant: uuid.New(), Repo: repo, Base: "main",
		Commit: func(t *testing.T, branch string) string {
			commits++
			sha := fmt.Sprintf("commit%d", commits)
			f.BranchAt[codehosttest.BranchKey{Repo: repo, Branch: branch}] = sha
			return sha
		},
	}
}

func TestFake_HonoursTheContract(t *testing.T) {
	contract.Run(t, newFakeSubject)
}
