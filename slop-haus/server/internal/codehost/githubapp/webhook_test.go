package githubapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

const (
	hooksBase   = "https://hooks.test"
	expectedURL = hooksBase + githubapp.AppWebhookPath
)

type patchBody struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
	InsecureSSL string `json:"insecure_ssl"`
}

func (f *fixture) configurer(ring *secretbox.Keyring, store githubapp.Store) *githubapp.WebhookConfigurer {
	f.t.Helper()
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: f.github.URL()})
	require.NoError(f.t, err)
	c, err := githubapp.NewWebhookConfigurer(&githubapp.WebhookOptions{
		Store: store, Keyring: ring, Client: client, PublicBaseURL: hooksBase, Now: f.clock,
		Logger: slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
	require.NoError(f.t, err)
	return c
}

// ghHasURL makes GitHub report url as the App's webhook URL.
func (f *fixture) ghHasURL(url string) {
	f.github.hookConfig = answer{Body: map[string]any{"url": url, "content_type": "json", "insecure_ssl": "0"}}
}

func (f *fixture) patches() []patchBody {
	var out []patchBody
	for _, r := range f.github.Calls("/app/hook/config") {
		if r.Method != http.MethodPatch {
			continue
		}
		var b patchBody
		require.NoError(f.t, json.Unmarshal(r.Body, &b))
		out = append(out, b)
	}
	return out
}

// storedSecret reads the sealed secret past row level security and opens it.
func (f *fixture) storedSecret(tn *tenant, ring *secretbox.Keyring) (secret string, blob []byte, url string) {
	f.t.Helper()
	var id string
	require.NoError(f.t, f.owners.Global().QueryRow(context.Background(),
		`SELECT id::text, webhook_secret_enc, coalesce(webhook_url, '') FROM codehost_apps WHERE tenant_id = $1`, tn.id).
		Scan(&id, &blob, &url))
	if blob == nil {
		return "", nil, url
	}
	rec, err := f.store.Get(tn.ctx)
	require.NoError(f.t, err)
	plain, err := ring.Open(blob, secretbox.Binding{Tenant: tn.id, Row: rec.ID, Purpose: "webhook secret"})
	require.NoError(f.t, err)
	return string(plain), blob, url
}

func connected(t *testing.T) (*fixture, *tenant, *githubapp.WebhookConfigurer) {
	f := newFixture(t)
	tn := f.tenant()
	f.connect(tn, 0)
	return f, tn, f.configurer(f.keyring, f.store)
}

func TestWebhookApply_SetsGitHubAndStoresTheSecretSealed(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")

	res, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookApplied, res.Outcome)

	patches := f.patches()
	require.Len(t, patches, 1)
	assert.Equal(t, expectedURL, patches[0].URL)
	assert.Equal(t, "json", patches[0].ContentType)
	assert.Equal(t, "0", patches[0].InsecureSSL)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), patches[0].Secret)

	secret, blob, url := f.storedSecret(tn, f.keyring)
	assert.Equal(t, patches[0].Secret, secret, "the stored secret is the one GitHub was given")
	assert.NotContains(t, string(blob), patches[0].Secret)
	assert.Equal(t, expectedURL, url)
	assert.Equal(t, f.keyring.CurrentID(), secretbox.BlobKeyID(blob))
	assert.NotContains(t, f.logs.String(), patches[0].Secret)
}

func TestWebhookApply_SignsWithTheAppToken(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	for _, r := range f.github.Calls("/app/hook/config") {
		assert.Regexp(t, `^Bearer [\w-]+\.[\w-]+\.[\w-]+$`, r.Auth)
	}
}

func TestWebhookReconcile_ReappliesWhenTheURLDiffersKeepingTheSecret(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	first := f.patches()[0].Secret

	f.ghHasURL(hooksBase + "/old/path")
	res, err := c.Reconcile(tn.ctx)
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookApplied, res.Outcome)
	assert.Equal(t, hooksBase+"/old/path", res.GitHubURL)

	patches := f.patches()
	require.Len(t, patches, 2)
	assert.Equal(t, expectedURL, patches[1].URL)
	assert.Equal(t, first, patches[1].Secret, "an address change must not change the secret")
}

func TestWebhookReconcile_LeavesAMatchingConfigurationUntouched(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	_, blobBefore, _ := f.storedSecret(tn, f.keyring)

	f.ghHasURL(expectedURL)
	res, err := c.Reconcile(tn.ctx)
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookUnchanged, res.Outcome)
	assert.Len(t, f.patches(), 1, "nothing was sent to GitHub")
	_, blobAfter, _ := f.storedSecret(tn, f.keyring)
	assert.Equal(t, blobBefore, blobAfter)
}

func TestWebhookReconcile_AppliesWhenNoSecretWasEverStored(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL(expectedURL)
	res, err := c.Reconcile(tn.ctx)
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookApplied, res.Outcome)
	require.Len(t, f.patches(), 1)
}

