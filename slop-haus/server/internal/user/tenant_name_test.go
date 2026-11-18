package user_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestValidateTenantName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"trimmed", "  Acme Web \t", "Acme Web", nil},
		{"empty", "", "", user.ErrTenantNameEmpty},
		{"only spaces", "   ", "", user.ErrTenantNameEmpty},
		{"forty runes of three bytes each", strings.Repeat("日", 40), strings.Repeat("日", 40), nil},
		{"forty-one runes", strings.Repeat("日", 41), "", user.ErrTenantNameTooLong},
		{"newline", "a\nb", "", user.ErrTenantNameInvalid},
		{"tab inside", "a\tb", "", user.ErrTenantNameInvalid},
		{"nul", "a\x00b", "", user.ErrTenantNameInvalid},
		{"unicode line separator", "a b", "", user.ErrTenantNameInvalid},
		{"escape", "a\x1bb", "", user.ErrTenantNameInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := user.ValidateTenantName(c.in)
			if c.err != nil {
				assert.ErrorIs(t, err, c.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestDefaultTenantNamesFollowTheRules(t *testing.T) {
	f := newFixture(t)
	for address, display := range map[string]string{
		"a@example.com": "  Two\nlines\u2028here  ",
		"b@example.com": " \t ",
	} {
		require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: address, Password: password, DisplayName: display}))
		require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, f.mail.last(t, address))))
	}
	a := f.signIn(t, "a@example.com")
	assert.Equal(t, "Two lines here", tenantNamed(t, f, a, a.TenantID).Name)
	b := f.signIn(t, "b@example.com")
	name := tenantNamed(t, f, b, b.TenantID).Name
	_, err := user.ValidateTenantName(name)
	assert.NoError(t, err, "a display name with nothing usable in it still gives a valid name, %q", name)
}

func tenantNamed(t *testing.T, f *fixture, who *authn.User, id interface{ String() string }) user.TenantSummary {
	t.Helper()
	tenants, err := f.svc.Tenants(f.ctx, who)
	require.NoError(t, err)
	for _, tn := range tenants {
		if tn.ID.String() == id.String() {
			return tn
		}
	}
	require.FailNow(t, "tenant not listed", id.String())
	return user.TenantSummary{}
}

func TestAnOwnerRenamesTheirTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	got, err := f.svc.RenameTenant(f.ctx, a, "  Acme Web ")
	require.NoError(t, err)
	assert.Equal(t, "Acme Web", got.Name)
	assert.Equal(t, "Acme Web", tenantNamed(t, f, a, a.TenantID).Name)
	assert.Equal(t, 1, f.events.Count(audit.TenantRenamed), "the rename is audited")
	assert.Equal(t, "Acme Web", f.count0(t, `SELECT name FROM global_tenants WHERE id = $1`, a.TenantID))

	_, err = f.svc.RenameTenant(f.ctx, a, "ACME web")
	assert.NoError(t, err, "changing only the case of its own name is not a clash with itself")
}

func (f *fixture) count0(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	require.NoError(t, f.db.Global().QueryRow(f.ctx, sql, args...).Scan(&s))
	return s
}

func TestAMemberCannotRenameTheTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	b := f.signIn(t, "b@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)
	inA := &authn.User{ID: b.ID, TenantID: a.TenantID}

	_, err = f.svc.RenameTenant(f.ctx, inA, "Mine now")
	assert.ErrorIs(t, err, user.ErrNotOwner)
	assert.Equal(t, "Someone", tenantNamed(t, f, a, a.TenantID).Name)
	assert.Equal(t, 1, f.events.Count(audit.TenantRenameRefused))
	assert.Zero(t, f.events.Count(audit.TenantRenamed))

	_, err = f.svc.RenameTenant(f.ctx, &authn.User{ID: b.ID, TenantID: a.TenantID}, "")
	assert.ErrorIs(t, err, user.ErrTenantNameEmpty, "the name is checked whoever asks")
}

func TestRenamingRefusesNamesThatBreakTheRules(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	for in, want := range map[string]error{
		"":                      user.ErrTenantNameEmpty,
		strings.Repeat("x", 41): user.ErrTenantNameTooLong,
		"two\nlines":            user.ErrTenantNameInvalid,
	} {
		_, err := f.svc.RenameTenant(f.ctx, a, in)
		assert.ErrorIs(t, err, want, "%q", in)
	}
	assert.Equal(t, "Someone", tenantNamed(t, f, a, a.TenantID).Name)
	assert.Zero(t, f.events.Count(audit.TenantRenamed))
}

