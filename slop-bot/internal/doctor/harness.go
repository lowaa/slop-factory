package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agy"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// harnessCheck is one configured harness being checked.
type harnessCheck struct {
	d     *Doctor
	name  string
	group string
	bin   string
	cfg   *config.HarnessConfig
	// models is the list the sign-in check fetched, where fetching it is
	// the sign-in check (agy), so it isn't fetched twice.
	models []string
	// onlyWarn caps the checks at Warn, for a harness this build can't run.
	onlyWarn bool
}

// env is the environment the harness's agents get: the API keys only with
// the opt-in. A probe run with more could pass on a key no agent will have.
func (h *harnessCheck) env() []string {
	return agentproc.AgentEnv(&agentproc.EnvOptions{Base: h.d.opts.Environ(), APIKeyOptIn: h.cfg.APIKeyOptIn})
}

func (h *harnessCheck) run(ctx context.Context, args ...string) (*service.Result, error) {
	return h.d.opts.Commands.Run(ctx, &Command{Path: h.bin, Args: args, Env: h.env()})
}

func (h *harnessCheck) add(name string, level Level, detail, fix string) {
	if h.onlyWarn && level == Fail {
		level = Warn
	}
	h.d.add(h.group, name, level, detail, fix)
}

// probe is how each harness is checked. Each sign-in and model check is
// the cheapest one found that spends no model tokens; see each function.
type probe struct {
	binary  string
	install hint
	// signIn checks a login or a key; model checks the configured model
	// against the harness's list, where it has one.
	signIn func(ctx context.Context, h *harnessCheck)
	model  func(ctx context.Context, h *harnessCheck)
}

var probes = map[string]*probe{
	config.ClaudeCode: {
		binary: "claude",
		install: hint{linux: "curl -fsSL https://claude.ai/install.sh | bash",
			darwin: "curl -fsSL https://claude.ai/install.sh | bash (or brew install --cask claude-code)"},
		signIn: claudeSignIn,
		model:  claudeModel,
	},
	config.Codex: {
		binary: "codex",
		install: hint{linux: "npm install -g @openai/codex",
			darwin: "brew install --cask codex (or npm install -g @openai/codex)"},
		signIn: codexSignIn,
		model:  codexModel,
	},
	config.Pi: {
		binary: "pi",
		install: hint{linux: "install pi (see its own install docs) and put `pi` on PATH",
			darwin: "install pi (see its own install docs) and put `pi` on PATH"},
		signIn: piSignIn,
		model:  piModel,
	},
	config.Agy: {
		binary: "agy",
		install: hint{linux: "install the Antigravity CLI, then `agy install` to put it on PATH",
			darwin: "install the Antigravity CLI, then `agy install` to put it on PATH"},
		signIn: agySignIn,
		model:  agyModel,
	},
}

func (d *Doctor) checkHarness(ctx context.Context, name string, cfg *config.HarnessConfig) {
	p, ok := probes[name]
	if !ok {
		return // config validation has already refused an unknown harness
	}
	h := &harnessCheck{d: d, name: name, group: "harness " + name, cfg: cfg}
	if !d.implemented(name, cfg.Driver) {
		// The default harness is what blank tickets run on, so none of them
		// could start; another one only parks the tickets that name it.
		level, fix := Warn, "its tickets are marked misconfigured until a build has it; drop it from harnesses"
		if name == d.opts.Config.DefaultHarness {
			level, fix = Fail, "set default_harness to a harness this build runs (claude-code, codex, pi)"
		}
		h.add("implemented", level, fmt.Sprintf("this build of slop-bot can't run %s under the %s driver yet", name, cfg.Driver), fix)
		// Its probes still say what a build that has it would need, but
		// nothing runs it here: they mustn't fail doctor, and so install.
		h.onlyWarn = true
	}
	bin, err := d.opts.Commands.LookPath(p.binary)
	if err != nil {
		h.add("binary", Fail, p.binary+" isn't on PATH", d.forOS(p.install))
		return
	}
	h.bin = bin
	h.add("binary", Pass, bin, "")
	d.checkVersion(ctx, &versionCheck{group: h.group, row: "version", key: name, name: p.binary, path: bin, args: []string{"--version"}})
	p.signIn(ctx, h)
	p.model(ctx, h)
	if name == config.Agy {
		agyTerms(h)
	}
}