func TestWebhookReconcile_ReSealsAMatchingSecretUnderTheCurrentKey(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)

	ring := newRing(t, newKey(t), f.keyBytes)
	f.ghHasURL(expectedURL)
	res, err := f.configurer(ring, f.store).Reconcile(tn.ctx)
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookUnchanged, res.Outcome)

	secret, blob, _ := f.storedSecret(tn, ring)
	assert.Equal(t, ring.CurrentID(), secretbox.BlobKeyID(blob))
	assert.Equal(t, f.patches()[0].Secret, secret)
}

func TestWebhookRotate_SetsGitHubFirstThenStores(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	before, _, _ := f.storedSecret(tn, f.keyring)

	f.ghHasURL(expectedURL)
	require.NoError(t, c.Rotate(tn.ctx, githubapp.ApplyOptions{}))
	patches := f.patches()
	require.Len(t, patches, 2)
	assert.NotEqual(t, before, patches[1].Secret)
	after, _, _ := f.storedSecret(tn, f.keyring)
	assert.Equal(t, patches[1].Secret, after)
}

func TestWebhookRotate_GitHubFailingLeavesTheStoredSecretAlone(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	before, blobBefore, _ := f.storedSecret(tn, f.keyring)

	f.ghHasURL(expectedURL)
	f.github.hookPatch = answer{Status: http.StatusInternalServerError, Body: map[string]any{"message": "boom"}}
	err = c.Rotate(tn.ctx, githubapp.ApplyOptions{})
	require.Error(t, err)

	attempted := f.patches()[1].Secret
	after, blobAfter, _ := f.storedSecret(tn, f.keyring)
	assert.Equal(t, before, after, "GitHub still has the old secret, so the store must too")
	assert.Equal(t, blobBefore, blobAfter)
	assert.NotContains(t, err.Error(), attempted)
	assert.NotContains(t, err.Error(), before)
	assert.NotContains(t, f.logs.String(), attempted)
	assert.NotContains(t, f.logs.String(), before)
}

func TestWebhookApply_GitHubFailingStoresNothing(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	f.github.hookPatch = answer{Status: http.StatusUnprocessableEntity, Body: map[string]any{"message": "Validation Failed"}}
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.Error(t, err)
	secret, blob, url := f.storedSecret(tn, f.keyring)
	assert.Empty(t, secret)
	assert.Nil(t, blob)
	assert.Empty(t, url)
}

func TestWebhook_RefusesAURLOnAnotherHostUnlessToldTo(t *testing.T) {
	for name, act := range map[string]func(*githubapp.WebhookConfigurer, context.Context) error{
		"apply": func(c *githubapp.WebhookConfigurer, ctx context.Context) error {
			_, err := c.Apply(ctx, githubapp.ApplyOptions{})
			return err
		},
		"reconcile": func(c *githubapp.WebhookConfigurer, ctx context.Context) error {
			_, err := c.Reconcile(ctx)
			return err
		},
		"rotate": func(c *githubapp.WebhookConfigurer, ctx context.Context) error {
			return c.Rotate(ctx, githubapp.ApplyOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, tn, c := connected(t)
			const foreign = "https://hooks.other.test/hooks/github-app"
			f.ghHasURL(foreign)

			err := act(c, tn.ctx)
			var fe *githubapp.ForeignWebhookError
			require.ErrorAs(t, err, &fe)
			assert.Equal(t, foreign, fe.Current)
			assert.Equal(t, expectedURL, fe.Expected)
			assert.Empty(t, f.patches(), "another environment's URL must not be overwritten")
			_, blob, _ := f.storedSecret(tn, f.keyring)
			assert.Nil(t, blob)
		})
	}
}

func TestWebhookApply_TakeOverReplacesAForeignURL(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("https://hooks.other.test/x")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{TakeOver: true})
	require.NoError(t, err)
	require.Len(t, f.patches(), 1)
	assert.Equal(t, expectedURL, f.patches()[0].URL)
}

func TestWebhook_NoCredentialIsNotConnected(t *testing.T) {
	f := newFixture(t)
	c := f.configurer(f.keyring, f.store)
	_, err := c.Apply(f.tenant().ctx, githubapp.ApplyOptions{})
	require.Error(t, err)
	assert.Empty(t, f.github.Calls("/app/hook/config"))
}

func TestWebhook_ReplacingTheCredentialDropsItsSecret(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)

	f.connect(tn, 1)
	secret, blob, url := f.storedSecret(tn, f.keyring)
	assert.Empty(t, secret)
	assert.Nil(t, blob)
	assert.Empty(t, url)
}

func TestWebhookDeliveries_ReadsTheAppsRecentDeliveries(t *testing.T) {
	f, tn, c := connected(t)
	f.github.hookDeliveries = answer{Body: []map[string]any{
		{"id": 1, "guid": "g1", "delivered_at": "2026-10-05T12:00:00Z", "redelivery": false, "status_code": 200, "event": "pull_request", "action": "closed"},
	}}
	got, err := c.Deliveries(tn.ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "pull_request", got[0].Event)
	assert.Equal(t, 200, got[0].StatusCode)
	assert.Equal(t, "per_page=30", f.github.Calls("/app/hook/deliveries")[0].Query)
}

