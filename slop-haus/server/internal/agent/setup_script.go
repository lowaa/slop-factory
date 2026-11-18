package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// The setup script: register an agent and get back a shell script that
// turns any directory into its workspace.
//
// A machine running an agent has a checkout of some other repo, not the
// slop-haus source or a Go toolchain, so a script is what can be handed
// to it without shipping binaries.
//
// The script carries no credential. It registers the agent here, writes
// the workspace, and then runs the device login: it shows a short code,
// the user approves it on the board as this agent, and the workspace
// receives its own tokens. A leaked script is therefore just a script.

type SetupScriptRequest struct {
	Body struct {
		Name string `json:"name" doc:"Agent name, unique across the fleet"`
	}
}

type baseURLKey struct{}

// BaseURLMiddleware puts the address this request arrived on onto the
// context.
//
// A middleware rather than a header field on the input, because Go moves
// the Host header off Header and onto Request.Host -- so binding
// `header:"Host"` yields an empty string, every time, silently. huma's
// Context exposes it properly.
func BaseURLMiddleware(ctx huma.Context, next func(huma.Context)) {
	url, err := baseURL(ctx.Header("X-Forwarded-Proto"), ctx.Host())
	if err == nil {
		ctx = huma.WithValue(ctx, baseURLKey{}, url)
	}
	next(ctx)
}

// BaseURLFrom is the address the request arrived on, as BaseURLMiddleware
// recorded it.
func BaseURLFrom(ctx context.Context) (string, error) {
	url, ok := ctx.Value(baseURLKey{}).(string)
	if !ok || url == "" {
		return "", fmt.Errorf("this server cannot tell where it was reached, so it cannot tell an agent where to find it")
	}
	return url, nil
}

// shellScriptMediaType is SetupScriptResponse's Content-Type, less its
// charset.
const shellScriptMediaType = "text/x-shellscript"

// shellScriptResponses declares a SetupScriptResponse route's success as
// the shell script it is. Left to itself, huma documents a []byte body as
// base64 in application/json, which a generated client would then try to
// parse as JSON.
func shellScriptResponses(status int) map[string]*huma.Response {
	return map[string]*huma.Response{
		strconv.Itoa(status): {
			Description: http.StatusText(status),
			Content: map[string]*huma.MediaType{
				shellScriptMediaType: {Schema: &huma.Schema{Type: huma.TypeString}},
			},
		},
	}
}

// SetupScriptResponse is served as a file rather than as JSON: the human
// saves it, reads it, and runs it.
type SetupScriptResponse struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	// Per-request content (the agent it registered); nothing to share.
	CacheControl string `header:"Cache-Control"`
	Body         []byte
}

// ReposHeader carries the manifest's repos beside the update script, as a
// JSON array of {path, remote}. The supervisor reads them from here rather
// than out of the generated shell, whose quoting is free to change.
const ReposHeader = "Slop-Haus-Repos"

// UpdateScriptResponse is the update script plus the repos it syncs.
// It repeats SetupScriptResponse's fields rather than embedding it: huma
// finds Body only at the top level of the output.
type UpdateScriptResponse struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	CacheControl       string `header:"Cache-Control"`
	Repos              string `header:"Slop-Haus-Repos" doc:"JSON array of {path, remote}: the workspace manifest's repos, as the script's repo sync receives them."`
	Body               []byte
}

type repoJSON struct {
	Path   string `json:"path"`
	Remote string `json:"remote"`
}

func reposHeader(repos []workspace.Repo) (string, error) {
	out := make([]repoJSON, 0, len(repos))
	for _, r := range repos {
		out = append(out, repoJSON{Path: r.Path, Remote: r.Remote})
	}
	// ASCII-only: header values are bytes on the wire, and a client may decode them as latin-1.
	var b strings.Builder
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	for _, c := range string(raw) {
		if c < 0x80 {
			b.WriteRune(c)
		} else if c > 0xFFFF {
			c -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xD800+(c>>10), 0xDC00+(c&0x3FF))
		} else {
			fmt.Fprintf(&b, "\\u%04x", c)
		}
	}
	return b.String(), nil
}

