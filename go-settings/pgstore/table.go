// Package pgstore reads and appends settings in Postgres tables laid out as
// TABLES.md documents. A Table is built for one explicit table and scope; the
// shared queries only ever name the table they were built with, so one scope
// cannot read another scope's rows.
package pgstore

import (
	"fmt"
	"regexp"
)

// identifier is what a table name must match. Names are spliced into SQL, so
// they are checked once, here, and a caller's string never reaches a query
// unchecked.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Membership is the rule of a per-user per-tenant table: a user's rows count
// only since their current membership of the context's tenant began, and only
// a member may write. Someone who left and rejoined starts again from the
// defaults, so an old value cannot switch itself back on.
//
// Table is the app's memberships table. It needs columns tenant, user_id and
// created_at, and the app's current_tenant() function (see TABLES.md).
type Membership struct {
	Table string
}

// Table describes one settings table to the shared queries. Its fields are
// unexported, so the only descriptors that exist are the ones the
// constructors build from a checked name.
type Table struct {
	name string
	// keyCol is the column the first query argument is matched against.
	keyCol string
	// latestOrder is the ORDER BY that seeks the newest row per name.
	latestOrder string
	// membershipTable, when set, restricts reads to rows written since the
	// key's current membership of the context's tenant began, and writes to
	// members.
	membershipTable string
	// actorCol, when set, is the column that records who made a write. Append
	// leaves it NULL; AppendBy fills it.
	actorCol string
	// actorDeletedCol is the boolean beside actorCol that says the actor's
	// account was removed, which is why actorCol is NULL.
	actorDeletedCol string
}

// GlobalUserTable is one user's settings, in every tenant: a global table
// keyed by user_id, with no tenant.
func GlobalUserTable(name string) *Table {
	return &Table{
		name:   mustIdentifier(name),
		keyCol: "user_id",
		// Matches the (user_id, setting_name, created_at DESC, id DESC) index.
		latestOrder: "t.created_at DESC, t.id DESC",
	}
}

// UserTenantTable is one user's settings within the context's tenant. A nil
// membership applies no membership rule.
func UserTenantTable(name string, membership *Membership) *Table {
	t := &Table{
		name:        mustIdentifier(name),
		keyCol:      "user_id",
		latestOrder: "t.id DESC",
	}
	if membership != nil {
		t.membershipTable = mustIdentifier(membership.Table)
	}
	return t
}

// TenantTable is the settings of a whole tenant, the context's. Its rows
// record who changed them.
func TenantTable(name string) *Table {
	return &Table{
		name: mustIdentifier(name),
		// The key is the tenant's id: a tenant-wide setting has no other owner.
		// Row-level security keeps a query to the context's tenant as well.
		keyCol: "tenant_id",
		// Matches the (tenant_id, setting_name, id DESC) index.
		latestOrder:     "t.id DESC",
		actorCol:        "changed_by",
		actorDeletedCol: "changed_by_deleted",
	}
}

// Name is the table's name.
func (t *Table) Name() string { return t.name }

// RecordsActor reports whether AppendBy is available on the table.
func (t *Table) RecordsActor() bool { return t.actorCol != "" }

// HasMembershipRule reports whether the table applies a Membership.
func (t *Table) HasMembershipRule() bool { return t.membershipTable != "" }

// mustIdentifier panics on a name that is not a plain lower-case SQL
// identifier. Table names are program constants, so a bad one is a bug to
// fail on at startup, as regexp.MustCompile does.
func mustIdentifier(name string) string {
	if !identifier.MatchString(name) {
		panic(fmt.Sprintf("pgstore: %q is not a usable table name", name))
	}
	return name
}
