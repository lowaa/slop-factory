package inventory

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	idPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	configKeyRegexp = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	tableRegexp     = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	durationRegexp  = regexp.MustCompile(`^[1-9][0-9]*(m|h|d|w|mo|y)$`)
)

// Validate checks the inventory is complete and consistent: every required
// field is set, every reference resolves, and nothing is listed twice. It
// reports every problem it finds, not just the first.
func (inv *Inventory) Validate() error {
	v := &validation{}
	if inv.SchemaVersion != SchemaVersion {
		v.addf("schema_version is %d, want %d", inv.SchemaVersion, SchemaVersion)
	}
	if !idPattern.MatchString(inv.App) {
		v.addf("app %q is not a lowercase id", inv.App)
	}

	purposes := v.ids("purposes", len(inv.Purposes), func(i int) string { return inv.Purposes[i].ID })
	for _, p := range inv.Purposes {
		at := "purpose " + p.ID
		switch p.Kind {
		case CookieCategory, Processing, Operational:
		default:
			v.addf("%s: kind %q is not cookie_category, processing or operational", at, p.Kind)
		}
		v.nonEmpty(at, "description", p.Description)
		v.nonEmpty(at, "legal_basis", p.LegalBasis)
	}

	retention := v.ids("retention_classes", len(inv.RetentionClasses), func(i int) string { return inv.RetentionClasses[i].ID })
	for _, r := range inv.RetentionClasses {
		at := "retention class " + r.ID
		v.nonEmpty(at, "description", r.Description)
		v.nonEmpty(at, "period", r.Period)
		v.nonEmpty(at, "mechanism", r.Mechanism)
		isDuration := durationRegexp.MatchString(r.Period)
		switch {
		case r.Period == "":
		case isDuration, r.Period == Indefinite, r.Period == Unknown, r.Period == TodoLegal:
		default:
			v.addf("%s: period %q is not a duration like 14d, indefinite, unknown or %s", at, r.Period, TodoLegal)
		}
		// A period that something is meant to enforce has to say what.
		if isDuration && (r.Mechanism == NoMechanism || r.Mechanism == Unknown) {
			v.addf("%s: has a period of %s but no mechanism enforcing it", at, r.Period)
		}
	}

	categories := v.ids("data_categories", len(inv.DataCategories), func(i int) string { return inv.DataCategories[i].ID })
	processors := v.ids("processors", len(inv.Processors), func(i int) string { return inv.Processors[i].ID })
	for _, c := range inv.DataCategories {
		at := "data category " + c.ID
		v.nonEmpty(at, "description", c.Description)
		v.nonEmpty(at, "legal_basis", c.LegalBasis)
		switch c.SpecialCategory {
		case NotSpecial, Health:
		default:
			v.addf("%s: special_category %q is not health", at, c.SpecialCategory)
		}
		if len(c.Purposes) == 0 {
			v.addf("%s: has no purposes", at)
		}
		v.refs(at, "purposes", c.Purposes, purposes)
		v.refs(at, "processors", c.Processors, processors)
		if len(c.Stores) == 0 {
			v.addf("%s: has no stores", at)
		}
		for _, s := range c.Stores {
			switch s.Kind {
			case StoreTable, StoreObjectStore, StoreLogs, StoreMetrics, StoreBackups, StoreBrowser, StoreWorkflow, StoreMemory:
			default:
				v.addf("%s: store kind %q is not recognised", at, s.Kind)
			}
			v.nonEmpty(at, "store name", s.Name)
		}
		v.refs(at, "retention", []string{c.Retention}, retention)
	}

	v.ids("tables", len(inv.Tables), func(i int) string { return inv.Tables[i].Name })
	for _, t := range inv.Tables {
		at := "table " + t.Name
		if !tableRegexp.MatchString(t.Name) {
			v.addf("%s: not a table name", at)
		}
		switch {
		case t.NoPersonalData && len(t.Categories) > 0:
			v.addf("%s: says it holds no personal data and lists categories", at)
		case !t.NoPersonalData && len(t.Categories) == 0:
			v.addf("%s: lists no categories and does not say no_personal_data", at)
		}
		v.refs(at, "categories", t.Categories, categories)
	}

	inv.validateTableStores(v)

	for _, p := range inv.Processors {
		at := "processor " + p.ID
		v.nonEmpty(at, "name", p.Name)
		v.nonEmpty(at, "purpose", p.Purpose)
		v.nonEmpty(at, "location", p.Location)
		v.nonEmpty(at, "transfer_mechanism", p.TransferMechanism)
		switch p.Role {
		case RoleProcessor, RoleSubProcessor, RoleController, RoleUnknown:
		default:
			v.addf("%s: role %q is not processor, sub_processor, controller or unknown", at, p.Role)
		}
		if len(p.DataShared) == 0 {
			v.addf("%s: shares no data categories", at)
		}
		v.refs(at, "data_shared", p.DataShared, categories)
		if !slices.Equal(p.SubProcessors, []string{Unknown}) {
			v.refs(at, "sub_processors", p.SubProcessors, processors)
		}
		v.configKeys(at, p.ConfigKeys)
	}

	v.ids("internal_services", len(inv.InternalServices), func(i int) string { return inv.InternalServices[i].ID })
	for _, s := range inv.InternalServices {
		v.nonEmpty("internal service "+s.ID, "name", s.Name)
		v.configKeys("internal service "+s.ID, s.ConfigKeys)
	}
	v.uniqueConfigKeys(inv)

	seenKeys := map[string]bool{}
	for _, s := range inv.Storage {
		at := fmt.Sprintf("storage %s %q", s.Type, s.Key)
		switch s.Type {
		case Cookie, LocalStorage, SessionStorage, IndexedDB:
		default:
			v.addf("%s: type is not cookie, local_storage, session_storage or indexeddb", at)
		}
		id := string(s.Type) + "/" + s.Key
		if seenKeys[id] {
			v.addf("%s: listed twice", at)
		}
		seenKeys[id] = true
		v.nonEmpty(at, "key", s.Key)
		v.nonEmpty(at, "lifetime", s.Lifetime)
		v.nonEmpty(at, "description", s.Description)
		v.nonEmpty(at, "set_by", s.SetBy)
		v.refs(at, "purpose", []string{s.Purpose}, purposes)
		if p := inv.purpose(s.Purpose); p != nil && p.Kind != CookieCategory {
			v.addf("%s: purpose %q is not a cookie_category", at, s.Purpose)
		}
	}
	return v.err()
}

