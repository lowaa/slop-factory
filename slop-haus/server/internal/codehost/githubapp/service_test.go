package githubapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

var acmeApp = codehost.Repo{Owner: "acme", Name: "app"}

const secretToken = "ghs_installation_token_for_tests_only"

func (f *fixture) mintsFor(repos ...string) {
	f.github.token = func(int) answer {
		return answer{Body: tokenBody(secretToken, f.clock().Add(time.Hour), repos...)}
	}
}

func TestConnect_StoresTheKeyEncryptedAndNeverReturnsIt(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	key := rsaKey(t, 0)

	rec, err := f.svc.Connect(a.ctx, a.owner, &githubapp.Connection{
		AppID: appID, InstallationID: installationID, PrivateKey: []byte(pkcs1PEM(key)),
	})
	require.NoError(t, err)
	assert.Equal(t, appID, rec.AppID)
	assert.Nil(t, rec.VerifiedAt)

	// What the database holds, read past row level security: ciphertext,
	// which does not contain the key in any encoding it was handed over in.
	var blob []byte
	require.NoError(t, f.owners.Global().QueryRow(context.Background(),
		`SELECT private_key_enc FROM codehost_credentials WHERE tenant_id = $1`, a.id).Scan(&blob))
	body := strings.Split(pkcs1PEM(key), "\n")[1]
	assert.NotContains(t, string(blob), body)
	assert.NotContains(t, string(blob), "PRIVATE KEY")
	assert.Equal(t, f.keyring.CurrentID(), secretbox.BlobKeyID(blob))

	// And it opens only as that tenant's row.
	got, err := f.keyring.Open(blob, secretbox.Binding{Tenant: a.id, Row: rec.ID})
	require.NoError(t, err)
	assert.Contains(t, string(got), "PRIVATE KEY")

	st, err := f.svc.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.True(t, st.Enabled)
	assert.True(t, st.CanEdit)
	require.NotNil(t, st.Credential)
	assert.Equal(t, rec.Fingerprint, st.Credential.Fingerprint)
}

