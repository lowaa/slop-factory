// Package mergeguard is slop-bot's merge-flag guard: defence in depth
// behind the briefing's instruction that an agent on a ticket without
// AutoMerge stops at ready-to-merge. A harness's pre-tool hook runs
// `slop-bot hook pre-tool` before each shell command; the daemon classifies
// the command and refuses one that merges (or pushes to the default branch)
// until the user has authorised it.
//
// Detection is advisory, like the agent token it rests on: an agent with a
// shell can always reach a merge the classifier can't see.
// slop-bot/docs/merge_guard.md lists the known ways round it, and which
// harnesses enforce the guard.
package mergeguard

import (
	"bytes"
	"context"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Kind is what sort of merge a tool call was found to be.
type Kind string

// Kinds of merge.
const (
	// KindPRMerge is a forge CLI's merge: `gh pr merge`, `glab mr merge`.
	KindPRMerge Kind = "pr_merge"
	// KindAPIMerge is a merge through a forge's API: `gh api`, or curl.
	KindAPIMerge Kind = "api_merge"
	// KindPushDefault is a push that lands on a default branch.
	KindPushDefault Kind = "push_default"
	// KindPushUnknown is a push whose destination can't be told from the
	// command (a variable, say): it may be the default branch.
	KindPushUnknown Kind = "push_unknown"
	// KindTool is a non-shell tool named as a merge (an MCP server's).
	KindTool Kind = "merge_tool"
	// KindOpaque is a command the classifier can't take apart (a parse
	// failure, a script fed to a shell's stdin) that mentions a merge or a
	// push.
	KindOpaque Kind = "opaque"
)

// Finding is a tool call classified as a merge.
type Finding struct {
	Kind Kind `json:"kind"`
	// Detail says what was found, for the agent and the log.
	Detail string `json:"detail"`
}

// Call is one tool call as a harness's pre-tool hook reports it.
type Call struct {
	// Tool is the harness's tool name: Claude Code's "Bash", pi's "bash".
	Tool string
	// Command is the shell command, for a shell tool.
	Command string
	// Cwd is where the command runs, for resolving the current branch.
	Cwd string
}

// Git runs git; *gitx.Git is one.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// Classifier tells merges from other tool calls.
type Classifier struct {
	git Git
}

// NewClassifier returns a classifier that asks git, in the command's
// directory, for the current branch and the remote's default branch.
func NewClassifier(git Git) *Classifier { return &Classifier{git: git} }

// shellTools are the tool names harnesses give their shell.
var shellTools = []string{"bash", "shell", "exec_command", "local_shell"}

// mergeTool matches an MCP tool that merges: GitHub's MCP server's
// merge_pull_request, say. Its arguments aren't looked at.
var mergeTool = regexp.MustCompile(`(?i)^mcp__.*merge`)

// Classify returns the merge call is, or nil when it isn't one.
func (c *Classifier) Classify(ctx context.Context, call *Call) *Finding {
	switch {
	case slices.Contains(shellTools, strings.ToLower(call.Tool)):
		return c.Command(ctx, call.Cwd, call.Command)
	case mergeTool.MatchString(call.Tool):
		return &Finding{Kind: KindTool, Detail: "the tool " + call.Tool}
	}
	return nil
}

// Command classifies a shell command run in cwd.
func (c *Classifier) Command(ctx context.Context, cwd, command string) *Finding {
	s := &scan{c: c, ctx: ctx, dir: cwd, src: command}
	s.script(command, 0)
	return s.found
}

// maxDepth bounds `bash -c "bash -c ..."` and eval nesting.
const maxDepth = 4

// mentions is the fallback test for a command that can't be taken apart:
// anything that names a merge or a push is treated as one. It is crude on
// purpose; only the paths where the parse gives up reach it.
var mentions = regexp.MustCompile(`(?i)\b(merge|push)`)

// scan is one classification: it walks the parsed command in source order,
// tracking `cd`, and stops at the first merge.
type scan struct {
	c     *Classifier
	ctx   context.Context
	dir   string
	src   string
	found *Finding
}

func (s *scan) find(kind Kind, detail string) {
	if s.found == nil {
		s.found = &Finding{Kind: kind, Detail: detail}
	}
}

// opaque falls back to mentions for text the scan can't take apart.
func (s *scan) opaque(text, why string) {
	if mentions.MatchString(text) {
		s.find(KindOpaque, why+", and it mentions a merge or a push")
	}
}

func (s *scan) script(src string, depth int) {
	if depth > maxDepth {
		s.opaque(src, "the command nests shells too deeply to follow")
		return
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		s.opaque(src, "the command doesn't parse")
		return
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		if s.found != nil {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			s.call(call, depth)
		}
		return s.found == nil
	})
}

// word is one argument: its value when it is a literal the scan can read,
// and the node for the fallbacks when it isn't.
type word struct {
	val  string
	ok   bool
	node *syntax.Word
}

func words(ws []*syntax.Word) []word {
	out := make([]word, 0, len(ws))
	for _, w := range ws {
		v, ok := literal(w)
		out = append(out, word{val: v, ok: ok, node: w})
	}
	return out
}

