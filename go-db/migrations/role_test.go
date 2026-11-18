package migrations_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// A table keyed on bigserial is the case uuid-keyed schemas never exercise:
// inserting into it also advances <table>_id_seq, so INSERT on the table is
// not by itself enough. Without the sequence grant this fails with
// "permission denied for sequence counters_id_seq" -- an error that points
// at the sequence rather than at the role setup that actually caused it.
func TestEnsureRole_GrantsSequenceUsageForSerialColumns(t *testing.T) {
	ctx := context.Background()

	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	if _, err := adminEng.ExecContext(ctx,
		`CREATE TABLE counters (id bigserial PRIMARY KEY, label text NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `DROP TABLE IF EXISTS counters`)
	}()

	const role, password = "seq_grant_test_role", "seq_grant_test_password"
	if err := migrations.EnsureRole(ctx, adminEng, role, password); err != nil {
		t.Fatalf("ensure role: %v", err)
	}

	appEng := engine.NewPostgres(connStringAs(t, role, password))
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()

	if _, err := appEng.ExecContext(ctx,
		`INSERT INTO counters (label) VALUES ('first')`); err != nil {
		t.Fatalf("insert as %s: %v", role, err)
	}
}

// Tables created after EnsureRole ran are covered by ALTER DEFAULT
// PRIVILEGES -- migrations routinely add tables later, so the sequence
// grant has to reach them the same way the table grant does.
func TestEnsureRole_GrantsSequenceUsageForTablesCreatedLater(t *testing.T) {
	ctx := context.Background()

	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	const role, password = "seq_default_test_role", "seq_default_test_password"
	if err := migrations.EnsureRole(ctx, adminEng, role, password); err != nil {
		t.Fatalf("ensure role: %v", err)
	}

	if _, err := adminEng.ExecContext(ctx,
		`CREATE TABLE later_counters (id bigserial PRIMARY KEY, label text NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `DROP TABLE IF EXISTS later_counters`)
	}()

	appEng := engine.NewPostgres(connStringAs(t, role, password))
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()

	if _, err := appEng.ExecContext(ctx,
		`INSERT INTO later_counters (label) VALUES ('first')`); err != nil {
		t.Fatalf("insert as %s: %v", role, err)
	}
}

