package main

import (
	"fmt"
	"go/format"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const rawType = "json.RawMessage"

// kind is how a named definition is rendered in Go.
type kind int

const (
	// kindAlias is `type X <go type>`: a string, a map, a raw value.
	kindAlias kind = iota
	// kindEnum is a string enum: a named string type and its constants.
	kindEnum
	// kindMixed is a union of strings and objects (AskForApproval is
	// "never" or {granular: ...}). Go has no sum types, so it is `any`,
	// with constants for its string values.
	kindMixed
	// kindStruct is an object, or a union of objects flattened into one
	// struct: every variant's fields, with its discriminator as a string.
	kindStruct
)

// generator renders the definitions reachable from a set of roots as Go.
type generator struct {
	pkg    string
	header string
	defs   map[string]*node
	// decls is each emitted type's source, by Go name.
	decls map[string]string
	// structs are the Go names that are struct types, so optional fields of
	// them become pointers.
	structs map[string]bool
	// idents guards against two declarations claiming one Go identifier.
	idents  map[string]string
	queue   []string
	queued  map[string]bool
	methods []method
	err     error
}

type method struct {
	ident, name, params string
}

func newGenerator(pkg, header string, defs map[string]*node) *generator {
	return &generator{
		pkg: pkg, header: header, defs: defs,
		decls: map[string]string{}, structs: map[string]bool{},
		idents: map[string]string{}, queued: map[string]bool{},
	}
}

// generate renders roots and everything they reference, plus the method
// names whose params are among them.
func (g *generator) generate(roots []string, methodGroups []methodGroup) ([]byte, error) {
	for _, r := range roots {
		if _, ok := g.defs[r]; !ok {
			return nil, fmt.Errorf("root %s is not in the schema", r)
		}
		g.use(r)
	}
	for len(g.queue) > 0 {
		name := g.queue[0]
		g.queue = g.queue[1:]
		g.declare(goName(name), g.defs[name])
	}
	for _, mg := range methodGroups {
		g.collectMethods(mg)
	}
	if g.err != nil {
		return nil, g.err
	}
	return g.render()
}

func (g *generator) claim(ident, owner string) {
	if prev, ok := g.idents[ident]; ok && prev != owner {
		if g.err == nil {
			g.err = fmt.Errorf("%s is claimed by both %s and %s", ident, prev, owner)
		}
		return
	}
	g.idents[ident] = owner
}

// use returns the Go name of a definition, queueing it for declaration.
func (g *generator) use(def string) string {
	if !g.queued[def] {
		g.queued[def] = true
		g.queue = append(g.queue, def)
		if g.kindOf(g.defs[def]) == kindStruct {
			g.structs[goName(def)] = true
		}
	}
	return goName(def)
}

func (g *generator) kindOf(n *node) kind {
	if n == nil {
		return kindAlias
	}
	if isStringEnum(n) {
		return kindEnum
	}
	vs, _ := variants(n)
	if len(vs) > 0 {
		strs := 0
		for _, v := range vs {
			if isStringEnum(v) {
				strs++
			}
		}
		switch {
		case strs == len(vs) && len(n.Properties) == 0:
			return kindEnum
		case strs > 0:
			return kindMixed
		case len(vs) == 1 && len(n.Properties) == 0:
			return g.kindOf(g.resolve(vs[0]))
		}
		return kindStruct
	}
	if len(n.Properties) > 0 {
		return kindStruct
	}
	if types, _ := n.Type.nonNull(); len(types) == 1 && types[0] == "object" && n.AdditionalProperties == nil {
		return kindStruct
	}
	return kindAlias
}

// resolve follows a bare $ref to its definition.
func (g *generator) resolve(n *node) *node {
	if n != nil && n.Ref != "" {
		if d, ok := g.defs[refName(n.Ref)]; ok {
			return d
		}
	}
	return n
}

func isStringEnum(n *node) bool {
	types, _ := n.Type.nonNull()
	return len(n.Enum) > 0 && len(types) == 1 && types[0] == "string"
}

// variants are a union's non-null alternatives, and whether null was one.
func variants(n *node) ([]*node, bool) {
	var out []*node
	nullable := false
	for _, v := range append(append([]*node(nil), n.OneOf...), n.AnyOf...) {
		if types, isNull := v.Type.nonNull(); isNull && len(types) == 0 && v.Ref == "" {
			nullable = true
			continue
		}
		out = append(out, v)
	}
	return out, nullable
}

func (g *generator) declare(name string, n *node) {
	if _, done := g.decls[name]; done {
		return
	}
	g.decls[name] = "" // reserve: a recursive reference must not redeclare it
	g.claim(name, name)
	var b strings.Builder
	writeDoc(&b, "", n.Description)
	switch g.kindOf(n) {
	case kindEnum:
		g.writeEnum(&b, name, n)
	case kindMixed:
		g.writeMixed(&b, name, n)
	case kindStruct:
		g.structs[name] = true
		g.writeStruct(&b, name, n)
	default:
		t, _ := g.goType(n, name+"Value")
		fmt.Fprintf(&b, "type %s %s\n", name, t)
	}
	g.decls[name] = b.String()
}

func enumValues(n *node) []string {
	var out []string
	for _, e := range n.Enum {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	vs, _ := variants(n)
	for _, v := range vs {
		if isStringEnum(v) {
			out = append(out, enumValues(v)...)
		}
	}
	return out
}

func (g *generator) writeEnum(b *strings.Builder, name string, n *node) {
	fmt.Fprintf(b, "type %s string\n\nconst (\n", name)
	for _, v := range enumValues(n) {
		ident := name + exportName(v)
		g.claim(ident, name)
		fmt.Fprintf(b, "\t%s %s = %s\n", ident, name, strconv.Quote(v))
	}
	b.WriteString(")\n")
}

func (g *generator) writeMixed(b *strings.Builder, name string, n *node) {
	fmt.Fprintf(b, "type %s = any\n\n// %s's string values.\nconst (\n", name, name)
	for _, v := range enumValues(n) {
		ident := name + exportName(v)
		g.claim(ident, name)
		fmt.Fprintf(b, "\t%s = %s\n", ident, strconv.Quote(v))
	}
	b.WriteString(")\n")
}

// field is one JSON property of a struct, gathered across a union's
// variants.
type field struct {
	json  string
	nodes []*node
	// in counts the variants that have it, req those that require it.
	in, req int
}

// chains are a node's union leaves, each the list of nested schemas whose
// properties make up that variant. A plain object is one chain of itself.
func chains(n *node) [][]*node {
	vs, _ := variants(n)
	if len(vs) == 0 {
		return [][]*node{{n}}
	}
	var out [][]*node
	for _, v := range vs {
		for _, c := range chains(v) {
			out = append(out, append([]*node{n}, c...))
		}
	}
	return out
}

func (g *generator) fields(n *node) ([]*field, int) {
	byName := map[string]*field{}
	cs := chains(n)
	for _, chain := range cs {
		seen := map[string]bool{}
		required := map[string]bool{}
		for _, layer := range chain {
			layer = g.resolve(layer)
			for _, r := range layer.Required {
				required[r] = true
			}
			for name, p := range layer.Properties {
				f := byName[name]
				if f == nil {
					f = &field{json: name}
					byName[name] = f
				}
				f.nodes = append(f.nodes, p)
				seen[name] = true
			}
		}
		for name := range seen {
			byName[name].in++
			if required[name] {
				byName[name].req++
			}
		}
	}
	out := make([]*field, 0, len(byName))
	for _, f := range byName {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].json < out[j].json })
	return out, len(cs)
}