// literal is w's value when nothing in it expands at run time: quotes and
// escapes are resolved (so `pu\sh` and `"push"` are push), but a variable or
// a command substitution makes it unknown.
func literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, ""))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false // $'...' has escapes of its own; no branch is written so
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unescape(lit.Value, "$`\\\""))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// unescape resolves backslashes as the shell does: outside quotes (only
// "") one escapes any character; inside double quotes only those in only.
// A backslash-newline is a line continuation either way.
func unescape(s, only string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch {
			case next == '\n':
				i++
				continue
			case only == "" || strings.IndexByte(only, next) >= 0:
				b.WriteByte(next)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func printed(n syntax.Node) string {
	var b bytes.Buffer
	_ = syntax.NewPrinter().Print(&b, n)
	return b.String()
}

func (s *scan) call(call *syntax.CallExpr, depth int) {
	argv := unwrap(words(call.Args))
	if len(argv) == 0 {
		return
	}
	if !argv[0].ok {
		s.opaque(printed(call), "the command it runs is computed")
		return
	}
	args := argv[1:]
	if strings.ContainsAny(argv[0].val, " \t\n") {
		// A command line in one word: `env -S "gh pr merge"`.
		s.script(argv[0].val, depth+1)
		return
	}
	switch name := path.Base(argv[0].val); name {
	case "cd", "pushd":
		s.cd(args)
	case "git":
		s.git(args)
	case "gh":
		s.gh(args)
	case "glab":
		if len(args) >= 2 && args[0].val == "mr" && args[1].val == "merge" {
			s.find(KindPRMerge, "glab mr merge")
		}
	case "curl", "wget", "http", "https", "xh":
		s.httpClient(name, args)
	case "eval":
		var parts []string
		for _, a := range args {
			if !a.ok {
				s.opaque(printed(call), "eval runs a computed command")
				return
			}
			parts = append(parts, a.val)
		}
		s.script(strings.Join(parts, " "), depth+1)
	default:
		if isShell(name) {
			s.shell(args, depth)
		}
	}
}

func (s *scan) cd(args []word) {
	for _, a := range args {
		if !a.ok {
			return
		}
		if strings.HasPrefix(a.val, "-") && a.val != "-" {
			continue
		}
		s.dir = joinDir(s.dir, a.val)
		return
	}
}

func joinDir(dir, to string) string {
	if filepath.IsAbs(to) || dir == "" {
		return to
	}
	return filepath.Join(dir, to)
}

var shells = []string{"bash", "sh", "zsh", "dash", "ksh", "mksh", "ash", "fish"}

func isShell(name string) bool { return slices.Contains(shells, name) }

// shell follows `bash -c "<script>"` into the script. A shell reading its
// script from stdin (`echo ... | bash`, a heredoc) can't be followed, so the
// whole command falls back to mentions. A script file isn't read: a known
// bypass.
func (s *scan) shell(args []word, depth int) {
	cflag := false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			break
		}
		if a.val == "--" || a.val == "-" {
			i++
			break
		}
		if strings.HasPrefix(a.val, "--") {
			continue
		}
		if len(a.val) > 1 && (a.val[0] == '-' || a.val[0] == '+') {
			cflag = cflag || strings.ContainsRune(a.val, 'c')
			if strings.ContainsAny(a.val, "oO") {
				i++ // -o pipefail
			}
			continue
		}
		break
	}
	switch {
	case cflag && i < len(args):
		if !args[i].ok {
			s.opaque(printed(args[i].node), "the script given to the shell is computed")
			return
		}
		s.script(args[i].val, depth+1)
	case i >= len(args):
		s.opaque(s.src, "it feeds a script to a shell's stdin")
	}
}

// wrapper is a command that runs its arguments as another command: values
// are its options that take a separate argument, and positionals how many
// arguments come before the command (timeout's duration).
type wrapper struct {
	values      []string
	positionals int
	// assignments skips NAME=value words (env).
	assignments bool
}

var wrappers = map[string]wrapper{
	"env":      {values: []string{"-u", "--unset", "-C", "--chdir"}, assignments: true},
	"sudo":     {values: []string{"-u", "--user", "-g", "--group", "-C", "-D", "--chdir", "-h", "--host", "-p", "--prompt", "-r", "--role", "-t", "--type", "-U", "--other-user", "-T", "--command-timeout"}},
	"doas":     {values: []string{"-u", "-C"}},
	"command":  {},
	"builtin":  {},
	"exec":     {values: []string{"-a"}},
	"nohup":    {},
	"setsid":   {},
	"time":     {values: []string{"-f", "--format", "-o", "--output"}},
	"nice":     {values: []string{"-n", "--adjustment"}},
	"ionice":   {values: []string{"-c", "--class", "-n", "--classdata", "-p", "--pid"}},
	"timeout":  {values: []string{"-s", "--signal", "-k", "--kill-after"}, positionals: 1},
	"stdbuf":   {values: []string{"-i", "-o", "-e"}},
	"chronic":  {},
	"unbuffer": {},
	"xargs": {values: []string{"-I", "-i", "-n", "--max-args", "-P", "--max-procs", "-d", "--delimiter", "-a", "--arg-file",
		"-L", "--max-lines", "-s", "--max-chars", "-E", "-e", "--eof", "--replace"}},
}

// unwrap strips wrappers (`env FOO=1 sudo -u me git push`) down to the
// command they run.
func unwrap(argv []word) []word {
	for len(argv) > 0 && argv[0].ok {
		name := path.Base(argv[0].val)
		w, ok := wrappers[name]
		if !ok {
			return argv
		}
		if name == "command" && len(argv) > 1 && (argv[1].val == "-v" || argv[1].val == "-V") {
			return nil // it looks the command up; it doesn't run it
		}
		i := w.skip(argv) + w.positionals
		if i > len(argv) {
			return nil
		}
		argv = argv[i:]
	}
	return argv
}

// skip is the index in argv (the wrapper first) past the wrapper's own
// options and assignments.
func (w *wrapper) skip(argv []word) int {
	i := 1
	for i < len(argv) && argv[i].ok {
		a := argv[i].val
		switch {
		case a == "--":
			return i + 1
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if slices.Contains(w.values, a) {
				i++
			}
		case !w.assignments || !strings.Contains(a, "="):
			return i
		}
		i++
	}
	return i
}
