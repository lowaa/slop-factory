package inventory

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CheckTables is the table-coverage check: every table the app's migrations
// create must be in the inventory, and every table in the inventory must
// exist. actual is the app's real tables, read from its migrated database
// (inventorytest.Tables), so a table made by any means is seen.
//
// ignore names tables that are machinery, not data (the migration version
// table).
func (inv *Inventory) CheckTables(actual []string, ignore ...string) error {
	listed := map[string]bool{}
	for _, t := range inv.Tables {
		listed[t.Name] = true
	}
	exists := map[string]bool{}
	var problems []error
	for _, name := range actual {
		exists[name] = true
		if slices.Contains(ignore, name) {
			continue
		}
		if !listed[name] {
			problems = append(problems, fmt.Errorf("table %q is in the database but not in the inventory: add it under tables", name))
		}
	}
	for _, t := range inv.Tables {
		if !exists[t.Name] {
			problems = append(problems, fmt.Errorf("table %q is in the inventory but not in the database: remove it, or check its migration", t.Name))
		}
	}
	return joinSorted("table coverage", problems)
}

// externalKeyPattern matches the config keys that can point the app at
// another service or credential for one: URLs, hosts, endpoints, API keys,
// client ids and provider selectors. The match is by name, so a new
// service added to the config under any ordinary name is caught; a service
// configured under an unusual one needs the name added here.
var externalKeyPattern = regexp.MustCompile(`(_URL|_HOST|_HOST_PORT|_ENDPOINT|_API_KEY|_CLIENT_ID|_PROVIDER|_PROVIDERS)$`)

// CheckConfigKeys is the processor check: every config key that can call out
// to a service (by name, see externalKeyPattern) must be claimed by a
// processor or an internal service, and every key either claims must still
// be in the config. configKeys is the app's config's env var names
// (ConfigKeysFromSource).
func (inv *Inventory) CheckConfigKeys(configKeys []string) error {
	if len(configKeys) == 0 {
		return errors.New("processor check: no config keys were read, so nothing was checked")
	}
	claimed := map[string]string{}
	for _, p := range inv.Processors {
		for _, k := range p.ConfigKeys {
			claimed[k] = "processor " + p.ID
		}
	}
	for _, s := range inv.InternalServices {
		for _, k := range s.ConfigKeys {
			claimed[k] = "internal service " + s.ID
		}
	}
	inConfig := map[string]bool{}
	var problems []error
	for _, k := range configKeys {
		inConfig[k] = true
		if externalKeyPattern.MatchString(k) && claimed[k] == "" {
			problems = append(problems, fmt.Errorf("config key %s can call a service but no processor or internal service lists it: add the service under processors (or internal_services if the operator runs it)", k))
		}
	}
	for k, by := range claimed {
		if !inConfig[k] {
			problems = append(problems, fmt.Errorf("%s lists config key %s, which the config no longer has", by, k))
		}
	}
	return joinSorted("processor check", problems)
}

func joinSorted(what string, problems []error) error {
	if len(problems) == 0 {
		return nil
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Error() < problems[j].Error() })
	return fmt.Errorf("%s failed: %w", what, errors.Join(problems...))
}