func TestConnect_ReplacingKeepsOneRowAndReSealsUnderANewId(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	first, err := f.store.Get(a.ctx)
	require.NoError(t, err)

	f.connect(a, 1)
	second, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.NotEqual(t, first.Fingerprint, second.Fingerprint)
	assert.Nil(t, second.VerifiedAt)

	var rows int
	require.NoError(t, f.owners.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM codehost_credentials WHERE tenant_id = $1`, a.id).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestConnect_RefusesWhatItShouldNotStore(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	good := pkcs1PEM(rsaKey(t, 0))
	stale := *a.owner
	stale.AuthTime = time.Now().Add(-48 * time.Hour)

	cases := map[string]struct {
		who  *authn.User
		conn githubapp.Connection
		want error
	}{
		"member":          {a.member, githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte(good)}, githubapp.ErrNotOwner},
		"stale sign-in":   {&stale, githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte(good)}, goauthn.ErrReauthRequired},
		"not a key":       {a.owner, githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte("nope")}, githubapp.ErrInvalidInput},
		"weak key":        {a.owner, githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte(pkcs1PEM(smallRSAKey(t)))}, githubapp.ErrInvalidInput},
		"no app id":       {a.owner, githubapp.Connection{InstallationID: 2, PrivateKey: []byte(good)}, githubapp.ErrInvalidInput},
		"no installation": {a.owner, githubapp.Connection{AppID: 1, PrivateKey: []byte(good)}, githubapp.ErrInvalidInput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Connect(a.ctx, c.who, &c.conn)
			require.ErrorIs(t, err, c.want)
			_, err = f.store.Get(a.ctx)
			assert.ErrorIs(t, err, codehost.ErrNotConnected, "nothing was stored")
		})
	}
}

func TestConnect_IsOffWithoutAnEncryptionKey(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	off := f.service(nil)

	assert.False(t, off.Enabled())
	_, err := off.Connect(a.ctx, a.owner, &githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte(pkcs1PEM(rsaKey(t, 0)))})
	assert.ErrorIs(t, err, githubapp.ErrDisabled)
	_, err = off.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, githubapp.ErrDisabled)
	_, err = f.store.Get(a.ctx)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	st, err := off.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.False(t, st.Enabled)
}

func TestTenancy_ACredentialIsItsOwnersAlone(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")

	st, err := f.svc.Status(b.ctx, b.owner)
	require.NoError(t, err)
	assert.Nil(t, st.Credential, "tenant b sees nothing of a's")
	_, err = f.svc.TokenFor(b.ctx, b.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	_, err = f.svc.Verify(b.ctx, b.owner)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	assert.Zero(t, f.github.Total(), "no call to GitHub was made for b")

	// Removing b's (nothing) leaves a's alone.
	require.NoError(t, f.svc.Remove(b.ctx, b.owner))
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.NoError(t, err)

	// A member of a, signed in as b's tenant, is not b's owner either.
	_, err = f.svc.Connect(b.ctx, &authn.User{ID: a.owner.ID, TenantID: b.id, AuthTime: time.Now()},
		&githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte(pkcs1PEM(rsaKey(t, 0)))})
	assert.ErrorIs(t, err, githubapp.ErrNotOwner)
}

func TestTenancy_ASealedKeyMovedToAnotherTenantsRowDoesNotOpen(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)
	f.connect(b, 1)
	f.mintsFor("acme/app")

	// An attacker with write access to the database copies a's blob into
	// b's row (as the table's owner, past row level security).
	_, err := f.owners.Global().Exec(context.Background(), `
		UPDATE codehost_credentials SET private_key_enc =
			(SELECT private_key_enc FROM codehost_credentials WHERE tenant_id = $1)
		WHERE tenant_id = $2`, a.id, b.id)
	require.NoError(t, err)

	_, err = f.svc.TokenFor(b.ctx, b.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen)
	assert.Zero(t, f.github.Total())
}

func TestRowLevelSecurityHidesOtherTenantsRows(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)

	_, err := f.store.Get(b.ctx)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	removed, err := f.store.Delete(b.ctx)
	require.NoError(t, err)
	assert.False(t, removed, "b cannot delete a's row")
	_, err = f.store.Get(a.ctx)
	assert.NoError(t, err)
	_, err = f.store.Get(context.Background())
	assert.ErrorIs(t, err, db.ErrNoTenant, "a query with no tenant fails rather than reading all")
}

// bearerJWT is the App JWT a recorded request carried, verified against the
// public half of the key that should have signed it.
func bearerJWT(t *testing.T, req recordedRequest, key int) (*jwt.RegisteredClaims, string) {
	t.Helper()
	raw, ok := strings.CutPrefix(req.Auth, "Bearer ")
	require.True(t, ok, req.Auth)
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &rsaKey(t, key).PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	return claims, raw
}

func requestedToken(t *testing.T, req recordedRequest) (repos []string, permissions map[string]string) {
	t.Helper()
	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &body))
	return body.Repositories, body.Permissions
}

func TestTokenFor_MintsOneRepoAndOnlyWhatTheOperationNeeds(t *testing.T) {
	cases := map[codehost.Operation]map[string]string{
		codehost.OpenChange:   {"pull_requests": "write", "contents": "read", "checks": "read", "statuses": "read"},
		codehost.MarkReady:    {"pull_requests": "write", "contents": "write", "checks": "read", "statuses": "read"},
		codehost.MarkDraft:    {"pull_requests": "write", "contents": "write", "checks": "read", "statuses": "read"},
		codehost.ReadChecks:   {"pull_requests": "read", "checks": "read", "statuses": "read"},
		codehost.CompareBase:  {"pull_requests": "read", "contents": "read"},
		codehost.ReadBranch:   {"contents": "read"},
		codehost.CreateBranch: {"contents": "write"},
		codehost.CloseChange:  {"pull_requests": "write"},
		codehost.MergeChange:  {"pull_requests": "write", "contents": "write"},

		codehost.RetargetChange:     {"pull_requests": "write"},
		codehost.RetitleChange:      {"pull_requests": "write"},
		codehost.DeleteBranch:       {"contents": "write"},
		codehost.MergeBranch:        {"contents": "write"},
		codehost.UpdateChangeBranch: {"pull_requests": "write", "contents": "write"},
	}
	for op, want := range cases {
		t.Run(string(op), func(t *testing.T) {
			f := newFixture(t)
			a := f.tenant()
			f.connect(a, 0)
			f.mintsFor("acme/app")

			tok, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, op)
			require.NoError(t, err)
			assert.Equal(t, secretToken, tok.Reveal())

			calls := f.github.Calls("/access_tokens")
			require.Len(t, calls, 1)
			assert.Equal(t, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), calls[0].Path)
			repos, perms := requestedToken(t, calls[0])
			assert.Equal(t, []string{"app"}, repos, "restricted to the one repository")
			assert.Equal(t, want, perms, "restricted to what the operation needs")
		})
	}
}

func TestTokenFor_OnlyMergingReadyingAndBranchingAskToWriteContents(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	for _, op := range []codehost.Operation{codehost.OpenChange, codehost.ReadChecks, codehost.CompareBase, codehost.ReadBranch} {
		_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, op)
		require.NoError(t, err)
	}
	for _, call := range f.github.Calls("/access_tokens") {
		_, perms := requestedToken(t, call)
		assert.NotEqual(t, "write", perms["contents"], perms)
	}
}

func TestTokenFor_SignsAnAppJWTForTheStoredKey(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 1)
	f.mintsFor("acme/app")

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)

	claims, _ := bearerJWT(t, f.github.Calls("/access_tokens")[0], 1)
	assert.Equal(t, "4242", claims.Issuer)
	assert.Less(t, claims.IssuedAt.Time, f.clock(), "backdated for clock drift")
	assert.LessOrEqual(t, claims.ExpiresAt.Sub(claims.IssuedAt.Time), 10*time.Minute, "GitHub refuses longer")
	assert.Greater(t, claims.ExpiresAt.Time, f.clock())
}

func TestTokenFor_RefusesAnOperationOrRepoItDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.Operation("delete_everything"))
	assert.Error(t, err)
	for _, bad := range []codehost.Repo{{Owner: "acme", Name: ""}, {Owner: "", Name: "app"}, {Owner: "acme", Name: ".."},
		{Owner: "acme", Name: "a/b"}, {Owner: "ac me", Name: "app"}, {Owner: "acme", Name: `app","contents":"write`}} {
		_, err := f.svc.TokenFor(a.ctx, a.id, bad, codehost.OpenChange)
		assert.ErrorIs(t, err, githubapp.ErrInvalidInput, "%+v", bad)
	}
	assert.Zero(t, f.github.Total())
}

func TestTokenFor_RefusesATokenThatDoesNotCoverExactlyThatRepo(t *testing.T) {
	cases := map[string][]string{
		"another owner's repo of the same name": {"someone-else/app"},
		"more than asked for":                   {"acme/app", "acme/other"},
		"none":                                  {},
	}
	for name, covers := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.tenant()
			f.connect(a, 0)
			f.mintsFor(covers...)

			_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
			assert.ErrorIs(t, err, githubapp.ErrRepoNotCovered)
		})
	}
}

func TestTokenFor_TheRepoMatchesWhateverCaseGitHubReports(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("Acme/App")
	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.NoError(t, err)
}

func TestTokenFor_CachesUntilNearExpiryAndNeverStoresTheToken(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	mint := func(op codehost.Operation) {
		_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, op)
		require.NoError(t, err)
	}

	mint(codehost.OpenChange)
	mint(codehost.OpenChange)
	assert.Len(t, f.github.Calls("/access_tokens"), 1, "the second call is the cache's")

	mint(codehost.MergeChange)
	assert.Len(t, f.github.Calls("/access_tokens"), 2, "another operation is another token")

	f.advance(54 * time.Minute)
	mint(codehost.OpenChange)
	assert.Len(t, f.github.Calls("/access_tokens"), 2, "still good with more than five minutes left")
	f.advance(2 * time.Minute)
	mint(codehost.OpenChange)
	assert.Len(t, f.github.Calls("/access_tokens"), 3, "within five minutes of expiry it is minted afresh")

	// In memory only: a dump of every table has no trace of it.
	var found int
	require.NoError(t, f.owners.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM codehost_credentials WHERE row_to_json(codehost_credentials)::text LIKE '%'||$1||'%'`,
		secretToken).Scan(&found))
	assert.Zero(t, found)
}

