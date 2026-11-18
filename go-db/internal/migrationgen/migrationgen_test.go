package migrationgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/internal/migrationgen"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

func TestCreateFirstMigrationInEmptyDir(t *testing.T) {
	dir := t.TempDir()

	file, err := migrationgen.Create(dir, "widgets", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wantFile := filepath.Join(dir, "000001_widgets.sql")
	if file != wantFile {
		t.Errorf("file = %q, want %q", file, wantFile)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("stat %s: %v", file, err)
	}
	latest, err := os.ReadFile(filepath.Join(dir, migrations.LatestFileName))
	require.NoError(t, err)
	assert.Equal(t, "000001_widgets\n", string(latest))
}

func TestCreateAssignsNextVersion(t *testing.T) {
	dir := t.TempDir()

	if _, err := migrationgen.Create(dir, "first", false); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := migrationgen.Create(dir, "second", false); err != nil {
		t.Fatalf("create second: %v", err)
	}

	file, err := migrationgen.Create(dir, "third", false)
	if err != nil {
		t.Fatalf("create third: %v", err)
	}
	if want := filepath.Join(dir, "000003_third.sql"); file != want {
		t.Errorf("file = %q, want %q", file, want)
	}
}

// TestCreateIgnoresNonMigrationEntries proves a stray file or an unnumbered
// file already in dir (neither of which nextVersion should treat as a
// migration) doesn't perturb version assignment.
func TestCreateIgnoresNonMigrationEntries(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not_a_migration.sql"), []byte("notes"), 0o644); err != nil {
		t.Fatalf("write stray sql: %v", err)
	}

	file, err := migrationgen.Create(dir, "widgets", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := filepath.Join(dir, "000001_widgets.sql"); file != want {
		t.Errorf("file = %q, want %q", file, want)
	}
}

func TestCreateTenantScopedTemplate(t *testing.T) {
	dir := t.TempDir()

	file, err := migrationgen.Create(dir, "accounts", true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{"tenant_id uuid NOT NULL", "go_db_apply_tenant_rls('accounts')", "-- +goose Up", "-- +goose Down"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("migration missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(string(content), "DROP TABLE IF EXISTS accounts") {
		t.Errorf("migration missing DROP TABLE: %s", content)
	}
}

func TestCreateNonTenantScopedTemplate(t *testing.T) {
	dir := t.TempDir()

	file, err := migrationgen.Create(dir, "global_currencies", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if strings.Contains(string(content), "go_db_apply_tenant_rls") {
		t.Errorf("non-tenant-scoped migration should not call go_db_apply_tenant_rls:\n%s", content)
	}
	if !strings.Contains(string(content), "CREATE TABLE global_currencies") {
		t.Errorf("migration missing CREATE TABLE:\n%s", content)
	}
}

func TestCreateRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"BadName", "has space", "1starts_with_digit", "has-dash", ""} {
		if _, err := migrationgen.Create(dir, name, false); err == nil {
			t.Errorf("name %q: want error, got nil", name)
		}
	}
}

// TestGeneratedMigrationPassesLint proves the scaffolded output isn't just
// well-formed on its own terms, but actually satisfies
// migrations.LintGlobalTenantInvariants -- the thing a real developer would
// run in CI right after generating a migration.
func TestGeneratedMigrationPassesLint(t *testing.T) {
	dir := t.TempDir()

	if _, err := migrationgen.Create(dir, "orders", true); err != nil {
		t.Fatalf("create tenant-scoped: %v", err)
	}
	if _, err := migrationgen.Create(dir, "global_plans", false); err != nil {
		t.Fatalf("create global: %v", err)
	}

	errs := migrations.LintGlobalTenantInvariants(os.DirFS(dir))
	for _, err := range errs {
		t.Error(err)
	}
}

func TestCreateWritesLatest(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"first", "second"} {
		_, err := migrationgen.Create(dir, name, false)
		require.NoError(t, err)
	}

	latest, err := os.ReadFile(filepath.Join(dir, migrations.LatestFileName))
	require.NoError(t, err)
	assert.Equal(t, "000002_second\n", string(latest))
	assert.Empty(t, (&migrations.SequenceLint{}).Lint(os.DirFS(dir)))
}

// The point of LATEST: two branches each scaffolding "the next" migration
// must collide in git, not later in a failing test.
func TestTwoBranchesAddingAMigrationConflictOnLatestInGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	migDir := filepath.Join(repo, "migrations")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	addMigration := func(branch, name string) {
		git("checkout", "-q", "-b", branch, "base")
		_, err := migrationgen.Create(migDir, name, false)
		require.NoError(t, err)
		git("add", "-A")
		git("commit", "-q", "-m", name)
	}

	git("init", "-q", "-b", "base")
	_, err := migrationgen.Create(migDir, "initial", false)
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-q", "-m", "initial")

	addMigration("one", "alpha")
	addMigration("two", "beta")

	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "merge", "--no-edit", "one")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "merging two branches that each add a migration must conflict:\n%s", out)
	assert.Contains(t, string(out), "CONFLICT (content): Merge conflict in migrations/LATEST")
}