// ConfigKeysFromSource reads the env var names out of an app's config
// source, where each app declares its keys in one []flagSpec (or []KeySpec)
// table of rows like
//
//	{"smtp-host", "SMTP_HOST", ""},
//	{Flag: "smtp-host", Key: "SMTP_HOST"},
//
// It reads the syntax tree, so formatting doesn't matter, and refuses a row
// whose env var name isn't a string literal: a partial reading would let a
// new service slip past CheckConfigKeys. Reading the source rather than
// asking the app keeps the inventory out of the app's runtime code.
func ConfigKeysFromSource(src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config.go", src, 0)
	if err != nil {
		return nil, fmt.Errorf("inventory: read config source: %w", err)
	}
	seen := map[string]bool{}
	var problems []error
	ast.Inspect(file, func(n ast.Node) bool {
		table, ok := n.(*ast.CompositeLit)
		if !ok || !isConfigTable(table) {
			return true
		}
		for _, elt := range table.Elts {
			row, ok := elt.(*ast.CompositeLit)
			if !ok {
				problems = append(problems, fmt.Errorf("config row at %s isn't a {flag, ENV_NAME, ...} literal", fset.Position(elt.Pos())))
				continue
			}
			keyExpr, ok := rowKeyExpr(row)
			if !ok {
				problems = append(problems, fmt.Errorf("config row at %s isn't a {flag, ENV_NAME, ...} or {Key: ENV_NAME, ...} literal", fset.Position(elt.Pos())))
				continue
			}
			key, ok := stringLiteral(keyExpr)
			if !ok || !envNameRegexp.MatchString(key) {
				problems = append(problems, fmt.Errorf("config row at %s doesn't name its env var with a string literal", fset.Position(row.Pos())))
				continue
			}
			seen[key] = true
		}
		return false
	})
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

var envNameRegexp = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// rowKeyExpr is the env var name of a table row: the second element of a
// positional row, or the Key field of a keyed one.
func rowKeyExpr(row *ast.CompositeLit) (ast.Expr, bool) {
	if len(row.Elts) == 0 {
		return nil, false
	}
	if _, keyed := row.Elts[0].(*ast.KeyValueExpr); !keyed {
		if len(row.Elts) < 2 {
			return nil, false
		}
		return row.Elts[1], true
	}
	for _, e := range row.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}
		if name, ok := kv.Key.(*ast.Ident); ok && name.Name == "Key" {
			return kv.Value, true
		}
	}
	return nil, false
}

// isConfigTable reports whether lit is a []flagSpec or []KeySpec, the types
// the apps declare their key table with. Matching on the type rather than the rows'
// shape means a malformed row is an error, not a table that goes unseen.
func isConfigTable(lit *ast.CompositeLit) bool {
	arr, ok := lit.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	elem, ok := arr.Elt.(*ast.Ident)
	return ok && (elem.Name == "flagSpec" || elem.Name == "KeySpec")
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// ParsePeriod reads a retention period of the inventory's form ("14d",
// "12mo") as a duration. A month is 30 days and a year 365: retention
// periods are lower bounds on when data is deleted, not calendar dates.
func ParsePeriod(p string) (time.Duration, bool) {
	if !durationRegexp.MatchString(p) {
		return 0, false
	}
	i := strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' })
	n, err := strconv.Atoi(p[:i])
	if err != nil {
		return 0, false
	}
	unit := map[string]time.Duration{
		"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
		"mo": 30 * 24 * time.Hour, "y": 365 * 24 * time.Hour,
	}[p[i:]]
	if unit == 0 || time.Duration(n) > math.MaxInt64/unit {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// CheckEnforcedPeriods is the retention check: each period the app's code
// enforces (by retention class id, as its config defaults give them) must be
// the one the inventory states for that class, so the policy generated from
// the inventory is what the system does. A class the inventory has no
// duration for fails too: code that deletes on a timer under a class that
// says "indefinite" is the case this exists to catch.
func (inv *Inventory) CheckEnforcedPeriods(enforced map[string]time.Duration) error {
	byID := map[string]RetentionClass{}
	for _, r := range inv.RetentionClasses {
		byID[r.ID] = r
	}
	var problems []error
	for id, got := range enforced {
		class, ok := byID[id]
		if !ok {
			problems = append(problems, fmt.Errorf("the code enforces retention class %q, which is not in the inventory", id))
			continue
		}
		want, isDuration := ParsePeriod(class.Period)
		switch {
		case !isDuration:
			problems = append(problems, fmt.Errorf("class %q is enforced as %s in code but its period in the inventory is %q", id, got, class.Period))
		case want != got:
			problems = append(problems, fmt.Errorf("class %q is enforced as %s in code but the inventory says %s (%s)", id, got, class.Period, want))
		}
	}
	return joinSorted("retention periods", problems)
}