func TestTokenFor_AReplacedOrRemovedCredentialIsNotServedFromTheCache(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)

	f.connect(a, 1)
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)
	calls := f.github.Calls("/access_tokens")
	require.Len(t, calls, 2, "replacing the credential minted again")
	bearerJWT(t, calls[1], 1) // signed with the new key

	require.NoError(t, f.svc.Remove(a.ctx, a.owner))
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	assert.Len(t, f.github.Calls("/access_tokens"), 2)
}

func TestTokenFor_ACacheOnAnotherServerDoesNotOutliveAReplace(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	other := f.service(f.keyring) // another replica: its own cache
	_, err := other.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)

	f.connect(a, 1) // replaced through the first server
	_, err = other.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)
	assert.Len(t, f.github.Calls("/access_tokens"), 2)

	require.NoError(t, f.svc.Remove(a.ctx, a.owner))
	_, err = other.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
}

func TestTokenFor_GitHubRefusingIsAnErrorWithNoCredentialInIt(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.token = func(int) answer {
		return answer{Status: 422, Body: map[string]string{"message": "the App lacks that permission"}}
	}

	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	var apiErr *githubapp.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 422, apiErr.Status)
	_, jwtText := bearerJWT(t, f.github.Calls("/access_tokens")[0], 0)
	assert.NotContains(t, err.Error(), jwtText)
}