func (inv *Inventory) purpose(id string) *Purpose {
	for i := range inv.Purposes {
		if inv.Purposes[i].ID == id {
			return &inv.Purposes[i]
		}
	}
	return nil
}

type validation struct{ problems []error }

func (v *validation) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Errorf(format, args...))
}

func (v *validation) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return fmt.Errorf("inventory is not valid: %w", errors.Join(v.problems...))
}

func (v *validation) nonEmpty(at, field, value string) {
	if value == "" {
		v.addf("%s: %s is empty (use %s or %s where it is not known)", at, field, Unknown, TodoLegal)
	}
}

// ids checks the ids of one list are well formed and unique, and returns
// them as a set.
func (v *validation) ids(list string, n int, id func(int) string) map[string]bool {
	set := map[string]bool{}
	for i := 0; i < n; i++ {
		name := id(i)
		if name == "" {
			v.addf("%s: entry %d has no id", list, i)
			continue
		}
		if set[name] {
			v.addf("%s: %q is listed twice", list, name)
		}
		set[name] = true
	}
	return set
}

func (v *validation) refs(at, field string, refs []string, known map[string]bool) {
	for _, r := range refs {
		if !known[r] {
			v.addf("%s: %s names %q, which is not listed", at, field, r)
		}
	}
}

func (v *validation) configKeys(at string, keys []string) {
	for _, k := range keys {
		if !configKeyRegexp.MatchString(k) {
			v.addf("%s: config key %q is not an upper-case env var name", at, k)
		}
	}
}

// uniqueConfigKeys refuses a config key claimed by two entries: the check
// that every key is accounted for would otherwise pass for the wrong one.
func (v *validation) uniqueConfigKeys(inv *Inventory) {
	owner := map[string]string{}
	claim := func(key, by string) {
		if prev, ok := owner[key]; ok {
			v.addf("config key %s is claimed by both %s and %s", key, prev, by)
			return
		}
		owner[key] = by
	}
	for _, p := range inv.Processors {
		for _, k := range p.ConfigKeys {
			claim(k, "processor "+p.ID)
		}
	}
	for _, s := range inv.InternalServices {
		for _, k := range s.ConfigKeys {
			claim(k, "internal service "+s.ID)
		}
	}
}

// validateTableStores holds a category's table stores and the tables'
// categories to each other: a table store names (comma-separated) tables
// that exist and list that category, and a table that lists a category is
// named by one of its table stores. Otherwise the generated documents could
// say data is held in a table that isn't there, or leave one out.
func (inv *Inventory) validateTableStores(v *validation) {
	tables := map[string]*Table{}
	for i := range inv.Tables {
		tables[inv.Tables[i].Name] = &inv.Tables[i]
	}
	stored := map[string]map[string]bool{} // category -> tables its stores name
	for _, c := range inv.DataCategories {
		stored[c.ID] = map[string]bool{}
		for _, s := range c.Stores {
			if s.Kind != StoreTable {
				continue
			}
			for _, name := range strings.Split(s.Name, ",") {
				name = strings.TrimSpace(name)
				t, ok := tables[name]
				switch {
				case !ok:
					v.addf("data category %s: table store names %q, which is not under tables", c.ID, name)
				case !slices.Contains(t.Categories, c.ID):
					v.addf("data category %s: table store names %q, which doesn't list this category", c.ID, name)
				}
				stored[c.ID][name] = true
			}
		}
	}
	for _, t := range inv.Tables {
		for _, c := range t.Categories {
			if s, ok := stored[c]; ok && !s[t.Name] {
				v.addf("table %s lists category %s, whose stores don't name it", t.Name, c)
			}
		}
	}
}
