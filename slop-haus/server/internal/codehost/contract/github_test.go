package contract_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/contract"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

const (
	// scratchRepoEnv names the scratch repository, "owner/name", and
	// scratchTokenEnv holds a token that can write its contents and pull
	// requests. The run is skipped unless both are set. The repository must
	// have no CI workflows and a default branch named main, and is written
	// to: it gets branches and pull requests, which the cases remove.
	scratchRepoEnv  = "SLOP_HAUS_CONTRACT_GITHUB_REPO"
	scratchTokenEnv = "SLOP_HAUS_CONTRACT_GITHUB_TOKEN"
)

type staticToken string

func (t staticToken) Reveal() string { return string(t) }

type staticCredentials string

func (c staticCredentials) TokenFor(context.Context, uuid.UUID, codehost.Repo, codehost.Operation) (codehost.Token, error) {
	return staticToken(c), nil
}

func TestGitHub_HonoursTheContract(t *testing.T) {
	spec, token := os.Getenv(scratchRepoEnv), os.Getenv(scratchTokenEnv)
	if spec == "" || token == "" {
		t.Skipf("set %s and %s to run the contract against a scratch repository on GitHub", scratchRepoEnv, scratchTokenEnv)
	}
	owner, name, ok := strings.Cut(spec, "/")
	require.True(t, ok, "%s is owner/name", scratchRepoEnv)
	repo := codehost.Repo{Owner: owner, Name: name}

	client, err := githubapp.NewClient(&githubapp.ClientOptions{})
	require.NoError(t, err)
	changes, err := github.NewChanges(&github.ChangesOptions{Client: client, Credentials: staticCredentials(token)})
	require.NoError(t, err)

	contract.Run(t, func(t *testing.T) *contract.Subject {
		return &contract.Subject{
			Changes: changes, Host: "github.com", Tenant: uuid.New(), Repo: repo, Base: "main",
			Commit: func(t *testing.T, branch string) string {
				t.Helper()
				path := fmt.Sprintf("/repos/%s/%s/contents/contract/%s.txt", owner, name, uuid.NewString())
				body := map[string]string{
					"message": "contract commit", "branch": branch,
					"content": base64.StdEncoding.EncodeToString([]byte(uuid.NewString())),
				}
				var out struct {
					Commit struct {
						SHA string `json:"sha"`
					} `json:"commit"`
				}
				require.NoError(t, client.Do(context.Background(), "PUT", path, token, body, 201, &out))
				require.NotEmpty(t, out.Commit.SHA)
				return out.Commit.SHA
			},
		}
	})
}