func TestNothingSecretReachesTheLogs(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	key := rsaKey(t, 0)
	f.connect(a, 0)
	f.mintsFor("acme/app")
	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.MergeChange)
	require.NoError(t, err)
	_, err = f.svc.Connect(a.ctx, a.owner, &githubapp.Connection{AppID: 1, InstallationID: 2, PrivateKey: []byte("junk " + pkcs1PEM(key)[:60])})
	require.Error(t, err)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }
	_, err = f.svc.Verify(a.ctx, a.owner)
	require.NoError(t, err)

	_, jwtText := bearerJWT(t, f.github.Calls("/access_tokens")[0], 0)
	pemBody := strings.Split(pkcs1PEM(key), "\n")[1]
	logs := f.logs.String()
	require.NotEmpty(t, logs)
	assert.NotContains(t, logs, secretToken)
	assert.NotContains(t, logs, jwtText)
	assert.NotContains(t, logs, pemBody)
	assert.NotContains(t, logs, "PRIVATE KEY")
	assert.Contains(t, logs, "minted a code host token", "the mint itself is on record")
}

func TestVerify_ListsWhatTheInstallationSeesAgainstTheManifest(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	f.github.repos = func(int) answer {
		return answer{Body: map[string]any{"repositories": []map[string]string{{"full_name": "acme/app"}, {"full_name": "Acme/Docs"}}}}
	}
	f.manifest.remotes = []string{"github.com/acme/app", "github.com/acme/docs", "github.com/acme/missing", "gitlab.com/acme/elsewhere"}

	v, err := f.svc.Verify(a.ctx, a.owner)
	require.NoError(t, err)
	assert.Equal(t, "acme", v.Account)
	assert.Equal(t, []string{"acme/app", "Acme/Docs"}, v.Repos)
	assert.Equal(t, []githubapp.RepoCheck{
		{Remote: "github.com/acme/app", Visible: true},
		{Remote: "github.com/acme/docs", Visible: true},
		{Remote: "github.com/acme/missing", Visible: false},
	}, v.Manifest, "only GitHub remotes are checked")

	// The verification token is as small as the rest: it lists, nothing more.
	_, perms := requestedToken(t, f.github.Calls("/access_tokens")[0])
	assert.Equal(t, map[string]string{"metadata": "read"}, perms)
	claims, _ := bearerJWT(t, f.github.Calls(fmt.Sprintf("/installations/%d", installationID))[0], 0)
	assert.Equal(t, "4242", claims.Issuer)

	st, err := f.svc.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.NotNil(t, st.Credential.VerifiedAt)
}