func TestNewWebhookConfigurer_RefusesABaseURLWithAPath(t *testing.T) {
	f := newFixture(t)
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: f.github.URL()})
	require.NoError(t, err)
	for _, base := range []string{"", "ftp://x", "https://x/path", "https://u:p@x", "https://x?q=1"} {
		_, err := githubapp.NewWebhookConfigurer(&githubapp.WebhookOptions{
			Store: f.store, Keyring: f.keyring, Client: client, PublicBaseURL: base,
			Logger: slog.NewJSONHandler(f.logs, nil),
		})
		assert.Error(t, err, fmt.Sprintf("base %q", base))
	}
}

func TestWebhookSecret_NeverPrints(t *testing.T) {
	s := githubapp.WebhookSecretForTest()
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v %s", s, s, s, s), s.Reveal())
	b, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(b), s.Reveal())
}

// A deadline turns a hang into a failure with a goroutine dump in seconds,
// not a CI slot held to the package timeout.
const lockTestDeadline = 30 * time.Second

func TestWebhook_ConcurrentRotationsLeaveTheStoreWithWhatGitHubEndedWith(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL(expectedURL)
	ctx, cancel := context.WithTimeout(tn.ctx, lockTestDeadline)
	defer cancel()

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, c.Rotate(ctx, githubapp.ApplyOptions{}))
		}()
	}
	wg.Wait()

	patches := f.patches()
	require.Len(t, patches, 6)
	stored, _, _ := f.storedSecret(tn, f.keyring)
	assert.Equal(t, patches[len(patches)-1].Secret, stored, "GitHub's last secret is the stored one")
}

// Every lock holder needs the pool for its reads, so with as many callers as
// the pool has connections the holder must reuse its own transaction rather
// than ask for another connection.
func TestWebhook_CallersFillingTheConnectionPoolStillComplete(t *testing.T) {
	const conns = 3
	ownerDSN := dbtest.WithOwnerDSN(t)
	role, password := dbtest.AppCredentials()
	appDSN, err := db.WithCredentials(ownerDSN, role, password)
	require.NoError(t, err)
	sep := "?"
	if strings.Contains(appDSN, "?") {
		sep = "&"
	}
	appDSN += sep + "pool_max_conns=" + strconv.Itoa(conns)
	app, err := db.New(context.Background(), appDSN)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	owner, err := db.New(context.Background(), ownerDSN)
	require.NoError(t, err)
	t.Cleanup(owner.Close)

	f := newFixtureOver(t, app, owner)
	tn := f.tenant()
	f.connect(tn, 0)
	c := f.configurer(f.keyring, f.store)
	f.ghHasURL(expectedURL)
	ctx, cancel := context.WithTimeout(tn.ctx, lockTestDeadline)
	defer cancel()

	var wg sync.WaitGroup
	for range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, c.Rotate(ctx, githubapp.ApplyOptions{}))
		}()
	}
	wg.Wait()
	assert.Len(t, f.patches(), conns)
}

// failingSetWebhook is a store whose webhook write finds the credential
// gone, as when it is replaced while GitHub is being called.
type failingSetWebhook struct{ *githubapp.PostgresStore }

func (failingSetWebhook) SetWebhook(context.Context, uuid.UUID, []byte, string, string) (bool, error) {
	return false, nil
}

func TestWebhookApply_ACredentialReplacedMeanwhileIsReportedAndNothingIsStored(t *testing.T) {
	f, tn, _ := connected(t)
	f.ghHasURL("")
	c := f.configurer(f.keyring, failingSetWebhook{f.store})
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.ErrorIs(t, err, githubapp.ErrCredentialChanged)
	_, blob, _ := f.storedSecret(tn, f.keyring)
	assert.Nil(t, blob)
}

// The receiver opens a copy of the sealed secret, made by a trigger on the
// credential row, and checks GitHub's signature with it.
func TestOpenWebhookSecret_OpensWhatApplyStoredAndNothingElse(t *testing.T) {
	f, tn, c := connected(t)
	f.ghHasURL("")
	_, err := c.Apply(tn.ctx, githubapp.ApplyOptions{})
	require.NoError(t, err)
	given := f.patches()[0].Secret

	var credential uuid.UUID
	var copied []byte
	require.NoError(t, f.owners.Global().QueryRow(context.Background(),
		`SELECT app_ref, webhook_secret_enc FROM global_github_app_installations WHERE tenant = $1`, tn.id).Scan(&credential, &copied))

	opened, err := githubapp.OpenWebhookSecret(f.keyring, tn.id, credential, copied)
	require.NoError(t, err)
	assert.Equal(t, given, opened.Reveal())

	_, err = githubapp.OpenWebhookSecret(f.keyring, uuid.New(), credential, copied)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen, "bound to its tenant")
	_, err = githubapp.OpenWebhookSecret(f.keyring, tn.id, uuid.New(), copied)
	assert.ErrorIs(t, err, secretbox.ErrCannotOpen, "and to its credential")
}
