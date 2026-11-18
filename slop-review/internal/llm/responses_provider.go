package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Errors a ResponsesLLMProvider fails a turn with, for callers (and tests)
// to tell apart with errors.Is. The messages they wrap say what to do next.
var (
	// ErrUsageLimit: the subscription's usage limit is reached. Never
	// retried -- only time (or a plan change) fixes it.
	ErrUsageLimit = errors.New("ChatGPT usage limit reached")
	// ErrAuth: no usable token, or the backend rejected it even after a
	// refresh.
	ErrAuth = errors.New("ChatGPT authentication failed")
	// ErrBackend: anything else the backend or the connection to it did
	// wrong, after any retries.
	ErrBackend = errors.New("ChatGPT request failed")
)

// switchHint names the ways to OpenRouter. The only automatic fallback is
// one a person configured in advance on the report server: anything else
// would spend money and change which model reviewed the change without
// anyone choosing that.
const switchHint = "to review through OpenRouter instead, a person switches the provider (or sets up a fallback) on the report server's Providers page; " +
	"a machine that doesn't report to one sets SLOP_REVIEW_PROVIDER=openrouter (e.g. in the repo-root .env), and --provider=openrouter does it for one run"

// LoginHint is what to do about a missing or broken ChatGPT login.
const LoginHint = "run `pi`, then `/login` and choose Sign in with ChatGPT (provider openai); or " + switchHint

const usageURL = "https://chatgpt.com/settings/usage"

// TokenSource supplies the bearer token for each request (see
// internal/piauth). Refresh is called once when the backend rejects a
// token, and must return a different one or an error.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Refresh(ctx context.Context, stale string) (string, error)
}

type ResponsesOptions struct {
	BaseURL string
	Tokens  TokenSource
	// ReasoningEffort is sent as reasoning.effort; empty leaves the model's
	// default, which is what OpenRouter's plain slug gets too.
	ReasoningEffort string
	// MaxRetries bounds retries of a turn that failed transiently (a 5xx,
	// a rate limit that isn't the usage limit, a dropped stream).
	MaxRetries int
	// RetryDelay is the wait before the first retry, doubled for each
	// after. Tests set it to zero.
	RetryDelay time.Duration
	// TurnTimeout bounds one request, stream included.
	TurnTimeout time.Duration
}

// ResponsesLLMProvider talks to OpenAI's Responses API with a Sign in with
// ChatGPT token: a subscription, not an API key. Such a token is only
// accepted on a streamed request with store: false and without
// max_output_tokens, temperature or prompt-cache retention settings (the
// same constraints pi-ai applies), and the response's encrypted reasoning
// has to be replayed for the model to keep its reasoning across turns --
// that rides in Message.ProviderState.
type ResponsesLLMProvider struct {
	service responses.ResponseService
	opts    ResponsesOptions
}

var _ LLMProvider = (*ResponsesLLMProvider)(nil)

func NewResponsesLLMProvider(opts *ResponsesOptions) *ResponsesLLMProvider {
	o := *opts
	if o.TurnTimeout <= 0 {
		o.TurnTimeout = 15 * time.Minute
	}
	return &ResponsesLLMProvider{
		// A bare service rather than openai.NewClient: the client's
		// defaults read OPENAI_API_KEY/OPENAI_ORG_ID/OPENAI_PROJECT_ID from
		// the environment, none of which belong on a subscription request.
		// Retries are ours (see Complete), so the SDK's are off: it would
		// retry a usage-limit 429 too.
		service: responses.NewResponseService(
			option.WithBaseURL(o.BaseURL),
			option.WithMaxRetries(0),
			option.WithRequestTimeout(o.TurnTimeout),
		),
		opts: o,
	}
}

func (p *ResponsesLLMProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	params, err := buildResponsesParams(req, p.opts.ReasoningEffort)
	if err != nil {
		return CompletionResponse{}, err
	}

	token, err := p.opts.Tokens.Token(ctx)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%w: %v; %s", ErrAuth, err, LoginHint)
	}

	// Every attempt is a request, the token refresh's retry and the
	// transient retries included: each is sent, and may be billed.
	var attempts int64
	failed := func(err error) (CompletionResponse, error) {
		return CompletionResponse{Usage: Usage{Requests: attempts}}, err
	}
	refreshed := false
	delay := p.opts.RetryDelay
	for retries := 0; ; {
		attempts++
		resp, err := p.stream(ctx, params, token)
		if err == nil {
			resp.Usage.Requests = attempts
			return resp, nil
		}
		if ctx.Err() != nil {
			return failed(ctx.Err())
		}

		switch class := classify(err); {
		case class == classAuth && !refreshed:
			refreshed = true
			token, err = p.opts.Tokens.Refresh(ctx, token)
			if err != nil {
				return failed(fmt.Errorf("%w: the backend rejected the token and no fresh one was available: %v; %s", ErrAuth, err, LoginHint))
			}
		case class == classTransient && retries < p.opts.MaxRetries:
			retries++
			if err := sleep(ctx, delay); err != nil {
				return failed(err)
			}
			delay *= 2
		default:
			return failed(p.failure(class, err, token))
		}
	}
}