func TestVerify_FailsAndStaysUnverifiedWhenGitHubRefuses(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Status: 401, Body: map[string]string{"message": "A JSON web token could not be decoded"}}

	_, err := f.svc.Verify(a.ctx, a.owner)
	var apiErr *githubapp.APIError
	require.ErrorAs(t, err, &apiErr)
	st, err := f.svc.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.Nil(t, st.Credential.VerifiedAt)
}

func TestVerify_IsForOwnersOnly(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	_, err := f.svc.Verify(a.ctx, a.member)
	assert.ErrorIs(t, err, githubapp.ErrNotOwner)
	assert.Zero(t, f.github.Total())
}

func TestRemove_IsForOwnersWithARecentSignIn(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	stale := *a.owner
	stale.AuthTime = time.Now().Add(-48 * time.Hour)

	assert.ErrorIs(t, f.svc.Remove(a.ctx, a.member), githubapp.ErrNotOwner)
	assert.ErrorIs(t, f.svc.Remove(a.ctx, &stale), goauthn.ErrReauthRequired)
	_, err := f.store.Get(a.ctx)
	require.NoError(t, err, "still connected")

	require.NoError(t, f.svc.Remove(a.ctx, a.owner))
	_, err = f.store.Get(a.ctx)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
}

func TestKeyRotation_DroppingTheOldKeyBeforeReSealingLeavesTheCredentialUnreadable(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	before, err := f.store.Get(a.ctx)
	require.NoError(t, err)

	rotated := newRing(t, newKey(t), nil)
	require.NotEqual(t, before.KeyID, rotated.CurrentID())

	noPrevious := f.service(rotated)
	_, err = noPrevious.Verify(a.ctx, a.owner)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen)
}

func TestKeyRotation_AServerHoldingThePreviousKeyReadsAndReSeals(t *testing.T) {
	oldKeyBytes, newKeyBytes := newKey(t), newKey(t)
	f := newFixture(t)
	f.keyring = newRing(t, oldKeyBytes, nil)
	f.svc = f.service(f.keyring)
	a := f.tenant()
	f.connect(a, 0)
	before, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor("acme/app")
	f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }

	rotated := newRing(t, newKeyBytes, oldKeyBytes)
	svc := f.service(rotated)

	// Minting reads under the previous key and changes nothing stored.
	_, err = svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)
	mid, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, before.KeyID, mid.KeyID)

	// Verifying re-seals under the current key, without a new version of
	// the credential (cached tokens stay valid).
	_, err = svc.Verify(a.ctx, a.owner)
	require.NoError(t, err)
	after, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, rotated.CurrentID(), after.KeyID)
	assert.Equal(t, rotated.CurrentID(), secretbox.BlobKeyID(after.PrivateKeyEnc))
	assert.Equal(t, mid.UpdatedAt, after.UpdatedAt)

	// The previous key can now be dropped.
	_, err = f.service(newRing(t, newKeyBytes, nil)).TokenFor(a.ctx, a.id, acmeApp, codehost.MergeChange)
	require.NoError(t, err)
}

func TestConnectAndRemoveAreOnRecord(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	require.NoError(t, f.svc.Remove(a.ctx, a.owner))

	logs := f.logs.String()
	assert.Contains(t, logs, "codehost_credential_connected")
	assert.Contains(t, logs, "codehost_credential_removed")
	assert.Contains(t, logs, a.owner.ID.String())
}