func (g *generator) writeStruct(b *strings.Builder, name string, n *node) {
	fs, nchains := g.fields(n)
	if len(fs) == 0 {
		fmt.Fprintf(b, "type %s struct{}\n", name)
		return
	}
	fmt.Fprintf(b, "type %s struct {\n", name)
	used := map[string]bool{}
	for _, f := range fs {
		ident := exportName(f.json)
		for used[ident] {
			ident += "_"
		}
		used[ident] = true
		t, nullable := g.fieldType(name, ident, f)
		optional := nullable || f.req < nchains
		if optional && g.structs[t] {
			t = "*" + t
		}
		tag := f.json
		if optional {
			tag += ",omitempty"
		}
		writeDoc(b, "\t", f.nodes[0].Description)
		fmt.Fprintf(b, "\t%s %s `json:%s`\n", ident, t, strconv.Quote(tag))
	}
	b.WriteString("}\n")
	g.writeDiscriminators(b, name, n)
}

// fieldType is one Go type for a property that may differ between
// variants: their common type, a string when each is some string, and the
// raw JSON otherwise.
func (g *generator) fieldType(owner, ident string, f *field) (string, bool) {
	nullable := false
	sigs := map[string]bool{}
	for _, p := range f.nodes {
		sig, isNull := g.signature(p)
		sigs[sig] = true
		nullable = nullable || isNull
	}
	if len(f.nodes) == 1 || (len(sigs) == 1 && !sigs["inline"]) {
		t, isNull := g.goType(f.nodes[0], owner+ident)
		return t, nullable || isNull
	}
	for s := range sigs {
		if s != "string" {
			return rawType, nullable
		}
	}
	return "string", nullable
}

