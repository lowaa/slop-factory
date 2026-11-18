package inventory

import (
	"fmt"
	"strings"
)

// The generated sections an inventory can fill in. The first three are the
// names legaldocs' {{inventory:...}} directives use; data_flow is the DPIA's
// data-flow section.
const (
	FragmentCookieTable   = "cookie_table"
	FragmentSubProcessors = "sub_processors"
	FragmentRetention     = "retention"
	FragmentDataFlow      = "data_flow"
)

// Fragments renders an inventory as Markdown sections for legal documents.
// It satisfies legaldocs.Fragments.
//
// Placeholders render as plain words ("To be confirmed", "Unknown") so a
// draft document reads sensibly. Unresolved lists, per fragment, the values
// that rendered that way, which is what legaldocs checks before it accepts a
// document marked reviewed.
type Fragments struct {
	inv *Inventory
}

// NewFragments returns the generator for an inventory.
func NewFragments(inv *Inventory) *Fragments {
	return &Fragments{inv: inv}
}

// Fragment returns the Markdown for name, or false for a name it doesn't
// generate.
func (f *Fragments) Fragment(name string) (string, bool) {
	text, _, ok := f.generate(name)
	return text, ok
}

// Unresolved lists, as "<where>: <field>" in the order they render, the
// values in fragment name that appear in its text as a placeholder ("To be
// confirmed", "Unknown", "(provisional)"). It is empty for a complete
// fragment. A name the generator doesn't know is reported as unresolved,
// so a typo can't read as complete.
//
// It is derived from the same pass that renders the text, so the two
// can't drift apart: a value is reported exactly when it is shown as a
// placeholder. It is narrower than Inventory.Placeholders, which also lists
// values no fragment shows (legal bases).
func (f *Fragments) Unresolved(name string) []string {
	_, unresolved, ok := f.Checked(name)
	if !ok {
		return []string{"fragment " + name + ": not generated"}
	}
	return unresolved
}

// Checked is Fragment and Unresolved from one pass, so the text and what is
// said about it can't disagree even if the inventory is edited between two
// calls. It is what legaldocs uses.
func (f *Fragments) Checked(name string) (string, []string, bool) {
	text, p, ok := f.generate(name)
	if !ok {
		return "", nil, false
	}
	return text, p.list, true
}

func (f *Fragments) generate(name string) (string, *pending, bool) {
	p := &pending{}
	switch name {
	case FragmentCookieTable:
		return f.cookieTable(p), p, true
	case FragmentSubProcessors:
		return f.subProcessors(p), p, true
	case FragmentRetention:
		return f.retention(p), p, true
	case FragmentDataFlow:
		return f.dataFlow(p), p, true
	}
	return "", nil, false
}

// pending collects the placeholder values a fragment shows.
type pending struct {
	list []string
	seen map[string]bool
}

func (p *pending) add(where, field string) {
	key := where + ": " + field
	if p.seen[key] {
		return
	}
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	p.seen[key] = true
	p.list = append(p.list, key)
}

// cell returns v as display shows it, recording it when it is a placeholder.
func (p *pending) cell(where, field, v string) string {
	if strings.Contains(v, TodoLegal) || strings.EqualFold(strings.TrimSpace(v), Unknown) {
		p.add(where, field)
	}
	return display(v)
}

func (f *Fragments) cookieTable(p *pending) string {
	t := newTable("Name", "Type", "Purpose", "Lifetime", "First party", "What it holds")
	for _, s := range f.inv.Storage {
		where := "storage " + s.Key
		purpose := s.Purpose
		if pu := f.inv.purpose(s.Purpose); pu != nil {
			purpose = pu.Description
		}
		t.row(code(s.Key), storageTypeLabel(s.Type), p.cell(where, "purpose", purpose),
			p.cell(where, "lifetime", s.Lifetime), yesNo(s.FirstParty), p.cell(where, "description", s.Description))
	}
	return t.String()
}