// Names are not unique across the board: only a person's own switcher must
// not show two the same.
func TestATenantNameClashesOnlyWithTheOwnersOtherTenants(t *testing.T) {
	f := newFixture(t)
	admin := f.seededAdmin(t, "admin@example.com")
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")

	_, err := f.svc.RenameTenant(f.ctx, b, "Shared name")
	require.NoError(t, err)
	_, err = f.svc.RenameTenant(f.ctx, a, "shared NAME")
	assert.NoError(t, err, "a tenant a is not in doesn't count")

	_, err = f.svc.InviteToNewTenant(f.ctx, admin, "a@example.com")
	require.NoError(t, err)
	second, err := f.svc.AcceptInvite(f.ctx, a, tokenFrom(t, f.mail.last(t, "a@example.com")), "Second")
	require.NoError(t, err)
	inSecond := &authn.User{ID: a.ID, TenantID: second.Profile.TenantID}

	_, err = f.svc.RenameTenant(f.ctx, inSecond, "  SHARED name ")
	assert.ErrorIs(t, err, user.ErrTenantNameTaken, "equal ignoring case to a's other tenant")
	assert.Equal(t, "Second", tenantNamed(t, f, a, second.Profile.TenantID).Name)
}

func TestAcceptingANewTenantInviteAsAnExistingUserNamesIt(t *testing.T) {
	f := newFixture(t)
	admin := f.seededAdmin(t, "admin@example.com")
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	invite := func() string {
		_, err := f.svc.InviteToNewTenant(f.ctx, admin, "a@example.com")
		require.NoError(t, err)
		return tokenFrom(t, f.mail.last(t, "a@example.com"))
	}

	token := invite()
	_, err := f.svc.AcceptInvite(f.ctx, a, token, "")
	require.ErrorIs(t, err, user.ErrTenantNameTaken, "left to default, the second tenant would be named like the first")
	_, err = f.svc.AcceptInvite(f.ctx, a, token, " someone ")
	require.ErrorIs(t, err, user.ErrTenantNameTaken, "so would a name that differs only in case and padding")
	_, err = f.svc.PreviewInvite(f.ctx, token)
	require.NoError(t, err, "a refused name spends nothing")
	_, err = f.svc.AcceptInvite(f.ctx, a, token, "bad\nname")
	require.ErrorIs(t, err, user.ErrTenantNameInvalid)

	session, err := f.svc.AcceptInvite(f.ctx, a, token, "Side project")
	require.NoError(t, err)
	assert.Equal(t, "Side project", tenantNamed(t, f, a, session.Profile.TenantID).Name)
	assert.Equal(t, 2, len(tenantIDs(t, f, a)))
}

