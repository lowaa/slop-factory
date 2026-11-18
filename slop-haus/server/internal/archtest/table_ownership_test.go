package archtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

// tableOwners maps every table to the one package allowed to write it;
// TestEveryTableHasAnOwner fails for a table without an entry, so a new
// migration forces the decision. A table written only by a trigger or by the
// prune sweep is owned by the package whose invariants it records. A package
// that needs a change to another's table asks the owner for a method; two
// writers of one table is how its invariants (a live attempt, a held lease)
// end up defined twice.
var tableOwners = map[string]string{
	"attachment_delete_queue":         "attachment",
	"attachment_reservations":         "attachment",
	"attachment_usage":                "attachment",
	"attachments":                     "attachment",
	"tickets":                         "ticket",
	"attempts":                        "ticket",
	"leases":                          "ticket",
	"locks":                           "ticket",
	"lock_holds":                      "ticket",
	"lock_waiters":                    "ticket",
	"ticket_deps":                     "ticket",
	"ticket_paths":                    "ticket",
	"ticket_comments":                 "ticket",
	"ticket_revisions":                "ticket",
	"ticket_status_changes":           "ticket",
	"attempt_status_changes":          "ticket",
	"ticket_required_roles":           "ticket",
	"ticket_changes":                  "change",
	"agents":                          "agent",
	"sessions":                        "session",
	"pending_actions":                 "pendingaction",
	"workspace_manifests":             "manifest",
	"workspace_manifest_revisions":    "manifest",
	"landings":                        "landing",
	"batches":                         "landing",
	"server_merge_outcomes":           "landing",
	"agent_contexts":                  "ticket",
	"agent_messages":                  "agentmsg",
	"agent_role_members":              "agent",
	"agent_roles":                     "agent",
	"agent_workspace_layouts":         "agent",
	"attempt_signals":                 "attemptsignals",
	"attempt_verdicts":                "attemptverdict",
	"review_runs":                     "reviewreport",
	"review_findings":                 "reviewreport",
	"review_finding_labels":           "reviewreport",
	"review_requests":                 "reviewreport",
	"board_changes":                   "ticket",
	"board_revisions":                 "ticket",
	"codehost_credentials":            "codehost",
	"codehost_setup_states":           "codehost",
	"conductor_conversation_events":   "conductor",
	"conductor_conversations":         "conductor",
	"conductor_queues":                "conductor",
	"conductor_work":                  "conductor",
	"eval_set_batches":                "evalset",
	"eval_sets":                       "evalset",
	"fleet_instruction_archives":      "fleetinstructions",
	"fleet_instruction_defaults":      "fleetinstructions",
	"fleet_instruction_versions":      "fleetinstructions",
	"github_deliveries":               "githubhook",
	"github_event_keys":               "githubhook",
	"global_auth_providers":           "user",
	"global_auth_tokens":              "user",
	"global_consent_events":           "user",
	"global_consent_merge_tokens":     "user",
	"global_former_memberships":       "user",
	"global_github_app_installations": "codehost",
	"global_harnesses":                "harness",
	"global_intake_credentials":       "intake",
	"global_invites":                  "user",
	"global_legal_document_versions":  "user",
	"global_oauth_auth_requests":      "oauth",
	"global_oauth_clients":            "oauth",
	"global_oauth_codes":              "oauth",
	"global_oauth_device_codes":       "oauth",
	"global_oauth_grants":             "oauth",
	"global_oauth_tokens":             "oauth",
	"global_tenant_memberships":       "user",
	"global_tenants":                  "user",
	"global_user_identities":          "user",
	"global_user_settings":            "usersettings",
	"global_users":                    "user",
	"goose_db_version":                "schema",
	"harness_config":                  "harnessconfig",
	"harness_config_revisions":        "harnessconfig",
	"ignored_merges":                  "ticket",
	"intake_deliveries":               "intake",
	"intake_events":                   "ticket",
	"intake_sources":                  "intake",
	"lease_configs":                   "ticket",
	"llm_guidance_usage":              "llmusage",
	"llm_tool_usage":                  "llmusage",
	"llm_usage":                       "llmusage",
	"llm_usage_daily":                 "llmusage",
	"llm_usage_marks":                 "llmusage",
	"merge_drift_decisions":           "change",
	"remote_control_host_leases":      "agent",
	"scope_blocks":                    "ticket",
	"scope_extension_denials":         "ticket",
	"scope_narrowings":                "ticket",
	"steward_assessments":             "steward",
	"steward_discussion_applies":      "steward",
	"steward_discussion_decisions":    "steward",
	"steward_discussion_labels":       "steward",
	"steward_drafts":                  "steward",
	"steward_leases":                  "steward",
	"supervisor_desired_agents":       "agent",
	"supervisor_desired_state":        "agent",
	"supervisor_slot_states":          "supervisorwatch",
	"tenant_settings":                 "settings",
	"ticket_sequences":                "ticket",
	"ticket_snapshots":                "ticket",
	"usage_declarations":              "llmusage",
	"user_tenant_settings":            "usersettings",
	"variant_archives":                "variant",
	"variant_versions":                "variant",
	"worker_polls":                    "wakeup",
}