func TestTokenFor_DoesNotTakeTheTenantFromTheContext(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")

	// Asked for b with a's context on it: b is who is served.
	_, err := f.svc.TokenFor(a.ctx, b.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
	_, err = f.svc.TokenFor(context.Background(), a.id, acmeApp, codehost.OpenChange)
	assert.NoError(t, err)
}

func TestRemove_WorksEvenWhenTheServerHasLostItsEncryptionKey(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	off := f.service(nil)

	require.NoError(t, off.Remove(a.ctx, a.owner), "a credential nothing can read is still the owner's to delete")
	_, err := f.store.Get(a.ctx)
	assert.ErrorIs(t, err, codehost.ErrNotConnected)
}

func TestTokenFor_ReadingChecksNeverAsksToWrite(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	_, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.ReadChecks)
	require.NoError(t, err)
	_, perms := requestedToken(t, f.github.Calls("/access_tokens")[0])
	for name, level := range perms {
		assert.Equal(t, "read", level, name)
	}
}

func TestTokenFor_ACredentialReplacedWhileMintingYieldsNoToken(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.token = func(int) answer {
		// GitHub is being asked on behalf of the old credential when the
		// owner replaces it.
		f.connect(a, 1)
		return answer{Body: tokenBody(secretToken, f.clock().Add(time.Hour), "acme/app")}
	}

	tok, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	assert.ErrorIs(t, err, githubapp.ErrCredentialChanged)
	assert.Nil(t, tok)

	// Nothing was cached from the old credential either.
	f.mintsFor("acme/app")
	_, err = f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)
	assert.Len(t, f.github.Calls("/access_tokens"), 2)
}

func TestVerify_IsNotRecordedAsDoneWhenALaterStepFails(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	f.github.repos = func(int) answer { return answer{Body: map[string]any{"repositories": []any{}}} }
	f.manifest.err = fmt.Errorf("manifest unavailable")

	_, err := f.svc.Verify(a.ctx, a.owner)
	require.Error(t, err)
	st, err := f.svc.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.Nil(t, st.Credential.VerifiedAt)
}

func TestNewService_RefusesMissingDependenciesInsteadOfPanickingLater(t *testing.T) {
	_, err := githubapp.NewService(&githubapp.Options{})
	assert.Error(t, err)
}

func TestToken_NeverPrintsItself(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.mintsFor("acme/app")
	tok, err := f.svc.TokenFor(a.ctx, a.id, acmeApp, codehost.OpenChange)
	require.NoError(t, err)

	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("t", "token", tok)
	asJSON, err := json.Marshal(map[string]any{"token": tok})
	require.NoError(t, err)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", tok), "%+v": fmt.Sprintf("%+v", tok), "%#v": fmt.Sprintf("%#v", tok),
		"%s": fmt.Sprintf("%s", tok), "slog": logs.String(), "json": string(asJSON),
		"error": fmt.Errorf("wrapped: %w", fmt.Errorf("%v", tok)).Error(),
	} {
		assert.NotContains(t, out, secretToken, name)
		assert.Contains(t, out, "[redacted]", name)
	}
}

func TestReseal_OnlyTouchesTheRowItWasGiven(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	f.connect(a, 0)
	f.connect(b, 1)
	recA, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	recB, err := f.store.Get(b.ctx)
	require.NoError(t, err)

	// A blob made for a row that has since been replaced matches nothing.
	ok, err := f.store.Reseal(a.ctx, uuid.New(), []byte("stale"), "0123456789abcdef")
	require.NoError(t, err)
	assert.False(t, ok)
	// And a tenant cannot reseal another's row even knowing its id.
	ok, err = f.store.Reseal(b.ctx, recA.ID, []byte("hijack"), "0123456789abcdef")
	require.NoError(t, err)
	assert.False(t, ok)

	afterA, err := f.store.Get(a.ctx)
	require.NoError(t, err)
	afterB, err := f.store.Get(b.ctx)
	require.NoError(t, err)
	assert.Equal(t, recA.PrivateKeyEnc, afterA.PrivateKeyEnc)
	assert.Equal(t, recB.PrivateKeyEnc, afterB.PrivateKeyEnc)
}