// failure builds the error a turn fails with. It carries err's text, not
// err itself: nothing reachable through Unwrap can then carry the token,
// and the text is scrubbed of it besides.
func (p *ResponsesLLMProvider) failure(class errClass, err error, token string) error {
	detail := errorText(err, token)
	switch class {
	case classUsageLimit:
		return fmt.Errorf("%w: %s; check usage at %s, or %s", ErrUsageLimit, detail, usageURL, switchHint)
	case classAuth:
		return fmt.Errorf("%w: the backend rejected a freshly fetched token: %s; %s", ErrAuth, detail, LoginHint)
	default:
		return fmt.Errorf("%w: %s; %s", ErrBackend, detail, switchHint)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// stream runs one request and assembles the assistant turn from its
// events. The completed event's own output list is empty when store is
// false, so the turn is built from each output_item.done instead.
func (p *ResponsesLLMProvider) stream(ctx context.Context, params *responses.ResponseNewParams, token string) (CompletionResponse, error) {
	s := p.service.NewStreaming(ctx, *params, option.WithAPIKey(token))
	defer func() { _ = s.Close() }()

	var items []indexedItem
	var usage *responses.ResponseUsage
	for s.Next() {
		ev := s.Current()
		switch ev.Type {
		case "response.output_item.done":
			items = append(items, indexedItem{index: ev.OutputIndex, raw: ev.Item.RawJSON()})
		case "response.completed":
			u := ev.Response.Usage
			usage = &u
		case "response.incomplete":
			return CompletionResponse{}, &streamError{code: "incomplete", message: "response incomplete: " + ev.Response.IncompleteDetails.Reason}
		case "response.failed":
			return CompletionResponse{}, &streamError{code: string(ev.Response.Error.Code), message: ev.Response.Error.Message}
		case "error":
			return CompletionResponse{}, &streamError{code: ev.Code, message: ev.Message}
		}
	}
	if err := s.Err(); err != nil {
		return CompletionResponse{}, err
	}
	if usage == nil {
		return CompletionResponse{}, errStreamEnded
	}

	msg, err := assistantFromItems(items)
	if err != nil {
		return CompletionResponse{}, err
	}
	input, cacheRead := promptUsage(usage.InputTokens, usage.InputTokensDetails.CachedTokens)
	return CompletionResponse{Message: msg, Usage: Usage{
		InputTokens:     input,
		CacheReadTokens: cacheRead,
		OutputTokens:    usage.OutputTokens,
		ReasoningTokens: reasoningUsage(usage.OutputTokens, usage.OutputTokensDetails.ReasoningTokens),
	}}, nil
}

type indexedItem struct {
	index int64
	raw   string
}

// outputItem is the part of a Responses output item this provider reads.
// Phase isn't in the SDK's types yet, and has to be replayed.
type outputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Phase     string `json:"phase,omitempty"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

// providerState is what ResponsesLLMProvider keeps in
// Message.ProviderState: the turn's output items, in order and in the form
// they go back as input. Reasoning items are kept byte for byte (their
// encrypted content is what the model needs back); messages and function
// calls are reduced to the fields input accepts, keeping their item ids,
// which the backend pairs with the reasoning that produced them.
type providerState struct {
	Items []json.RawMessage `json:"items"`
}

func assistantFromItems(items []indexedItem) (Message, error) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].index < items[j].index })

	msg := Message{Role: RoleAssistant}
	var state providerState
	var text []string
	for _, it := range items {
		var item outputItem
		if err := json.Unmarshal([]byte(it.raw), &item); err != nil {
			return Message{}, fmt.Errorf("decode output item: %w", err)
		}
		switch item.Type {
		case "reasoning":
			state.Items = append(state.Items, json.RawMessage(it.raw))
		case "message":
			var parts []string
			for _, c := range item.Content {
				if c.Type == "refusal" {
					parts = append(parts, c.Refusal)
				} else {
					parts = append(parts, c.Text)
				}
			}
			t := strings.Join(parts, "")
			text = append(text, t)
			state.Items = append(state.Items, mustMarshal(map[string]any{
				"type": "message", "role": "assistant", "id": item.ID, "status": "completed",
				"phase":   omitEmpty(item.Phase),
				"content": []map[string]any{{"type": "output_text", "text": t, "annotations": []any{}}},
			}))
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, ArgumentsJSON: item.Arguments})
			state.Items = append(state.Items, mustMarshal(map[string]any{
				"type": "function_call", "id": item.ID, "call_id": item.CallID, "name": item.Name, "arguments": item.Arguments,
			}))
		}
		// Other item types (built-in tools) can't occur: no such tools
		// are offered.
	}
	msg.Content = strings.Join(text, "\n\n")
	if len(state.Items) > 0 {
		msg.ProviderState = mustMarshal(state)
	}
	return msg, nil
}

// omitEmpty returns nil for "", so a map marshals it as null -- which
// mustMarshal's caller then strips (see mustMarshal).
func omitEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mustMarshal marshals maps and structs built here from plain values, and
// drops nil map entries, so an absent optional field is omitted rather
// than sent as null.
func mustMarshal(v any) json.RawMessage {
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			if val == nil {
				delete(m, k)
			}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("llm: marshal %T: %v", v, err))
	}
	return b
}

func buildResponsesParams(req CompletionRequest, effort string) (*responses.ResponseNewParams, error) {
	params := &responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		Store: param.NewOpt(false),
		// Without this the reasoning isn't returned at all, and with store
		// false there's nothing server-side to refer back to either.
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(effort)}
	}

	var instructions []string
	var input responses.ResponseInputParam
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			instructions = append(instructions, m.Content)
		case RoleUser:
			input = append(input, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleUser))
		case RoleTool:
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(m.ToolCallID, m.Content))
		case RoleAssistant:
			items, err := assistantInput(m)
			if err != nil {
				return nil, err
			}
			input = append(input, items...)
		}
	}
	if len(instructions) > 0 {
		params.Instructions = param.NewOpt(strings.Join(instructions, "\n\n"))
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}

	if len(req.Tools) > 0 {
		params.Tools = make([]responses.ToolUnionParam, len(req.Tools))
		for i, t := range req.Tools {
			params.Tools[i] = responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  t.Parameters,
				// Not strict, for the same reason as the chat-completions
				// provider: strict needs every property required.
				Strict: param.NewOpt(false),
			}}
		}
		// Forced for the same reason as OpenAIAPILLMProvider: a review
		// turn always has a tool to call, and prose instead of
		// submit_review is a turn wasted.
		params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired),
		}
		if req.ForceToolName != "" {
			params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{
				OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: req.ForceToolName},
			}
		}
	}
	return params, nil
}

// assistantInput replays an assistant turn: from its ProviderState when it
// has one, which carries the reasoning and item ids the backend needs;
// otherwise from its content and tool calls alone.
func assistantInput(m Message) ([]responses.ResponseInputItemUnionParam, error) {
	if len(m.ProviderState) > 0 {
		var state providerState
		if err := json.Unmarshal(m.ProviderState, &state); err != nil {
			return nil, fmt.Errorf("llm: assistant message has unreadable provider state: %w", err)
		}
		out := make([]responses.ResponseInputItemUnionParam, len(state.Items))
		for i, raw := range state.Items {
			out[i] = rawInputItem(raw)
		}
		return out, nil
	}

	var out []responses.ResponseInputItemUnionParam
	if m.Content != "" {
		out = append(out, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleAssistant))
	}
	for _, tc := range m.ToolCalls {
		out = append(out, responses.ResponseInputItemParamOfFunctionCall(tc.ArgumentsJSON, tc.ID, tc.Name))
	}
	return out, nil
}

// rawInputItem sends an item exactly as stored. The reasoning variant is
// only the vehicle: Override replaces its whole JSON with raw.
func rawInputItem(raw json.RawMessage) responses.ResponseInputItemUnionParam {
	item := param.Override[responses.ResponseReasoningItemParam](raw)
	return responses.ResponseInputItemUnionParam{OfReasoning: &item}
}

type errClass int

const (
	classBackend errClass = iota
	classTransient
	classAuth
	classUsageLimit
)

// streamError is an error the backend reported inside a 200 stream.
type streamError struct {
	code    string
	message string
}

func (e *streamError) Error() string {
	if e.code == "" {
		return e.message
	}
	return e.code + ": " + e.message
}

var errStreamEnded = errors.New("the response stream ended before the response completed")

// usageLimitMarkers identify a usage-limit error however it arrives: an
// HTTP 429 body or an error event. subscription_sharing_... is what pi-ai
// keys on for this token; usage_limit_reached is the Codex backend's.
var usageLimitMarkers = []string{"usage_limit", "insufficient_quota"}

var transientStreamCodes = []string{"rate_limit_exceeded", "server_error", "server_is_overloaded", "slow_down"}

func classify(err error) errClass {
	text := errorText(err, "")
	for _, m := range usageLimitMarkers {
		if strings.Contains(text, m) {
			return classUsageLimit
		}
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch s := apiErr.StatusCode; {
		case s == http.StatusUnauthorized:
			return classAuth
		case s == http.StatusRequestTimeout, s == http.StatusConflict, s == http.StatusTooManyRequests, s >= 500:
			return classTransient
		default:
			return classBackend
		}
	}

	var se *streamError
	if errors.As(err, &se) {
		for _, c := range transientStreamCodes {
			if se.code == c {
				return classTransient
			}
		}
		return classBackend
	}

	// A dropped connection, a stream cut off mid-response, an SSE error
	// payload the SDK surfaced as a plain error: all worth another try.
	return classTransient
}