// signature names a schema's shape without declaring anything, so a
// union's variants can be compared.
func (g *generator) signature(n *node) (string, bool) {
	switch {
	case n.boolean != nil:
		return "raw", false
	case n.Ref != "":
		def := g.defs[refName(n.Ref)]
		if def != nil && g.kindOf(def) == kindEnum {
			return "string", false
		}
		return "ref:" + refName(n.Ref), false
	case len(n.AllOf) == 1:
		return g.signature(n.AllOf[0])
	}
	if vs, nullable := variants(n); len(vs) > 0 {
		if len(vs) == 1 && len(n.Properties) == 0 {
			s, _ := g.signature(vs[0])
			return s, nullable
		}
		return "inline", nullable
	}
	types, nullable := n.Type.nonNull()
	if len(types) != 1 {
		return "raw", nullable
	}
	switch types[0] {
	case "array":
		s, _ := g.signature(n.Items)
		return "[]" + s, nullable
	case "object":
		if len(n.Properties) > 0 {
			return "inline", nullable
		}
		return "map", nullable
	}
	return types[0], nullable
}

// writeDiscriminators gives a union's `type` values constants, when each
// variant names one.
func (g *generator) writeDiscriminators(b *strings.Builder, name string, n *node) {
	if vs, _ := variants(n); len(vs) == 0 {
		return
	}
	var values []string
	for _, chain := range chains(n) {
		v := ""
		for _, layer := range chain {
			if t, ok := g.resolve(layer).Properties["type"]; ok && len(t.Enum) == 1 {
				v, _ = t.Enum[0].(string)
			}
		}
		if v == "" {
			return
		}
		// A variant with nested alternatives (an image by URL or by file
		// id) is several chains with one discriminator.
		if !slices.Contains(values, v) {
			values = append(values, v)
		}
	}
	fmt.Fprintf(b, "\n// %s's `type` values.\nconst (\n", name)
	for _, v := range values {
		ident := name + "Type" + exportName(v)
		g.claim(ident, name)
		fmt.Fprintf(b, "\t%s = %s\n", ident, strconv.Quote(v))
	}
	b.WriteString(")\n")
}

// goType renders a schema as a Go type expression, declaring any inline
// object under hint. It reports whether the schema allows null.
func (g *generator) goType(n *node, hint string) (string, bool) {
	switch {
	case n == nil || n.boolean != nil:
		return rawType, false
	case n.Ref != "":
		return g.use(refName(n.Ref)), false
	case len(n.AllOf) == 1:
		return g.goType(n.AllOf[0], hint)
	}
	if vs, nullable := variants(n); len(vs) > 0 {
		if len(vs) == 1 && len(n.Properties) == 0 {
			t, _ := g.goType(vs[0], hint)
			return t, nullable
		}
		return g.inline(hint, n), nullable
	}
	types, nullable := n.Type.nonNull()
	if len(types) != 1 {
		return rawType, nullable
	}
	return g.scalar(types[0], n, hint), nullable
}

func (g *generator) scalar(t string, n *node, hint string) string {
	switch t {
	case "string":
		return "string"
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		item, _ := g.goType(n.Items, hint+"Item")
		return "[]" + item
	case "object":
		if len(n.Properties) > 0 {
			return g.inline(hint, n)
		}
		if ap := n.AdditionalProperties; ap != nil && ap.boolean == nil {
			v, _ := g.goType(ap, hint+"Value")
			return "map[string]" + v
		}
		return "map[string]any"
	}
	return rawType
}