func (h *Handler) SetupScript(ctx context.Context, input *SetupScriptRequest) (*SetupScriptResponse, error) {
	server, err := BaseURLFrom(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	inputs, err := h.briefingInputs(ctx, nil)
	if err != nil {
		return nil, err
	}
	created, err := h.svc.Create(ctx, input.Body.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.registeredScript(ctx, created, server, inputs)
}

// registeredScript is the response to registering an agent: its setup
// script, built after the agent exists.
func (h *Handler) registeredScript(ctx context.Context, created *Agent, server string, inputs *briefingInputs) (*SetupScriptResponse, error) {
	script, err := workspace.Script(workspace.Params{
		AgentName:   created.Name,
		Server:      server,
		Repos:       h.manifestRepos(ctx),
		Fleet:       inputs.fleet,
		ChangeTools: inputs.changeTools,
		Role:        roleFor(created.Kind),
	})
	if err != nil {
		// The agent exists by now, so say so rather than leaving a ghost.
		return nil, huma.Error422UnprocessableEntity(fmt.Sprintf(
			"agent %q was registered but its setup script could not be built (%v) -- disable it and try another name",
			created.Name, err))
	}
	return scriptResponse(created.Name, script), nil
}

func scriptResponse(agentName, script string) *SetupScriptResponse {
	return &SetupScriptResponse{
		ContentType:        shellScriptMediaType + "; charset=utf-8",
		ContentDisposition: fmt.Sprintf("attachment; filename=%q", scriptFilename(agentName)),
		CacheControl:       "no-store",
		Body:               []byte(script),
	}
}

type RotateSetupScriptRequest struct {
	ID string `path:"id"`
}

// RotateSetupScript hands out a new setup script for an agent that already
// exists, and signs out every workspace currently signed in as it. The
// script has no credential to rotate, so the rotation is the sign-out: the
// only workspace that can act as the agent afterwards is the one whose
// device sign-in a human approves next.
func (h *Handler) RotateSetupScript(ctx context.Context, input *RotateSetupScriptRequest) (*SetupScriptResponse, error) {
	server, err := BaseURLFrom(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}

	found, err := h.svc.GetWorker(ctx, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	inputs, err := h.briefingInputs(ctx, &id)
	if err != nil {
		return nil, err
	}
	// Rendered before anything is revoked, so a script that cannot be built
	// never leaves the agent signed out with nothing to sign back in with.
	script, err := h.setupScript(ctx, found, server, inputs)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.SignOutWorkspaces(ctx, id); err != nil {
		return nil, h.mapError(err)
	}

	return scriptResponse(found.Name, script), nil
}

// UpdateScript renders the calling agent's workspace update. The agent is
// whoever the token says it is, so one agent cannot fetch -- or learn the
// name baked into -- another's.
func (h *Handler) UpdateScript(ctx context.Context, _ *struct{}) (*UpdateScriptResponse, error) {
	server, err := BaseURLFrom(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	caller, err := authn.FromContext(ctx)
	if err != nil {
		// RequireAgent guarantees an identity; reaching here means the route
		// was mounted without the auth middleware.
		return nil, huma.Error500InternalServerError("update script served without authentication", err)
	}

	agentID := caller.AgentID()
	inputs, err := h.briefingInputs(ctx, &agentID)
	if err != nil {
		return nil, err
	}
	self, err := h.svc.Get(ctx, agentID)
	if err != nil {
		return nil, h.mapError(err)
	}
	repos := h.manifestRepos(ctx)
	script, err := workspace.UpdateScript(workspace.Params{
		AgentName:   caller.AgentName(),
		Server:      server,
		Repos:       repos,
		Fleet:       inputs.fleet,
		ChangeTools: inputs.changeTools,
		Role:        roleFor(self.Kind),
	})
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	listed, err := reposHeader(repos)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not encode the manifest's repos", err)
	}
	return &UpdateScriptResponse{
		ContentType:        shellScriptMediaType + "; charset=utf-8",
		ContentDisposition: fmt.Sprintf("attachment; filename=%q", "slop-haus-update-"+safeName(caller.AgentName())+".sh"),
		// No secret in it, but it is per-agent and changes with every
		// deploy; a cached copy would quietly apply last week's workspace.
		CacheControl: "no-store",
		Repos:        listed,
		Body:         []byte(script),
	}, nil
}

// manifestRepos is what the scripts clone. A manifest that cannot be read
// means nothing is cloned this time, not that the workspace goes without its
// hooks: the repos come with the next update.
func (h *Handler) manifestRepos(ctx context.Context) []workspace.Repo {
	if h.manifest == nil {
		return nil
	}
	repos, err := h.manifest.ScriptRepos(ctx)
	if err != nil {
		h.logger.Error("could not read the workspace manifest for a script", "err", err)
		return nil
	}
	return repos
}

// briefingInputs is what the tenant decides about the briefings.
type briefingInputs struct {
	fleet *workspace.FleetInstructions
	// changeTools is whether the tenant has a code host credential, so the
	// briefings send agents to change_open, change_checks and change_merge.
	changeTools bool
}

// briefingInputs reads them for agentID, nil for an agent not registered yet.
// A failure to read either fails the request,
// where an unreadable manifest only delays a clone: an update that carried on
// without them would quietly strip the instructions from every briefing it
// rewrites, or send agents back to the host's own CLI.
func (h *Handler) briefingInputs(ctx context.Context, agentID *uuid.UUID) (*briefingInputs, error) {
	fleet, err := h.fleetInstructions(ctx, agentID)
	if err != nil {
		return nil, err
	}
	changeTools := false
	if h.codeHost != nil {
		if changeTools, err = h.codeHost.Connected(ctx); err != nil {
			h.logger.Error("could not read the code host credential for a script", "err", err)
			return nil, huma.Error500InternalServerError("could not read the code host credential")
		}
	}
	return &briefingInputs{fleet: fleet, changeTools: changeTools}, nil
}

// fleetInstructions is the text for the agent's briefings: the version its
// variant pins -- or none, if the variant pins none -- and otherwise the
// tenant's default.
func (h *Handler) fleetInstructions(ctx context.Context, agentID *uuid.UUID) (*workspace.FleetInstructions, error) {
	if h.fleet == nil {
		return nil, nil
	}
	v, err := h.fleetVersion(ctx, agentID)
	if err != nil {
		h.logger.Error("could not read the fleet instructions for a script", "err", err)
		return nil, huma.Error500InternalServerError("could not read the fleet instructions")
	}
	if v == nil {
		return nil, nil
	}
	return &workspace.FleetInstructions{Name: v.Name, Version: v.Version, Body: v.Body}, nil
}

// stewardInstructions is the version of the steward instructions the owner
// has pinned, nil while none is.
func (h *Handler) stewardInstructions(ctx context.Context) (*workspace.FleetInstructions, error) {
	if h.fleet == nil {
		return nil, nil
	}
	v, err := h.fleet.DefaultOf(ctx, fleetinstructions.KindSteward)
	if err != nil {
		h.logger.Error("could not read the steward instructions for a briefing", "err", err)
		return nil, huma.Error500InternalServerError("could not read the steward instructions")
	}
	if v == nil {
		return nil, nil
	}
	return &workspace.FleetInstructions{Name: v.Name, Version: v.Version, Body: v.Body}, nil
}

func (h *Handler) fleetVersion(ctx context.Context, agentID *uuid.UUID) (*fleetinstructions.Version, error) {
	if agentID != nil && h.harness != nil {
		m, err := h.harness.Membership(ctx, *agentID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			if m.FleetInstructionsVersionID == nil {
				return nil, nil
			}
			return h.fleet.Get(ctx, *m.FleetInstructionsVersionID)
		}
	}
	return h.fleet.Default(ctx)
}

// baseURL reconstructs the address the request arrived on.
//
// Not a configured value, and not a guess: it is where this server was
// actually reached, which is the one address known to work from where the
// human is standing. A default like http://localhost:8190 pastes cleanly
// on a deployed UI and quietly points the agent at the operator's laptop.
func baseURL(proto, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("no Host header, so the server cannot tell an agent where to find it")
	}
	// A Host with a scheme, a path or whitespace in it is not a host. This
	// value is written into a config agents authenticate against, so it is
	// checked rather than trusted.
	if strings.ContainsAny(host, " \t/\\\"'") {
		return "", fmt.Errorf("unusable Host header %q", host)
	}

	scheme := "https"
	switch {
	case proto != "":
		// Set by the proxy in front of us; it knows whether TLS terminated
		// there.
		scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
	case strings.HasPrefix(host, "localhost:"), strings.HasPrefix(host, "127.0.0.1:"),
		host == "localhost", host == "127.0.0.1":
		// Reached directly on a loopback address, which is the dev stack.
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unusable X-Forwarded-Proto %q", proto)
	}
	return scheme + "://" + host, nil
}

// scriptFilename keeps the download recognisable without letting an agent
// name choose a path.
func scriptFilename(name string) string {
	return "slop-haus-setup-" + safeName(name) + ".sh"
}

func safeName(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	if safe == "" {
		safe = "agent"
	}
	return safe
}

// setupScript renders an existing agent's setup script.
func (h *Handler) setupScript(ctx context.Context, a *Agent, server string, inputs *briefingInputs) (string, error) {
	script, err := workspace.Script(workspace.Params{AgentName: a.Name, Server: server, Repos: h.manifestRepos(ctx), Fleet: inputs.fleet, ChangeTools: inputs.changeTools, Role: roleFor(a.Kind)})
	if err != nil {
		return "", huma.Error422UnprocessableEntity(err.Error())
	}
	return script, nil
}

// roleFor is the protocol a workspace's root AGENTS.md carries for an agent
// of this kind: only a conductor and a console are a person's session.
func roleFor(k Kind) workspace.Role {
	switch k {
	case KindConductor:
		return workspace.RoleConductor
	case KindConsole:
		return workspace.RoleConsole
	}
	return workspace.RoleHeadless
}
