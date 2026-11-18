package llmusage

import (
	"regexp"
	"strings"
)

// CommandOther is the command label for anything outside the allowlist.
const CommandOther = "other"

var commandPattern = regexp.MustCompile(`^[a-z0-9._-]{1,32}( [a-z0-9._-]{1,32})?$`)

// ValidCommand reports whether c has the shape of a bash tool's command
// field: a program name, optionally followed by one subcommand. Clients
// extract it from the command line (fixtures/bash_commands.json is the
// shared test of that extraction); receivers store the value as is but
// label metrics with CommandLabel.
func ValidCommand(c string) bool {
	return commandPattern.MatchString(c)
}

// SubcommandProgram is a program whose first argument the client reports
// as a subcommand ("git status" rather than "git").
type SubcommandProgram struct {
	// ValueOptions are the options that may come before the subcommand
	// and take a separate value, which the client skips along with them
	// ("git -C dir status" is "git status").
	ValueOptions []string `json:"value_options"`
}

// SubcommandPrograms is the client extractors' allowlist. The shared
// fixture repeats it, and a test keeps the two in step.
var SubcommandPrograms = map[string]SubcommandProgram{
	"git":     {ValueOptions: []string{"-C", "-c"}},
	"go":      {ValueOptions: []string{"-C"}},
	"gh":      {ValueOptions: []string{"-R", "--repo"}},
	"kubectl": {ValueOptions: []string{"-n", "--namespace", "--context", "--kubeconfig"}},
	"docker":  {ValueOptions: []string{"--context", "-H", "--host"}},
	"helm":    {ValueOptions: []string{"-n", "--namespace", "--kube-context"}},
	"npm":     {ValueOptions: []string{"--prefix"}},
	"uv":      {ValueOptions: []string{"--directory", "--project"}},
}

// labelSubcommands are the subcommands that keep their own label value; any
// other subcommand of the same program is labelled by the program alone.
var labelSubcommands = map[string][]string{
	"git": {"add", "blame", "branch", "checkout", "cherry-pick", "clone", "commit", "config", "diff",
		"fetch", "grep", "log", "ls-files", "merge", "merge-base", "mv", "pull", "push", "rebase", "remote",
		"reset", "restore", "rev-parse", "rm", "show", "stash", "status", "switch", "tag", "worktree"},
	"go": {"build", "doc", "env", "fmt", "generate", "get", "install", "list", "mod", "run", "test",
		"tool", "version", "vet", "work"},
	"gh": {"api", "auth", "issue", "pr", "release", "repo", "run", "workflow"},
	"kubectl": {"annotate", "apply", "config", "create", "delete", "describe", "edit", "exec",
		"explain", "get", "label", "logs", "patch", "port-forward", "rollout", "scale", "top", "version", "wait"},
	"docker": {"build", "buildx", "compose", "cp", "exec", "images", "info", "inspect", "login", "logs",
		"network", "ps", "pull", "push", "rm", "rmi", "run", "start", "stop", "system", "tag", "version", "volume"},
	"helm": {"dependency", "get", "install", "lint", "list", "package", "repo", "rollback", "show",
		"status", "template", "uninstall", "upgrade", "version"},
	"npm": {"audit", "ci", "exec", "init", "install", "ls", "outdated", "publish", "run", "test",
		"update", "version", "view"},
	"uv": {"add", "init", "lock", "pip", "python", "remove", "run", "sync", "tool", "venv"},
}

// labelPrograms are the programs that keep their own label value. It is
// the common tooling of this repo's agents; anything else is CommandOther.
var labelPrograms = []string{
	// archive, text and file tools
	"awk", "base64", "basename", "cat", "chmod", "cp", "cut", "date", "df", "diff", "dirname", "du",
	"echo", "file", "find", "grep", "gzip", "head", "jq", "ln", "ls", "mkdir", "mktemp", "mv", "printf",
	"pwd", "readlink", "realpath", "rg", "rm", "sed", "sha256sum", "sort", "stat", "tail", "tar", "tee",
	"touch", "tr", "tree", "uniq", "unzip", "wc", "which", "xargs", "yq",
	// shell builtins and control
	"bash", "cd", "export", "false", "kill", "ps", "set", "sh", "sleep", "source", "test", "true", "zsh",
	// network
	"curl", "rsync", "scp", "ssh", "wget",
	// build, test and language tooling
	"bats", "gofmt", "golangci-lint", "goose", "make", "node", "npx", "pnpm", "pyright", "pytest",
	"python", "python3", "shellcheck", "tsc", "yarn",
	// infrastructure
	"ansible", "ansible-playbook", "argocd", "k3d", "psql", "terraform", "woodpecker-cli",
	// this repo's own entrypoints
	"deploy.sh", "review.sh", "sync.sh", "test.sh",
}

var commandLabels = buildCommandLabels()

func buildCommandLabels() map[string]bool {
	labels := map[string]bool{CommandOther: true}
	for _, p := range labelPrograms {
		labels[p] = true
	}
	for p, subs := range labelSubcommands {
		labels[p] = true
		for _, s := range subs {
			labels[p+" "+s] = true
		}
	}
	return labels
}

// CommandLabel maps a command to its metric label value: an allowlisted
// program or program-and-subcommand as is, an allowlisted program with any
// other subcommand as the program alone, and anything else as
// CommandOther. Empty stays empty: a non-bash tool has no command. The
// database keeps the real value; only the label is bounded.
func CommandLabel(command string) string {
	if command == "" {
		return ""
	}
	if commandLabels[command] {
		return command
	}
	if program, _, found := strings.Cut(command, " "); found && commandLabels[program] {
		return program
	}
	return CommandOther
}
