package mergeguard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// gitValueOptions are git's global options that take a separate argument.
var gitValueOptions = []string{"-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--super-prefix", "--list-cmds"}

// git finds the subcommand past git's global options, following -C.
func (s *scan) git(args []word) {
	dir := s.dir
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			if strings.HasPrefix(literalPrefix(a.node), "-") {
				s.dirUnknown = true // --git-dir="$G", --work-tree="$W"
				continue
			}
			s.opaque("git's subcommand is computed")
			return
		}
		switch {
		case a.val == "-C":
			if i+1 >= len(args) {
				return
			}
			if args[i+1].ok {
				dir = joinDir(dir, args[i+1].val)
			} else {
				s.dirUnknown = true
			}
			i++
		case slices.Contains(gitValueOptions, a.val):
			i++
		case strings.HasPrefix(a.val, "-"):
		default:
			switch a.val {
			case "push":
				s.push(dir, args[i+1:])
			case "checkout", "switch":
				s.switched = true
			}
			return
		}
	}
}

// pushOptions are git push's options that take a separate argument.
var pushOptions = []string{"--repo", "-o", "--push-option", "--receive-pack", "--exec"}

// push classifies `git push`: it is a merge when a refspec's destination is
// a default branch, when it pushes every branch, or when the destination
// can't be told.
func (s *scan) push(dir string, args []word) {
	a := parsePush(args)
	switch {
	case a.dry:
		return
	case a.all:
		s.find(KindPushDefault, "git push --all/--mirror pushes every branch, the default one included")
		return
	}
	remote := word{val: "origin", ok: true}
	var refspecs []word
	if len(a.positional) > 0 {
		remote, refspecs = a.positional[0], a.positional[1:]
	}
	p := &pushCheck{s: s, dir: dir, remote: remote}
	if len(refspecs) > 0 {
		p.refspecs(refspecs, a.del)
		return
	}
	if a.tags || a.del {
		return // tags only; or a delete naming nothing, which git refuses
	}
	if s.unsure() {
		s.find(KindPushUnknown, "a bare git push after a checkout or a computed cd, whose branch the guard can't see: name it")
		return
	}
	if dst := p.pushTarget(); dst != "" && p.protected(dst) {
		s.find(KindPushDefault, "git push from "+dst+", which pushes to the default branch "+dst)
	}
}

// unsure reports whether the branch the hook sees may not be the one the
// command pushes from.
func (s *scan) unsure() bool { return s.switched || s.dirUnknown }

// literalPrefix is the literal text a word starts with, before anything
// computed.
func literalPrefix(w *syntax.Word) string {
	if w == nil || len(w.Parts) == 0 {
		return ""
	}
	if lit, ok := w.Parts[0].(*syntax.Lit); ok {
		return lit.Value
	}
	return ""
}

// pushArgs is a `git push` command line, taken apart.
type pushArgs struct {
	positional                   []word
	all, del, dry, tags, endOpts bool
}

func parsePush(args []word) *pushArgs {
	p := &pushArgs{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if p.endOpts || !a.ok || !strings.HasPrefix(a.val, "-") || a.val == "-" {
			p.positional = append(p.positional, a)
			continue
		}
		i += p.option(a.val)
	}
	return p
}

// option records one option and returns how many of the arguments after it
// it takes as its value.
func (p *pushArgs) option(v string) int {
	name, _, hasValue := strings.Cut(v, "=")
	switch {
	case v == "--":
		p.endOpts = true
	case name == "--all" || name == "--branches" || name == "--mirror":
		p.all = true
	case name == "--delete":
		p.del = true
	case name == "--dry-run":
		p.dry = true
	case name == "--tags":
		p.tags = true
	case slices.Contains(pushOptions, name):
		if !hasValue {
			return 1
		}
	case !strings.HasPrefix(v, "--"):
		return p.shortOptions(v[1:])
	}
	return 0
}

// shortOptions reads bundled short options, e.g. -fu, -dn, -oci.skip, and
// returns how many following arguments they take.
func (p *pushArgs) shortOptions(flags string) int {
	taken := 0
	if o := strings.IndexByte(flags, 'o'); o >= 0 {
		if o == len(flags)-1 {
			taken = 1 // -o's value is the next argument
		}
		flags = flags[:o]
	}
	p.del = p.del || strings.ContainsRune(flags, 'd')
	p.dry = p.dry || strings.ContainsRune(flags, 'n')
	return taken
}