// seedPackages write test fixtures straight into the tables; they are not
// part of the running server.
var seedPackages = map[string]bool{"schema/seeddata": true, "db/dbtest": true}

// knownWriters maps "table: package/file" to why a non-owner writes it. The
// list only shrinks: arch S9 and later tickets retire these.
var knownWriters = map[string]string{
	"tickets: change/store.go":                           "change records the ticket's base_commit itself",
	"agents: ticket/server_claim.go":                     "ticket seeds the server console agent itself",
	"ticket_changes: ticket/attempt_merged_into_base.go": "ticket marks a change merged into base itself",
	"ticket_paths: manifest/move_store.go":               "manifest moves a ticket's scope paths itself",
	"intake_sources: manifest/move_store.go":             "manifest moves a ticket's intake source itself",
	"global_tenants: smoke/smoke.go":                     "the smoke check provisions its tenant itself",
}

var (
	writeSQL = regexp.MustCompile(`(?is)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+([a-z_]+)\b`)
)

// writesIn returns "table: package/file" for every owned-table write by a
// package that does not own the table.
func writesIn(root string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		r, _ := filepath.Rel(root, path)
		r = filepath.ToSlash(r)
		pkgDir := filepath.ToSlash(filepath.Dir(r))
		if seedPackages[pkgDir] {
			return nil
		}
		// A subpackage is its parent's code (landing/guidance is landing), so
		// the owner is the top-level directory and only the owner's own tree
		// may write. servermerge/runner is thus a non-owner, not invisible.
		pkgName := strings.SplitN(r, "/", 2)[0]
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range writeSQL.FindAllStringSubmatch(string(src), -1) {
			owner, owned := tableOwners[strings.ToLower(m[1])]
			if owned && (owner != pkgName) {
				seen[fmt.Sprintf("%s: %s", strings.ToLower(m[1]), r)] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, err
}

func TestNonOwnersDoNotWriteOwnedTables(t *testing.T) {
	found, err := writesIn("..")
	require.NoError(t, err)
	fresh, stale := compare(found, knownWriters)
	assert.Empty(t, fresh, "a package writes a table it does not own: add a method to the owner, or (rarely) list it in knownWriters with a reason")
	assert.Empty(t, stale, "fixed: remove from knownWriters")
}

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

// diffTables returns the tables with no owner and the owners of no table.
func diffTables(tables []string, owners map[string]string) (unowned, stale []string) {
	have := map[string]bool{}
	for _, t := range tables {
		have[t] = true
		if _, ok := owners[t]; !ok {
			unowned = append(unowned, t)
		}
	}
	for t := range owners {
		if !have[t] {
			stale = append(stale, t)
		}
	}
	sort.Strings(unowned)
	sort.Strings(stale)
	return unowned, stale
}

func TestEveryTableHasAnOwner(t *testing.T) {
	database := dbtest.WithDB(t)
	rows, err := database.Global().Query(context.Background(),
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	require.NoError(t, err)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	unowned, stale := diffTables(tables, tableOwners)
	assert.Empty(t, unowned, "a table has no owner: name the package that writes it in tableOwners")
	assert.Empty(t, stale, "tableOwners names a table that no longer exists: remove it")
}

// A typo in an owner would otherwise pass for a table nothing writes.
func TestEveryOwnerIsAPackage(t *testing.T) {
	for table, owner := range tableOwners {
		info, err := os.Stat(filepath.Join("..", owner))
		assert.NoError(t, err, "owner of %s", table)
		if err == nil {
			assert.True(t, info.IsDir(), "owner of %s", table)
		}
	}
}

func TestTheOwnerGuardSeesAMissingOwner(t *testing.T) {
	unowned, stale := diffTables([]string{"a", "b"}, map[string]string{"b": "x", "gone": "y"})
	assert.Equal(t, []string{"a"}, unowned)
	assert.Equal(t, []string{"gone"}, stale)
}

func TestTheOwnershipGuardSeesAWriter(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	put("ticket/ok.go", "const q = `UPDATE tickets SET x = 1`")
	put("other/bad.go", "const q = `UPDATE\n\ttickets SET x = 1; delete from leases`")
	put("other/read.go", "const q = `SELECT 1 FROM tickets`")
	put("other/mine_test.go", "const q = `UPDATE tickets SET x = 1`")
	put("ticket/sub/ok.go", "const q = `INSERT INTO attempts (a) VALUES (1)`")
	put("landing/guidance/ok.go", "const q = `UPDATE landings SET x = 1`")
	put("servermerge/runner/bad.go", "const q = `DELETE FROM server_merge_outcomes`")
	put("schema/seeddata/seed.go", "const q = `UPDATE tickets SET x = 1`")
	got, err := writesIn(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"leases: other/bad.go",
		"server_merge_outcomes: servermerge/runner/bad.go",
		"tickets: other/bad.go",
	}, got)
}