func (g *generator) inline(hint string, n *node) string {
	name := hint
	for i := 2; ; i++ {
		_, declared := g.decls[name]
		_, isDef := g.defs[name]
		if !declared && !isDef {
			break
		}
		name = hint + strconv.Itoa(i)
	}
	if g.kindOf(n) == kindStruct {
		g.structs[name] = true
	}
	g.declare(name, n)
	return name
}

// methodGroup is one of the schema's method unions (ClientRequest,
// ServerNotification, ...). all says to name every method in it, not just
// those whose params were generated.
type methodGroup struct {
	def string
	all bool
}

func (g *generator) collectMethods(mg methodGroup) {
	d := g.defs[mg.def]
	if d == nil {
		g.err = fmt.Errorf("method group %s is not in the schema", mg.def)
		return
	}
	for _, v := range d.OneOf {
		m := v.Properties["method"]
		if m == nil || len(m.Enum) != 1 {
			continue
		}
		name, _ := m.Enum[0].(string)
		params := ""
		if p := v.Properties["params"]; p != nil && p.Ref != "" {
			params = goName(refName(p.Ref))
		}
		if _, generated := g.decls[params]; !mg.all && !generated {
			continue
		}
		ident := "Method" + exportName(name)
		g.claim(ident, mg.def)
		g.methods = append(g.methods, method{ident: ident, name: name, params: params})
	}
}

func (g *generator) render() ([]byte, error) {
	var b strings.Builder
	sort.Slice(g.methods, func(i, j int) bool { return g.methods[i].ident < g.methods[j].ident })
	b.WriteString("// The protocol's method names.\nconst (\n")
	for _, m := range g.methods {
		if m.params != "" {
			fmt.Fprintf(&b, "\t// %s takes %s.\n", m.ident, m.params)
		}
		fmt.Fprintf(&b, "\t%s = %s\n", m.ident, strconv.Quote(m.name))
	}
	b.WriteString(")\n")
	names := make([]string, 0, len(g.decls))
	for n := range g.decls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("\n" + g.decls[n])
	}
	body := b.String()
	head := g.header + "\npackage " + g.pkg + "\n\n"
	if strings.Contains(body, rawType) {
		head += "import \"encoding/json\"\n\n"
	}
	out, err := format.Source([]byte(head + body))
	if err != nil {
		return nil, fmt.Errorf("formatting the generated source: %w", err)
	}
	return out, nil
}

func writeDoc(b *strings.Builder, indent, desc string) {
	line := strings.TrimSpace(desc)
	if i := strings.Index(line, "\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if line == "" {
		return
	}
	fmt.Fprintf(b, "%s// %s\n", indent, line)
}

// initialisms are the words Go spells in capitals (golint's list).
var initialisms = map[string]bool{
	"ACL": true, "API": true, "ASCII": true, "CPU": true, "CSS": true, "DNS": true,
	"EOF": true, "GUID": true, "HTML": true, "HTTP": true, "HTTPS": true, "ID": true,
	"IP": true, "JSON": true, "QPS": true, "RAM": true, "RPC": true, "SLA": true,
	"SMTP": true, "SQL": true, "SSH": true, "TCP": true, "TLS": true, "TTL": true,
	"UDP": true, "UI": true, "UID": true, "UUID": true, "URI": true, "URL": true,
	"UTF8": true, "VM": true, "XML": true, "XSRF": true, "XSS": true,
}

// goName is a definition's Go name.
func goName(def string) string { return exportName(def) }

// exportName turns a JSON or schema name (threadId, text_elements,
// danger-full-access, item/agentMessage/delta) into an exported Go
// identifier.
func exportName(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		if up := strings.ToUpper(w); initialisms[up] {
			b.WriteString(up)
			continue
		}
		// threadIds is ThreadIDs.
		if stem, ok := strings.CutSuffix(w, "s"); ok && initialisms[strings.ToUpper(stem)] {
			b.WriteString(strings.ToUpper(stem) + "s")
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	out := b.String()
	if out == "" {
		return "Empty"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		return "V" + out
	}
	return out
}

// words splits on punctuation and at lower-to-upper case changes.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && len(cur) > 0 && !unicode.IsUpper(cur[len(cur)-1]):
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}