// refspecs checks each refspec's destination.
func (p *pushCheck) refspecs(refspecs []word, del bool) {
	for i := 0; i < len(refspecs); i++ {
		r := refspecs[i]
		v := r.val
		if !r.ok {
			if !currentBranchSubst(r.node) {
				p.s.find(KindPushUnknown, "git push to "+printed(r.node)+", which could be the default branch: name the branch instead")
				return
			}
			v = "HEAD"
		}
		if v == "tag" {
			i++ // `tag <name>`
			continue
		}
		if strings.TrimPrefix(v, "+") == ":" {
			p.s.find(KindPushDefault, "git push "+v+" pushes every matching branch, the default one included")
			return
		}
		if dst := p.destination(v, del); dst != "" && p.protected(dst) {
			p.s.find(KindPushDefault, "git push to the default branch "+dst)
			return
		}
	}
}

// pushCheck resolves one push's destinations against its repo, asking git
// at most once for each fact.
type pushCheck struct {
	s      *scan
	dir    string
	remote word
	// defaults is the protected branches, once looked up.
	defaults []string
}

// destination is the branch refspec v writes to on the remote, "" for one
// that isn't a branch (a tag) or can't be resolved.
func (p *pushCheck) destination(v string, del bool) string {
	v = strings.TrimPrefix(v, "+")
	src, dst, hasColon := strings.Cut(v, ":")
	switch {
	case del:
		dst = v
	case !hasColon || dst == "":
		dst = src
	}
	if dst == "HEAD" || dst == "@" {
		if p.s.unsure() {
			return "*" // after a checkout, HEAD may be the default branch
		}
		dst = p.currentBranch()
	}
	dst = strings.TrimPrefix(dst, "heads/") // git resolves heads/main as refs/heads/main
	if strings.HasPrefix(dst, "refs/") && !strings.HasPrefix(dst, "refs/heads/") {
		return "" // a tag, a note, ...
	}
	return strings.TrimPrefix(dst, "refs/heads/")
}

// protected reports whether branch is a default branch: main, master, or
// what the remote's HEAD names. A glob destination may be any of them.
func (p *pushCheck) protected(branch string) bool {
	if strings.Contains(branch, "*") {
		return true
	}
	if p.defaults == nil {
		p.defaults = []string{"main", "master"}
		if d := p.remoteDefault(); d != "" {
			p.defaults = append(p.defaults, d)
		}
	}
	return slices.Contains(p.defaults, branch)
}

// remoteDefault is the branch refs/remotes/<remote>/HEAD points at, when
// the remote is one by name.
func (p *pushCheck) remoteDefault() string {
	if !p.remote.ok || strings.ContainsAny(p.remote.val, ":/") {
		return ""
	}
	out, err := p.git("symbolic-ref", "--quiet", "--short", "refs/remotes/"+p.remote.val+"/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(out, p.remote.val+"/")
}

// pushTarget is the branch a bare `git push` writes to: its push
// destination (@{push}, which push.default and the upstream decide), else
// the current branch's own name.
func (p *pushCheck) pushTarget() string {
	if out, err := p.git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{push}"); err == nil && out != "" {
		if _, branch, ok := strings.Cut(out, "/"); ok {
			return branch
		}
	}
	return p.currentBranch()
}

func (p *pushCheck) currentBranch() string {
	out, err := p.git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// git runs a read-only git query in the push's directory. Without an
// absolute directory there is nothing safe to ask: the daemon's own cwd
// isn't the agent's.
func (p *pushCheck) git(args ...string) (string, error) {
	if p.s.c.git == nil || !strings.HasPrefix(p.dir, "/") {
		return "", errNoRepo
	}
	out, err := p.s.c.git.Run(p.s.ctx, p.dir, args...)
	return strings.TrimSpace(out), err
}

// currentBranchQueries are the commands that print the current branch, so
// `git push origin "$(git branch --show-current)"`, a common way to push
// one's own branch, isn't refused as an unknown destination.
var currentBranchQueries = []string{
	"git branch --show-current",
	"git rev-parse --abbrev-ref HEAD",
	"git symbolic-ref --short HEAD",
	"git symbolic-ref --short -q HEAD",
	"git symbolic-ref -q --short HEAD",
}

// currentBranchSubst reports whether w is just `$(one of
// currentBranchQueries)`, quoted or not.
func currentBranchSubst(w *syntax.Word) bool {
	parts := w.Parts
	if len(parts) == 1 {
		if dq, ok := parts[0].(*syntax.DblQuoted); ok {
			parts = dq.Parts
		}
	}
	if len(parts) != 1 {
		return false
	}
	cs, ok := parts[0].(*syntax.CmdSubst)
	if !ok || len(cs.Stmts) != 1 {
		return false
	}
	call, ok := cs.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(cs.Stmts[0].Redirs) > 0 {
		return false
	}
	var argv []string
	for _, a := range words(call.Args) {
		if !a.ok {
			return false
		}
		argv = append(argv, a.val)
	}
	return slices.Contains(currentBranchQueries, strings.Join(argv, " "))
}
