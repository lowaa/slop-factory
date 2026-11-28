// Package setup writes the files `slop-bot install` gives a new user: a
// commented default config and a stub briefing. It never overwrites one,
// so a re-run keeps the user's edits.
package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"text/template"

	"go.yaml.in/yaml/v3"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
)

// ConfigOptions are what the default config is written with.
type ConfigOptions struct {
	// Harnesses are the harnesses to enable, the first being the default.
	// Empty means claude-code.
	Harnesses []string
	// Repos come from install's --repo flags; none leaves an example.
	Repos []config.Repo
	// BriefingPath is the stub install writes alongside.
	BriefingPath string
	// Home is the user's home, for "~" in the written paths.
	Home string
}

var configTemplate = template.Must(template.New("config").Funcs(template.FuncMap{"yaml": yamlScalar}).Parse(
	`# slop-bot's config, written by ` + "`slop-bot install`" + `. Edit it freely:
# install never overwrites it. Run ` + "`slop-bot doctor`" + ` after a change.

# Where ticket worktrees are created.
workspace_dir: ~/slop-bot/workspace

# Your own briefing for every agent: how your setup works. Empty means none.
briefing_path: {{yaml .BriefingPath}}

# Who tickets are assigned to; blank is your OS user name.
# user: ada

# The harness for tickets that leave it blank.
default_harness: {{yaml .Default}}

# The harnesses this machine runs. Per harness: model (the model a ticket
# with none runs on; blank is the harness's own default), api_key_opt_in
# (let the harness's API key through from the environment; off, because
# claude bills a key at API rates over your login), and permissive.
harnesses:
{{- range .Harnesses}}
  {{.}}: {}
{{- end}}
{{- range .Others}}
  # {{.}}: {}
{{- end}}

# The repos agents make worktrees from: a name tickets use, and its
# checkout on this machine.
{{- if .Repos}}
repos:
{{- range .Repos}}
  - name: {{yaml .Name}}
    path: {{yaml .Path}}
{{- end}}
{{- else}}
repos: []
#  - name: my-project
#    path: ~/src/my-project
{{- end}}

# concurrency: {max_active: 4, urgent: {max_active: 1}}
# autonomy: {enabled: false, tickets_per_agent: 10}

# Binaries your workflow needs, for ` + "`slop-bot doctor`" + ` to check, e.g. [gh].
doctor:
  require: []
`))

// allHarnesses is the order the template lists harnesses in.
var allHarnesses = []string{config.ClaudeCode, config.Codex, config.Pi, config.Agy}

// DefaultConfig renders the commented default config, and checks it parses.
func DefaultConfig(opts *ConfigOptions) ([]byte, error) {
	harnesses := opts.Harnesses
	if len(harnesses) == 0 {
		harnesses = []string{config.ClaudeCode}
	}
	var others []string
	for _, h := range allHarnesses {
		if !slices.Contains(harnesses, h) {
			others = append(others, h)
		}
	}
	repos := make([]config.Repo, len(opts.Repos))
	for i, r := range opts.Repos {
		repos[i] = config.Repo{Name: r.Name, Path: tilde(r.Path, opts.Home)}
	}
	var b bytes.Buffer
	err := configTemplate.Execute(&b, map[string]any{
		"BriefingPath": tilde(opts.BriefingPath, opts.Home), "Default": harnesses[0],
		"Harnesses": harnesses, "Others": others, "Repos": repos,
	})
	if err != nil {
		return nil, err
	}
	// What install writes must load: a template slip would otherwise leave
	// the user a config the daemon refuses.
	if _, err := config.Parse(b.Bytes(), filepath.Dir(opts.BriefingPath), opts.Home); err != nil {
		return nil, fmt.Errorf("setup: the default config doesn't load: %w", err)
	}
	return b.Bytes(), nil
}

// yamlScalar quotes a value only when YAML needs it.
func yamlScalar(v string) (string, error) {
	out, err := yaml.Marshal(v)
	return string(bytes.TrimSuffix(out, []byte("\n"))), err
}

// tilde writes a path under home as ~/..., so the config reads the same
// to the user and survives a moved home.
func tilde(path, home string) string {
	if home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != "." && filepath.IsLocal(rel) {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

// BriefingStub is the briefing install writes: prompts for the user to
// answer, all commented, so an untouched stub tells agents nothing wrong.
const BriefingStub = `<!--
Your briefing: every agent slop-bot starts reads this after slop-bot's own.
Describe how your setup works, so agents follow your workflow rather than
guess it. Agents get the version on disk when they start, so an edit applies
to the next agent without a restart. Some prompts to answer:

- How is work reviewed? (open a PR; who or what reviews it; what "done" is)
- What must run before pushing? (tests, linters, a security scan)
- Branch and commit conventions?
- Which tools may agents use, and which need asking first?
- Anything agents must never do? (push to main, touch production, ...)
- Where are the docs an agent should read first?

Delete this comment once you've written your own.
-->
`

// WriteNew writes data to path only if nothing is there, creating its
// directory. It reports whether it wrote: false means the user's own file
// was kept.
func WriteNew(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// O_EXCL: a file that appears between a check and the write is kept.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the user's own config dir
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}