func (f *Fragments) subProcessors(p *pending) string {
	t := newTable("Name", "Role", "Used for", "Data shared", "Location", "Transfer mechanism", "Engages")
	for _, pr := range f.inv.Processors {
		where := "processor " + pr.ID
		role := roleLabel(pr.Role)
		if pr.Role == RoleUnknown {
			p.add(where, "role")
		}
		t.row(p.cell(where, "name", pr.Name), role, p.cell(where, "purpose", pr.Purpose), f.categoryList(p, where, "data_shared", pr.DataShared),
			p.cell(where, "location", pr.Location), p.cell(where, "transfer_mechanism", pr.TransferMechanism),
			f.processorList(p, where, "sub_processors", pr.SubProcessors))
	}
	return t.String()
}

func (f *Fragments) retention(p *pending) string {
	t := newTable("Data", "Kept for", "Deleted by", "Applies to")
	for _, r := range f.inv.RetentionClasses {
		t.row(p.cell("retention class "+r.ID, "description", r.Description), f.retentionKept(p, &r), f.retentionMechanism(p, &r), f.categoriesWithRetention(p, r.ID))
	}
	return t.String()
}

// retentionKept is shared by retention and data_flow, which would otherwise
// disagree about a provisional period.
func (f *Fragments) retentionKept(p *pending, r *RetentionClass) string {
	where := "retention class " + r.ID
	kept := periodLabel(r.Period)
	if r.Period == TodoLegal || r.Period == Unknown {
		p.add(where, "period")
	}
	if r.Placeholder {
		p.add(where, "period (provisional)")
		kept += " (provisional)"
	}
	return kept
}

func (f *Fragments) retentionMechanism(p *pending, r *RetentionClass) string {
	if r.Mechanism == NoMechanism {
		return "Nothing deletes it automatically"
	}
	return p.cell("retention class "+r.ID, "mechanism", r.Mechanism)
}

func (f *Fragments) dataFlow(p *pending) string {
	t := newTable("Data", "Special category", "Purposes", "Stored in", "Shared with", "Retention")
	for _, c := range f.inv.DataCategories {
		where := "data category " + c.ID
		special := "No"
		if c.SpecialCategory == Health {
			special = "Yes (health)"
		}
		var stores []string
		for _, s := range c.Stores {
			stores = append(stores, fmt.Sprintf("%s: %s", storeKindLabel(s.Kind), p.cell(where, "store", s.Name)))
		}
		retention := p.cell(where, "retention", c.Retention)
		if r := f.inv.retention(c.Retention); r != nil {
			retention = f.retentionKept(p, r)
		}
		t.row(p.cell(where, "description", c.Description), special, f.categoryList(p, where, "purposes", c.Purposes), strings.Join(stores, "; "),
			f.processorList(p, where, "processors", c.Processors), retention)
	}
	return t.String()
}

// categoryList names ids (data categories or purposes) as words.
func (f *Fragments) categoryList(p *pending, where, field string, ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		p.cell(where, field, id)
		out[i] = humanize(id)
	}
	return strings.Join(out, ", ")
}

func (f *Fragments) processorList(p *pending, where, field string, ids []string) string {
	if len(ids) == 0 {
		return "None"
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = p.cell(where, field, id)
		for _, pr := range f.inv.Processors {
			if pr.ID == id {
				out[i] = p.cell("processor "+pr.ID, "name", pr.Name)
			}
		}
	}
	return strings.Join(out, ", ")
}

func (f *Fragments) categoriesWithRetention(p *pending, class string) string {
	var out []string
	for _, c := range f.inv.DataCategories {
		if c.Retention == class {
			p.cell("data category "+c.ID, "id", c.ID)
			out = append(out, humanize(c.ID))
		}
	}
	return strings.Join(out, ", ")
}