// keyOptIn reports whether agents get the named API key: opted in, and set
// in this environment.
func (h *harnessCheck) keyOptIn(envVar string) bool { return h.cfg.APIKeyOptIn && h.d.env(envVar) }

// serviceKeyFix says how to give the service a key: the unit and plist
// carry only paths and PATH, and a service manager never reads a shell
// profile.
func (h *harnessCheck) serviceKeyFix(name string) string {
	return h.d.forOS(hint{
		linux: "give the service the key too: `systemctl --user import-environment " + name + "` now, and a " +
			"~/.config/environment.d/*.conf line for later logins, then re-run slop-bot install",
		darwin: "give the service the key too: `launchctl setenv " + name + " ...`, then re-run slop-bot install",
	})
}

// claudeSignIn reads `claude auth status`, which prints the login as JSON
// from local state without a request to the model.
func claudeSignIn(ctx context.Context, h *harnessCheck) {
	res, err := h.run(ctx, "auth", "status")
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(res.Stdout), &st)
	}
	key := h.keyOptIn("ANTHROPIC_API_KEY")
	switch {
	case err != nil && !key:
		h.add("signed in", Fail, "couldn't read `claude auth status`: "+err.Error(), "claude auth login")
	case st.LoggedIn && key:
		h.add("signed in", Pass, "signed in ("+st.AuthMethod+"), but ANTHROPIC_API_KEY is passed to agents "+
			"(api_key_opt_in) and claude bills it at API rates instead", "")
	case st.LoggedIn:
		h.add("signed in", Pass, "signed in ("+st.AuthMethod+")", "")
	case key:
		h.add("signed in", Warn, "only ANTHROPIC_API_KEY (api_key_opt_in), billed at API rates, and it is in this "+
			"shell: the service gets it only if its own environment has it", h.serviceKeyFix("ANTHROPIC_API_KEY"))
	default:
		h.add("signed in", Fail, "not signed in", "claude auth login")
	}
}

// claudeAliases are the aliases claude --help documents, and "default".
var claudeAliases = []string{"default", "fable", "opus", "sonnet", "haiku", "opusplan"}

// claudeModel can only check the form: Claude Code has no model list that
// doesn't spend a request.
func claudeModel(_ context.Context, h *harnessCheck) {
	m := h.cfg.Model
	switch {
	case m == "":
		h.add("model", Pass, "Claude Code's own default (harnesses.claude-code.model is blank)", "")
	case slices.Contains(claudeAliases, m) || strings.HasPrefix(m, "claude-"):
		h.add("model", Pass, m+" (an alias or a full name: Claude Code has no model list to check without a request)", "")
	default:
		h.add("model", Warn, fmt.Sprintf("%s isn't a Claude Code alias (%s) or a claude-* name", m,
			strings.Join(claudeAliases[1:5], ", ")), "set harnesses.claude-code.model to an alias or a full model name")
	}
}

// codexSignIn runs `codex login status`, which reads the stored login
// ($CODEX_HOME/auth.json) and answers through its exit code.
func codexSignIn(ctx context.Context, h *harnessCheck) {
	res, err := h.run(ctx, "login", "status")
	switch {
	case err != nil:
		h.add("signed in", Fail, "couldn't run `codex login status`: "+err.Error(), "codex login")
	case res.ExitCode == 0:
		h.add("signed in", Pass, firstLine(res.Stdout+"\n"+res.Stderr), "")
	default:
		h.add("signed in", Fail, "not signed in: "+firstLine(res.Stdout+"\n"+res.Stderr), "codex login")
	}
}