// A password is a secret, not SQL: whatever it contains -- quotes, a
// statement terminator, a dollar-quote -- it must be stored as the
// password and run as nothing.
func TestEnsureRole_QuotesThePassword(t *testing.T) {
	ctx := context.Background()

	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	const role = "quoting_test_role"
	const password = `it's'; CREATE TABLE injected (x int); -- $$ \`
	if err := migrations.EnsureRole(ctx, adminEng, role, password); err != nil {
		t.Fatalf("ensure role: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `DROP TABLE IF EXISTS injected`)
	}()

	var injected bool
	if err := adminEng.QueryRowContext(ctx, `SELECT to_regclass('injected') IS NOT NULL`).Scan(&injected); err != nil {
		t.Fatalf("look for injected table: %v", err)
	}
	if injected {
		t.Fatal("the password was run as SQL")
	}

	appEng := engine.NewPostgres(connStringAs(t, role, password))
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s with the password as given: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()
	if err := appEng.Ping(ctx); err != nil {
		t.Fatalf("ping as %s: %v", role, err)
	}
}

// Every replica runs EnsureRole at startup; the second must not fail
// because the first already made the role.
func TestEnsureRole_IsIdempotent(t *testing.T) {
	ctx := context.Background()

	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	for i := range 2 {
		if err := migrations.EnsureRole(ctx, adminEng, "idempotent_test_role", "pw"); err != nil {
			t.Fatalf("ensure role, run %d: %v", i+1, err)
		}
	}
}

// A role that already exists -- made by hand, or by an earlier deploy
// before a table was added -- is granted, not just accepted.
func TestEnsureRole_GrantsAnExistingRole(t *testing.T) {
	ctx := context.Background()
	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	const role, password = "existing_ungranted_role", "pw"
	if _, err := adminEng.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD '`+password+`'`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := adminEng.ExecContext(ctx, `CREATE TABLE existing_role_rows (label text NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `DROP TABLE IF EXISTS existing_role_rows`)
	}()

	if err := migrations.EnsureRole(ctx, adminEng, role, password); err != nil {
		t.Fatalf("ensure role: %v", err)
	}

	appEng := engine.NewPostgres(connStringAs(t, role, password))
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()
	if _, err := appEng.ExecContext(ctx, `INSERT INTO existing_role_rows (label) VALUES ('x')`); err != nil {
		t.Fatalf("insert as %s: %v", role, err)
	}
}

func connStringAs(t *testing.T, role, password string) string {
	t.Helper()
	u, err := url.Parse(testConnString)
	if err != nil {
		t.Fatalf("parse conn string: %v", err)
	}
	u.User = url.UserPassword(role, password)
	return u.String()
}

// With standard_conforming_strings off, a backslash inside a literal
// escapes the quote after it -- quoting that only doubles quotes can then
// be broken out of. The server's own %L quoting knows the setting.
func TestEnsureRole_QuotesThePasswordWhateverTheStringSetting(t *testing.T) {
	ctx := context.Background()
	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	var dbName string
	if err := adminEng.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatalf("current database: %v", err)
	}
	if _, err := adminEng.ExecContext(ctx, `ALTER DATABASE `+pgxIdent(dbName)+` SET standard_conforming_strings = off`); err != nil {
		t.Fatalf("turn standard_conforming_strings off: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `ALTER DATABASE `+pgxIdent(dbName)+` RESET standard_conforming_strings`)
	}()

	// A fresh engine, so its sessions start with the setting off.
	offEng := engine.NewPostgres(testConnString)
	if err := offEng.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = offEng.Close() }()
	var setting string
	if err := offEng.QueryRowContext(ctx, `SHOW standard_conforming_strings`).Scan(&setting); err != nil || setting != "off" {
		t.Fatalf("expected standard_conforming_strings off, got %q (%v)", setting, err)
	}

	const role = "quoting_off_test_role"
	const password = `x\' ; CREATE TABLE injected_off (x int); -- `
	if err := migrations.EnsureRole(ctx, offEng, role, password); err != nil {
		t.Fatalf("ensure role: %v", err)
	}
	defer func() {
		_, _ = adminEng.ExecContext(context.Background(), `DROP TABLE IF EXISTS injected_off`)
	}()
	var injected bool
	if err := adminEng.QueryRowContext(ctx, `SELECT to_regclass('injected_off') IS NOT NULL`).Scan(&injected); err != nil {
		t.Fatalf("look for injected table: %v", err)
	}
	if injected {
		t.Fatal("the password was run as SQL")
	}
	appEng := engine.NewPostgres(connStringAs(t, role, password))
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s with the password as given: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()
	if err := appEng.Ping(ctx); err != nil {
		t.Fatalf("ping as %s: %v", role, err)
	}
}

// A role that already exists with SUPERUSER or BYPASSRLS would make every
// query the application runs skip row level security. It is refused, not
// granted to.
func TestEnsureRole_RefusesAnExistingPrivilegedRole(t *testing.T) {
	ctx := context.Background()
	adminEng := engine.NewPostgres(testConnString)
	if err := adminEng.Connect(ctx); err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = adminEng.Close() }()

	for _, attr := range []string{"SUPERUSER", "BYPASSRLS"} {
		role := "privileged_" + strings.ToLower(attr) + "_role"
		if _, err := adminEng.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN `+attr); err != nil {
			t.Fatalf("create %s role: %v", attr, err)
		}
		if err := migrations.EnsureRole(ctx, adminEng, role, "pw"); err == nil {
			t.Errorf("an existing %s role must be refused", attr)
		}
	}
}

func pgxIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
