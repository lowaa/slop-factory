// Command slop-agent drives slop-haus the way a real coding agent will:
// it connects to the MCP endpoint with an agent token, hands the tool
// definitions to a live model, and lets the model decide what to call.
//
// It exists because everything else is tested against fakes or a client we
// wrote ourselves. Those prove the wire format is what we *think* Claude
// Code sends. They cannot tell us whether a model, reading only the tool
// names, descriptions and schemas, will actually work out that it should
// call ticket_next before doing anything -- or what it does when a scope
// extension is denied. That is a property of the prompts and descriptions,
// and the only way to see it is to put a model in front of them.
//
// It is not a replacement for Claude Code: the Stop and SessionStart hooks
// are Claude Code's own mechanism and are exercised by the e2e suite
// hitting those endpoints directly. What this covers is the half that
// depends on a model's judgement rather than on a payload shape.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/shared"
)

const openRouterBaseURL = "https://openrouter.ai/api/v1"

type config struct {
	server   string
	token    string
	apiKey   string
	model    string
	task     string
	maxTurns int
	verbose  bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "slop-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config{}
	flag.StringVar(&cfg.server, "server", "http://localhost:8190", "slop-haus base URL")
	flag.StringVar(&cfg.token, "token", os.Getenv("SLOP_HAUS_TOKEN"), "agent token (or SLOP_HAUS_TOKEN)")
	flag.StringVar(&cfg.apiKey, "api-key", os.Getenv("OPENROUTER_API_KEY"), "OpenRouter key (or OPENROUTER_API_KEY)")
	flag.StringVar(&cfg.model, "model", "openai/gpt-5.6-luna", "model slug")
	flag.StringVar(&cfg.task, "task", "", "extra instruction appended to the briefing")
	flag.IntVar(&cfg.maxTurns, "max-turns", 20, "give up after this many model turns")
	flag.BoolVar(&cfg.verbose, "v", false, "log every tool call and result")
	flag.Parse()

	if cfg.token == "" {
		return errors.New("-token (or SLOP_HAUS_TOKEN) is required")
	}
	if cfg.apiKey == "" {
		return errors.New("-api-key (or OPENROUTER_API_KEY) is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	_, err = (&agent{cfg: cfg, mcp: session, llm: openai.NewClient(
		option.WithBaseURL(openRouterBaseURL),
		option.WithAPIKey(cfg.apiKey),
	)}).work(ctx)
	return err
}

// bearer adds the agent's token to every MCP request, which is how the
// server resolves which agent is calling.
type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func connect(ctx context.Context, cfg config) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "slop-agent", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   strings.TrimSuffix(cfg.server, "/") + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: cfg.token}, Timeout: 60 * time.Second},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.server, err)
	}
	return session, nil
}

type agent struct {
	cfg config
	mcp *mcp.ClientSession
	llm openai.Client
}

// briefing fetches the same text the SessionStart hook injects into a real
// agent's context, so this harness is reading the instructions we actually
// ship rather than a paraphrase written for the test.
func (a *agent) briefing(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"session_id": fmt.Sprintf("slop-agent-%d", time.Now().UnixNano()),
		"cwd":        "/srv/ws/slop-agent",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(a.cfg.server, "/")+"/hooks/session-start", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.token)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("session-start: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("session-start: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var out struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("session-start: %w", err)
	}
	return out.HookSpecificOutput.AdditionalContext, nil
}

// runSummary is what a test can assert on. The transcript itself is not
// assertable -- a model will not repeat itself -- but which tools it
// managed to find, and how many turns it needed, are stable enough to
// notice a regression in the tool descriptions.
type runSummary struct {
	Turns     int
	ToolCalls []string
}

// Called reports whether the model ever reached for a given tool.
func (r runSummary) Called(name string) bool {
	for _, called := range r.ToolCalls {
		if called == name {
			return true
		}
	}
	return false
}

// work runs the agent loop: give the model the tools, let it choose, feed
// results back, repeat until it stops calling tools or we run out of turns.
func (a *agent) work(ctx context.Context) (runSummary, error) {
	var summary runSummary

	tools, err := a.mcp.ListTools(ctx, nil)
	if err != nil {
		return summary, fmt.Errorf("list tools: %w", err)
	}
	fmt.Printf("connected: %d tools\n", len(tools.Tools))

	briefing, err := a.briefing(ctx)
	if err != nil {
		return summary, err
	}

	system := briefing + "\n\nYou are running headless. Work through what you " +
		"are given using the tools, and stop when there is nothing left to do."
	if a.cfg.task != "" {
		system += "\n\n" + a.cfg.task
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
		openai.UserMessage("Check in, then take whatever work is available and complete it."),
	}

	for turn := 1; turn <= a.cfg.maxTurns; turn++ {
		resp, err := a.llm.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(a.cfg.model),
			Messages: messages,
			Tools:    toOpenAITools(tools.Tools),
		})
		if err != nil {
			return summary, fmt.Errorf("turn %d: %w", turn, err)
		}
		if len(resp.Choices) == 0 {
			return summary, fmt.Errorf("turn %d: model returned no choices", turn)
		}
		summary.Turns = turn

		msg := resp.Choices[0].Message
		if text := strings.TrimSpace(msg.Content); text != "" {
			fmt.Printf("\n[%d] %s\n", turn, text)
		}
		messages = append(messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			fmt.Printf("\nagent stopped after %d turns\n", turn)
			return summary, nil
		}

		for _, call := range msg.ToolCalls {
			summary.ToolCalls = append(summary.ToolCalls, call.Function.Name)
			result := a.callTool(ctx, call.Function.Name, call.Function.Arguments)
			messages = append(messages, openai.ToolMessage(result, call.ID))
		}
	}

	// Hitting the ceiling is a result, not a crash: it usually means the
	// model is looping on a tool that keeps refusing it, which is exactly
	// the kind of thing this harness exists to surface.
	return summary, fmt.Errorf("gave up after %d turns without finishing", a.cfg.maxTurns)
}

// callTool forwards one tool call to slop-haus and returns whatever the
// model should see next.
//
// An error is reported back to the model as text rather than aborting the
// run: refusals are normal here -- a claim can be blocked, a scope
// extension denied -- and how the model reacts to being told "no" is one of
// the things worth watching.
func (a *agent) callTool(ctx context.Context, name, rawArgs string) string {
	var args map[string]any
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return fmt.Sprintf("could not parse your arguments: %v", err)
		}
	}
	fmt.Printf("  -> %s(%s)\n", name, strings.TrimSpace(rawArgs))

	result, err := a.mcp.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Printf("     !! %v\n", err)
		return fmt.Sprintf("tool %s failed: %v", name, err)
	}

	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	text := out.String()
	if a.cfg.verbose {
		fmt.Printf("     <- %s\n", strings.ReplaceAll(text, "\n", "\n        "))
	}
	return text
}

// toOpenAITools converts MCP tool definitions into the chat-completions
// shape. The schemas are passed through unchanged -- the point of this
// harness is to see what a model does with the descriptions and schemas we
// actually publish, so rewriting them here would defeat it.
func toOpenAITools(tools []*mcp.Tool) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var schema map[string]any
		if raw, err := json.Marshal(tool.InputSchema); err == nil {
			_ = json.Unmarshal(raw, &schema)
		}
		out = append(out, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        tool.Name,
			Description: openai.String(tool.Description),
			Parameters:  schema,
		}))
	}
	return out
}