// codexModels asks the app-server for its model list: initialize, then
// model/list. It is the list the harness's own sessions choose from, and it
// costs no tokens.
func codexModels(ctx context.Context, h *harnessCheck) (*modelList, error) {
	input := `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"slop-bot-doctor","version":"0"}}}` + "\n" +
		`{"method":"initialized"}` + "\n" +
		`{"id":2,"method":"model/list","params":{}}` + "\n"
	res, err := h.d.opts.Commands.Run(ctx, &Command{
		Path: h.bin, Args: []string{"app-server"}, Input: input, Env: h.env(),
		Done: func(out string) bool { _, done := rpcReply(out, 2); return done },
	})
	if err != nil {
		return nil, err
	}
	reply, done := rpcReply(res.Stdout, 2)
	if !done {
		return nil, fmt.Errorf("codex app-server gave no model list: %s", firstLine(res.Stderr))
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("codex app-server: %s", reply.Error.Message)
	}
	var list struct {
		Data []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(reply.Result, &list); err != nil {
		return nil, fmt.Errorf("codex app-server's model list: %w", err)
	}
	// Only the first page is read: a model missing from it with more pages
	// to come is unknown, not absent.
	out := &modelList{partial: list.NextCursor != nil && *list.NextCursor != ""}
	for _, m := range list.Data {
		out.ids = append(out.ids, m.ID)
		if m.IsDefault {
			out.def = m.ID
		}
	}
	return out, nil
}

// modelList is a harness's model list: its default where it names one,
// and whether it is only the first page.
type modelList struct {
	ids     []string
	def     string
	partial bool
}

type rpcMessage struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// rpcReply finds the reply to request id in newline-delimited JSON-RPC
// output. An error reply to any request also ends the conversation: the
// app-server answers nothing after a failed initialize.
func rpcReply(out string, id int64) (*rpcMessage, bool) {
	for _, line := range strings.Split(out, "\n") {
		var m rpcMessage
		if json.Unmarshal([]byte(line), &m) != nil || m.ID == nil {
			continue
		}
		if *m.ID == id || m.Error != nil {
			return &m, true
		}
	}
	return nil, false
}

func codexModel(ctx context.Context, h *harnessCheck) {
	list, err := codexModels(ctx, h)
	if err != nil {
		h.add("model", Warn, "couldn't list codex's models: "+err.Error(), "check `codex app-server` starts")
		return
	}
	listed(h, list)
}

// listed checks the configured model against a harness's list.
func listed(h *harnessCheck, list *modelList) {
	m, ids, def := h.cfg.Model, list.ids, list.def
	switch {
	case m == "" && def != "":
		h.add("model", Pass, fmt.Sprintf("%s's default, %s (harnesses.%s.model is blank)", h.name, def, h.name), "")
	case m == "":
		h.add("model", Pass, fmt.Sprintf("%s's own default (harnesses.%s.model is blank)", h.name, h.name), "")
	case slices.Contains(ids, m):
		h.add("model", Pass, m+" (in "+h.name+"'s model list)", "")
	case list.partial:
		h.add("model", Warn, fmt.Sprintf("%s isn't on the first page of %s's model list (%s)", m, h.name, abbreviate(ids)),
			"check the model name; doctor reads only the first page")
	default:
		h.add("model", Fail, fmt.Sprintf("%s isn't in %s's model list (%s)", m, h.name, abbreviate(ids)),
			fmt.Sprintf("set harnesses.%s.model to a listed model, or leave it blank", h.name))
	}
}

// abbreviate keeps a model list to a report line.
func abbreviate(ids []string) string {
	const keep = 5
	if len(ids) <= keep {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(ids[:keep], ", "), len(ids)-keep)
}

// piRunModel is the model pi's agents run on: the configured one, else the
// adapter's fallback, which it always passes.
func piRunModel(h *harnessCheck) string {
	if h.cfg.Model != "" {
		return h.cfg.Model
	}
	return pi.DefaultModel
}

// piSignIn runs `pi auth check` for the model agents will use. It reads
// pi's auth.json and the provider's key variables; --no-refresh keeps it
// from rotating an OAuth token a running agent holds.
func piSignIn(ctx context.Context, h *harnessCheck) {
	m := piRunModel(h)
	res, err := h.run(ctx, "auth", "check", "--model", m, "--json", "--no-refresh")
	var st struct {
		Status   string `json:"status"`
		Provider string `json:"provider"`
		AuthType string `json:"authType"`
		Reason   string `json:"reason"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(res.Stdout), &st)
	}
	fix := "run pi and sign in with /login, or set harnesses.pi.model to a model whose provider you're signed in to"
	switch {
	case err != nil:
		h.add("signed in", Fail, "couldn't read `pi auth check`: "+err.Error(), fix)
	case st.Status == "ready":
		h.add("signed in", Pass, fmt.Sprintf("ready for %s (%s, %s)", m, st.Provider, st.AuthType), "")
	case strings.Contains(st.Reason, "expired"):
		h.add("signed in", Warn, fmt.Sprintf("%s's login has expired (%s)", st.Provider, st.Reason),
			"pi refreshes it on next use; if that fails, sign in again with /login")
	default:
		h.add("signed in", Fail, fmt.Sprintf("not ready for %s: %s (%s)", m, st.Reason, st.Provider), fix)
	}
}

// piModel checks the model against `pi --list-models`, pi's local catalogue
// of provider/model pairs. A blank model warns: the adapter then runs a
// fallback, because the spike found pi's own configured default rejected
// for the account.
func piModel(ctx context.Context, h *harnessCheck) {
	res, err := h.run(ctx, "--list-models")
	if err != nil || res.ExitCode != 0 {
		h.add("model", Warn, "couldn't list pi's models: "+errText(err, res), "check `pi --list-models` runs")
		return
	}
	ids := parsePiModels(res.Stdout)
	m := piRunModel(h)
	inList := slices.Contains(ids, m)
	switch {
	case h.cfg.Model == "":
		where := "in pi's model list"
		if !inList {
			where = "not in pi's model list"
		}
		h.add("model", Warn, fmt.Sprintf("harnesses.pi.model is blank: agents run on slop-bot's fallback, %s (%s)", m, where),
			"set harnesses.pi.model to a provider/model your login serves (pi --list-models); pi's own default "+
				"was rejected for a ChatGPT login in testing")
	case inList:
		h.add("model", Pass, m+" (in pi's model list)", "")
	default:
		h.add("model", Fail, m+" isn't in pi's model list", "set harnesses.pi.model to a provider/model from `pi --list-models`")
	}
}

// parsePiModels reads pi's table (provider, model, then columns) into
// provider/model ids.
func parsePiModels(out string) []string {
	var ids []string
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 {
			continue
		}
		ids = append(ids, f[0]+"/"+f[1])
	}
	return ids
}

// agyModels runs `agy models` as an agent's session would (with an opted-in
// key, in API-key mode), which fetches the list the signed-in
// account (or API key) is served: it proves the login without a model
// request.
func agyModels(ctx context.Context, h *harnessCheck) ([]string, error) {
	res, err := h.run(ctx, agy.SubcommandArgs(h.env(), "models")...)
	if err != nil || res.ExitCode != 0 {
		return nil, errors.New(errText(err, res))
	}
	var ids []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if id, _, ok := strings.Cut(line, "\t"); ok && id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("agy models listed nothing")
	}
	return ids, nil
}

func agySignIn(ctx context.Context, h *harnessCheck) {
	ids, err := agyModels(ctx, h)
	if err != nil {
		h.add("signed in", Fail, "agy couldn't list its models (not signed in?): "+err.Error(),
			"run agy and sign in, or give it a Gemini API key (see the terms warning)")
		return
	}
	h.models = ids
	h.add("signed in", Pass, fmt.Sprintf("agy lists %d models for this login", len(ids)), "")
}

func agyModel(_ context.Context, h *harnessCheck) {
	if h.cfg.Model != "" && h.models == nil {
		h.add("model", Skip, h.cfg.Model+": no model list without a sign-in", "")
		return
	}
	listed(h, &modelList{ids: h.models})
}

// agyTerms is the consumer-terms caveat, given once: Google's terms don't
// allow driving agy from another program on a consumer sign-in. It warns
// and never refuses; the choice is the user's.
func agyTerms(h *harnessCheck) {
	if h.keyOptIn("GEMINI_API_KEY") {
		h.add("terms", Warn, "GEMINI_API_KEY (api_key_opt_in) is in this shell, billed per token within Google's terms; "+
			"without it the service's agy falls back to your Google sign-in", h.serviceKeyFix("GEMINI_API_KEY"))
		return
	}
	h.add("terms", Warn, "agy runs on your Google sign-in: Google's consumer terms don't allow driving agy "+
		"from another program, and accounts have been suspended for it",
		"use a GEMINI_API_KEY from a billed Cloud project, with harnesses.agy.api_key_opt_in: true; "+
			"slop-bot runs agy either way, the choice is yours")
}

// errText describes a probe that failed to run or exited non-zero.
func errText(err error, res *service.Result) string {
	if err != nil {
		return err.Error()
	}
	if msg := firstLine(res.Stderr); msg != "" {
		return fmt.Sprintf("exited %d: %s", res.ExitCode, msg)
	}
	return fmt.Sprintf("exited %d", res.ExitCode)
}