func TestAcceptingANewTenantInviteAsANewAccountNamesIt(t *testing.T) {
	f := newFixture(t)
	admin := f.seededAdmin(t, "admin@example.com")
	accept := func(address, displayName, tenantName string) (*user.Session, error) {
		_, err := f.svc.InviteToNewTenant(f.ctx, admin, address)
		require.NoError(t, err)
		return f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, address)), displayName, password, tenantName)
	}

	_, err := accept("bad@example.com", "Bad", strings.Repeat("x", 41))
	require.ErrorIs(t, err, user.ErrTenantNameTooLong)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM global_users WHERE email = 'bad@example.com'`), "no account is made for a refused name")

	named, err := accept("n@example.com", "Nell", " Nell's lab ")
	require.NoError(t, err)
	n := f.signIn(t, "n@example.com")
	assert.Equal(t, "Nell's lab", tenantNamed(t, f, n, named.Profile.TenantID).Name)

	defaulted, err := accept("d@example.com", "Dee", "")
	require.NoError(t, err)
	assert.Equal(t, "Dee", tenantNamed(t, f, f.signIn(t, "d@example.com"), defaulted.Profile.TenantID).Name)
}

func TestActivationNamesAFirstTenantAfterItsOwner(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: strings.Repeat("é", 60)}))
	require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, f.mail.last(t, "a@example.com"))))
	a := f.signIn(t, "a@example.com")
	assert.Equal(t, strings.Repeat("é", user.MaxTenantNameLength), tenantNamed(t, f, a, a.TenantID).Name,
		"a display name longer than a tenant name is cut to fit")
}

func TestTheTenantListCarriesOwnersOfOnlyTheCallersTenants(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	f.activeUser(t, "c@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")
	c := f.signIn(t, "c@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)

	tenants, err := f.svc.Tenants(f.ctx, b)
	require.NoError(t, err)
	require.Len(t, tenants, 2)
	byID := map[string]user.TenantSummary{}
	for _, tn := range tenants {
		byID[tn.ID.String()] = tn
	}
	assert.False(t, byID[a.TenantID.String()].IsOwner, "b is a member of a's")
	assert.Equal(t, []string{"Someone"}, byID[a.TenantID.String()].Owners)
	assert.True(t, byID[b.TenantID.String()].IsOwner)
	assert.NotContains(t, byID, c.TenantID.String(), "c's tenant is not b's to see")
}

func TestRenamingOverHTTP(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	owner := newBrowser(t, env.mux)
	owner.signInWithPassword("a@example.com")

	rec := owner.do(http.MethodPatch, "/api/tenant", `{"name":"Renamed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"name":"Renamed"`)
	assert.Equal(t, "Renamed", tenantNamed(t, env.f, a, a.TenantID).Name)

	rec = owner.do(http.MethodPatch, "/api/tenant", `{"name":"a\nb"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	env.member(t, "b@example.com")
	_, err := env.f.svc.InviteToTenant(env.f.ctx, a, "b@example.com")
	require.NoError(t, err)
	member := newBrowser(t, env.mux)
	member.signInWithPassword("b@example.com")
	rec = member.do(http.MethodPost, "/api/invites/accept", `{"token":"`+tokenFrom(t, env.f.mail.last(t, "b@example.com"))+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = member.do(http.MethodPatch, "/api/tenant", `{"name":"Hijacked"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "Renamed", tenantNamed(t, env.f, a, a.TenantID).Name)
}

func TestAcceptingANewTenantInviteWithAProviderUsesTheChosenName(t *testing.T) {
	env := newProviderEnv(t)
	admin := env.f.seededAdmin(t, "admin@example.com")
	_, err := env.f.svc.InviteToNewTenant(env.f.ctx, admin, "n@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, env.f.mail.last(t, "n@example.com"))

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInviteNaming(token, "", "Nell's lab"), asOIDC(env.idp, "sub-n", "nell"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	who := b.session(env.f)
	assert.Equal(t, "Nell's lab", tenantNamed(t, env.f, who, who.TenantID).Name)
}

func TestAProviderInviteForAClashingNameIsRefusedAndSpendsNothing(t *testing.T) {
	env := newProviderEnv(t)
	admin := env.f.seededAdmin(t, "admin@example.com")
	bee := env.member(t, "b@example.com")
	env.link(t, bee, oidcName, "sub-b")
	_, err := env.f.svc.InviteToNewTenant(env.f.ctx, admin, "b@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, env.f.mail.last(t, "b@example.com"))

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInviteNaming(token, "", "someone"), asOIDC(env.idp, "sub-b", "b"))
	requireRefusal(t, rec, user.InvitePage, "tenant_name_taken")
	_, err = env.f.svc.PreviewInvite(env.f.ctx, token)
	assert.NoError(t, err, "a refusal spends nothing")

	body := `{"token":"` + token + `","tenant_name":"bad\nname"}`
	rec = newBrowser(t, env.mux).do(http.MethodPost, "/api/auth/invite/accept/"+oidcName, body)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a bad name fails before the round trip")

	b = newBrowser(t, env.mux)
	rec = b.callback(oidcName, b.startInviteNaming(token, "", "Second"), asOIDC(env.idp, "sub-b", "b"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	who := b.session(env.f)
	assert.Equal(t, "Second", tenantNamed(t, env.f, who, who.TenantID).Name)
}

// The clash check and the write are in one transaction under a lock on the
// person, so a tenant being made by another transaction is seen by the
// check rather than missed. The test is that other transaction: it holds the
// person's lock with an uncommitted tenant called "Same", and the accept
// must wait for it and then refuse "Same".
func TestAcceptingANewTenantInviteWaitsForTheCallersOtherTenantBeingMade(t *testing.T) {
	f := newFixture(t)
	admin := f.seededAdmin(t, "admin@example.com")
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	_, err := f.svc.InviteToNewTenant(f.ctx, admin, "a@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, f.mail.last(t, "a@example.com"))

	locked, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- f.db.InGlobalTx(f.ctx, func(q db.Querier) error {
			tenantID := uuid.New()
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`SELECT 1 FROM global_users WHERE id = $1 FOR UPDATE`, []any{a.ID}},
				{`INSERT INTO global_tenants (id, name) VALUES ($1, 'Same')`, []any{tenantID}},
				{`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`, []any{uuid.New(), tenantID, a.ID}},
			} {
				if _, err := q.Exec(f.ctx, stmt.sql, stmt.args...); err != nil {
					return err
				}
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	accepted := make(chan error, 1)
	go func() {
		_, err := f.svc.AcceptInvite(f.ctx, a, token, "same")
		accepted <- err
	}()
	// Until the accept is seen waiting on a lock it may not have finished: a
	// finished one did not wait, and could not have seen the other tenant.
	for f.count(t, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`) == 0 {
		select {
		case err := <-accepted:
			close(release)
			require.FailNow(t, "the accept did not wait for the other transaction", "%v", err)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	require.NoError(t, <-held)
	assert.ErrorIs(t, <-accepted, user.ErrTenantNameTaken)
}

func TestTheTenantListOverHTTPCarriesOwnersAndOwnership(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.member(t, "b@example.com")
	_, err := env.f.svc.InviteToTenant(env.f.ctx, a, "b@example.com")
	require.NoError(t, err)
	b := newBrowser(t, env.mux)
	b.signInWithPassword("b@example.com")
	rec := b.do(http.MethodPost, "/api/invites/accept", `{"token":"`+tokenFrom(t, env.f.mail.last(t, "b@example.com"))+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = b.do(http.MethodGet, "/api/auth/tenants", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Tenants []user.TenantBody `json:"tenants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Tenants, 2)
	byID := map[string]user.TenantBody{}
	for _, tn := range list.Tenants {
		byID[tn.ID.String()] = tn
	}
	joined := byID[a.TenantID.String()]
	assert.False(t, joined.IsOwner)
	assert.Equal(t, []string{"Someone"}, joined.Owners)
	assert.True(t, joined.Current, "accepting moved the session into it")
	var owned int
	for _, tn := range list.Tenants {
		if tn.IsOwner {
			owned++
			assert.Equal(t, []string{"Someone"}, tn.Owners)
		}
	}
	assert.Equal(t, 1, owned)
}

func TestAnOwnerSetsAndClearsTheirTenantColour(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	assert.Nil(t, tenantNamed(t, f, a, a.TenantID).Colour, "no colour until the owner chooses")

	blue := "blue"
	got, err := f.svc.SetTenantColour(f.ctx, a, &blue)
	require.NoError(t, err)
	require.NotNil(t, got.Colour)
	assert.Equal(t, "blue", *got.Colour)
	assert.Equal(t, "blue", f.count0(t, `SELECT colour FROM global_tenants WHERE id = $1`, a.TenantID))
	assert.Equal(t, 1, f.events.Count(audit.TenantRecoloured))

	got, err = f.svc.SetTenantColour(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, got.Colour)
	assert.Equal(t, 2, f.events.Count(audit.TenantRecoloured))
}

func TestAMemberCannotRecolourTheTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	b := f.signIn(t, "b@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)

	red := "red"
	_, err = f.svc.SetTenantColour(f.ctx, &authn.User{ID: b.ID, TenantID: a.TenantID}, &red)
	assert.ErrorIs(t, err, user.ErrNotOwner)
	assert.Nil(t, tenantNamed(t, f, a, a.TenantID).Colour)
	assert.Equal(t, 1, f.events.Count(audit.TenantRecolourRefused))
	assert.Zero(t, f.events.Count(audit.TenantRecoloured))
}

func TestAnUnknownTenantColourIsRefused(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	chartreuse := "chartreuse"
	_, err := f.svc.SetTenantColour(f.ctx, a, &chartreuse)
	assert.ErrorIs(t, err, user.ErrTenantColourUnknown)
	assert.Zero(t, f.events.Count(audit.TenantRecoloured))
}

func TestTenantColourOverHTTP(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	owner := newBrowser(t, env.mux)
	owner.signInWithPassword("a@example.com")

	rec := owner.do(http.MethodPut, "/api/tenant/colour", `{"colour":"teal"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"colour":"teal"`)

	palette := []string{"red", "orange", "amber", "green", "teal", "blue", "violet", "pink"}
	assert.Equal(t, palette, user.TenantColours, "the palette is the documented eight")
	for _, key := range palette {
		rec = owner.do(http.MethodPut, "/api/tenant/colour", `{"colour":"`+key+`"}`)
		assert.Equal(t, http.StatusOK, rec.Code, "the API's enum accepts %s: %s", key, rec.Body.String())
	}
	rec = owner.do(http.MethodPut, "/api/tenant/colour", `{"colour":"teal"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = owner.do(http.MethodPut, "/api/tenant/colour", `{"colour":"#ff0000"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Equal(t, "teal", *tenantNamed(t, env.f, a, a.TenantID).Colour)

	rec = owner.do(http.MethodPut, "/api/tenant/colour", `{"colour":null}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"colour":null`)
	assert.Nil(t, tenantNamed(t, env.f, a, a.TenantID).Colour)
}