func (inv *Inventory) retention(id string) *RetentionClass {
	for i := range inv.RetentionClasses {
		if inv.RetentionClasses[i].ID == id {
			return &inv.RetentionClasses[i]
		}
	}
	return nil
}

// Placeholders lists where the inventory still holds TodoLegal, as
// "<where>: <field>", in file order. A document built from it shouldn't be
// marked reviewed while there are any.
func (inv *Inventory) Placeholders() []string {
	var out []string
	add := func(where, field, value string) {
		if value == TodoLegal {
			out = append(out, where+": "+field)
		}
	}
	for _, p := range inv.Purposes {
		add("purpose "+p.ID, "legal_basis", p.LegalBasis)
	}
	for _, c := range inv.DataCategories {
		add("data category "+c.ID, "legal_basis", c.LegalBasis)
	}
	for _, p := range inv.Processors {
		add("processor "+p.ID, "transfer_mechanism", p.TransferMechanism)
		add("processor "+p.ID, "location", p.Location)
	}
	for _, r := range inv.RetentionClasses {
		add("retention class "+r.ID, "period", r.Period)
	}
	return out
}

func display(v string) string {
	switch v {
	case TodoLegal:
		return "To be confirmed"
	case Unknown:
		return "Unknown"
	}
	return v
}

func periodLabel(p string) string {
	switch p {
	case Indefinite:
		return "Until deleted"
	case Unknown, TodoLegal:
		return display(p)
	}
	if !durationRegexp.MatchString(p) {
		return p
	}
	i := strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' })
	n, unit := p[:i], p[i:]
	names := map[string][2]string{
		"m": {"minute", "minutes"}, "h": {"hour", "hours"}, "d": {"day", "days"},
		"w": {"week", "weeks"}, "mo": {"month", "months"}, "y": {"year", "years"},
	}[unit]
	if n == "1" {
		return n + " " + names[0]
	}
	return n + " " + names[1]
}

func humanize(id string) string { return strings.NewReplacer("_", " ", "-", " ").Replace(id) }

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func code(s string) string { return "`" + s + "`" }

func storageTypeLabel(t StorageType) string {
	return map[StorageType]string{
		Cookie: "Cookie", LocalStorage: "Local storage", SessionStorage: "Session storage", IndexedDB: "IndexedDB",
	}[t]
}

func roleLabel(r Role) string {
	return map[Role]string{RoleProcessor: "Processor", RoleSubProcessor: "Sub-processor", RoleController: "Controller", RoleUnknown: "Unknown"}[r]
}

func storeKindLabel(k StoreKind) string {
	return map[StoreKind]string{
		StoreTable: "database", StoreObjectStore: "object store", StoreLogs: "logs", StoreMetrics: "metrics",
		StoreBackups: "backups", StoreBrowser: "browser", StoreWorkflow: "workflow history", StoreMemory: "memory only",
	}[k]
}

// table builds a Markdown table. Cell text comes from the inventory, a file
// people edit, and ends up in a published page, so pipes and newlines are
// neutralised rather than trusted.
type table struct {
	b strings.Builder
	n int
}

func newTable(headers ...string) *table {
	t := &table{n: len(headers)}
	t.line(headers)
	sep := make([]string, len(headers))
	for i := range sep {
		sep[i] = "---"
	}
	t.line(sep)
	return t
}

func (t *table) row(cells ...string) {
	if len(cells) != t.n {
		panic(fmt.Sprintf("inventory: table row has %d cells, want %d", len(cells), t.n))
	}
	t.line(cells)
}

func (t *table) line(cells []string) {
	t.b.WriteString("|")
	for _, c := range cells {
		c = strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ").Replace(strings.TrimSpace(c))
		if c == "" {
			c = " "
		}
		t.b.WriteString(" " + c + " |")
	}
	t.b.WriteString("\n")
}

func (t *table) String() string { return strings.TrimRight(t.b.String(), "\n") }