func TestVerify_ACredentialReplacedWhileVerifyingIsNotReportedVerified(t *testing.T) {
	f := newFixture(t)
	a := f.tenant()
	f.connect(a, 0)
	f.github.installation = answer{Body: map[string]any{"account": map[string]string{"login": "acme"}}}
	f.mintsFor()
	// The replacement lands while Verify is waiting on GitHub's last answer.
	f.github.repos = func(int) answer {
		f.connect(a, 1)
		return answer{Body: map[string]any{"repositories": []any{}}}
	}

	v, err := f.svc.Verify(a.ctx, a.owner)
	assert.ErrorIs(t, err, githubapp.ErrCredentialChanged)
	assert.Nil(t, v)
	st, err := f.svc.Status(a.ctx, a.owner)
	require.NoError(t, err)
	assert.Nil(t, st.Credential.VerifiedAt, "the new credential was never checked")
}

type failingStore struct {
	githubapp.Store
	err error
}

func (s failingStore) Get(context.Context) (*githubapp.Record, error) { return nil, s.err }

func TestConnected_OnlyAKeyThatOpensCounts(t *testing.T) {
	f := newFixture(t)

	t.Run("no row", func(t *testing.T) {
		got, err := f.svc.Connected(f.tenant().ctx)
		require.NoError(t, err)
		assert.False(t, got)
	})
	t.Run("a stored key that opens", func(t *testing.T) {
		a := f.tenant()
		f.connect(a, 0)
		got, err := f.svc.Connected(a.ctx)
		require.NoError(t, err)
		assert.True(t, got)
	})
	t.Run("credentials switched off", func(t *testing.T) {
		a := f.tenant()
		f.connect(a, 0)
		got, err := f.service(nil).Connected(a.ctx)
		require.NoError(t, err)
		assert.False(t, got)
	})
	t.Run("a key sealed under a key the server no longer has", func(t *testing.T) {
		a := f.tenant()
		f.connect(a, 0)
		got, err := f.service(newRing(t, newKey(t), nil)).Connected(a.ctx)
		require.NoError(t, err)
		assert.False(t, got)
	})
	t.Run("a damaged blob", func(t *testing.T) {
		a := f.tenant()
		f.connect(a, 0)
		_, err := f.owners.Global().Exec(context.Background(),
			`UPDATE codehost_credentials SET private_key_enc = '\xdeadbeef' WHERE tenant_id = $1`, a.id)
		require.NoError(t, err)
		got, err := f.svc.Connected(a.ctx)
		require.NoError(t, err)
		assert.False(t, got)
	})
	t.Run("a blob that decrypts to something that is not a key", func(t *testing.T) {
		a := f.tenant()
		f.connect(a, 0)
		rec, err := f.store.Get(a.ctx)
		require.NoError(t, err)
		blob, err := f.keyring.Seal([]byte("not a key"), secretbox.Binding{Tenant: a.id, Row: rec.ID})
		require.NoError(t, err)
		_, err = f.store.Reseal(a.ctx, rec.ID, blob, f.keyring.CurrentID())
		require.NoError(t, err)
		got, err := f.svc.Connected(a.ctx)
		require.NoError(t, err)
		assert.False(t, got)
		assert.NotContains(t, f.logs.String(), "not a key")
	})
	t.Run("a context with no tenant", func(t *testing.T) {
		_, err := f.svc.Connected(context.Background())
		assert.Error(t, err)
	})
}

func TestConnected_AStoreThatCannotBeReadIsAnError(t *testing.T) {
	f := newFixture(t)
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: f.github.URL()})
	require.NoError(t, err)
	logHandler := slog.NewJSONHandler(f.logs, nil)
	boom := fmt.Errorf("db down")
	svc, err := githubapp.NewService(&githubapp.Options{
		Store: failingStore{err: boom}, Keyring: f.keyring, Client: client, Owners: user.NewPostgresStore(f.database),
		Events: audit.New(logHandler, nil), Logger: logHandler,
	})
	require.NoError(t, err)

	got, err := svc.Connected(f.tenant().ctx)
	assert.ErrorIs(t, err, boom)
	assert.False(t, got)
}
