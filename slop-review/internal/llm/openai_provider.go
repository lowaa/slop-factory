package llm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Errors an OpenAIAPILLMProvider fails a turn with, for callers to tell
// apart with errors.Is (see internal/runreport, which reports them as a
// failed run's class). The openai-go client has already retried whatever
// it considers transient by then.
var (
	// ErrKeyRejected: the endpoint refused the API key (401).
	ErrKeyRejected = errors.New("the API key was rejected")
	// ErrCredits: the account behind the key has no credits left (402).
	ErrCredits = errors.New("the account is out of credits")
	// ErrRequest: anything else the endpoint or the connection to it did
	// wrong.
	ErrRequest = errors.New("API request failed")
)

// OpenAIAPILLMProvider talks to any OpenAI-compatible chat-completions
// endpoint (OpenRouter, OpenAI itself, or anything else that implements the
// same API shape) via the official openai-go client, pointed at baseURL.
type OpenAIAPILLMProvider struct {
	chat        openai.ChatCompletionService
	apiKey      string
	reportsCost bool
}

var _ LLMProvider = (*OpenAIAPILLMProvider)(nil)

type OpenAIOptions struct {
	BaseURL string
	APIKey  string
	// ReportsCost says the backend reports what each completion cost, as
	// OpenRouter does in usage.cost (always, now: its docs deprecate the
	// usage.include request flag as having no effect). Only then is the
	// field read -- this provider serves OpenAI too, and a cost is never
	// inferred.
	ReportsCost bool
}

func NewOpenAIAPILLMProvider(opts *OpenAIOptions) *OpenAIAPILLMProvider {
	return &OpenAIAPILLMProvider{
		// A bare service rather than openai.NewClient, whose defaults read
		// OPENAI_* from the environment -- OPENAI_CUSTOM_HEADERS among
		// them, which would send whatever headers another tool set there
		// to this provider.
		chat:        openai.NewChatCompletionService(option.WithBaseURL(opts.BaseURL), option.WithAPIKey(opts.APIKey)),
		apiKey:      opts.APIKey,
		reportsCost: opts.ReportsCost,
	}
}

func (p *OpenAIAPILLMProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: toOpenAIMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAITools(req.Tools)
		// Every turn in a review session has a tool to call -- one of the
		// navigation tools, or submit_review once it's done -- so force
		// it rather than leaving tool use optional (the default): without
		// this, a model can (and in practice sometimes does) answer in
		// plain prose instead of calling submit_review, which the session
		// loop has no way to treat as a finished review.
		params.ToolChoice.OfAuto = param.NewOpt(string(openai.ChatCompletionToolChoiceOptionAutoRequired))
		if req.ForceToolName != "" {
			params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
				OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
					Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: req.ForceToolName},
				},
			}
		}
	}

	// The SDK retries inside New, so its attempts are counted where it
	// sends them: middleware runs once per HTTP attempt.
	var attempts int64
	countAttempts := option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		attempts++
		return next(r)
	})
	resp, err := p.chat.New(ctx, params, countAttempts)
	if err != nil {
		return CompletionResponse{Usage: Usage{Requests: attempts}}, p.failure(err)
	}
	usage := p.usage(&resp.Usage)
	usage.Requests = attempts
	if len(resp.Choices) == 0 {
		return CompletionResponse{Usage: usage}, fmt.Errorf("llm: %w: chat completion response had no choices", ErrRequest)
	}

	return CompletionResponse{Message: fromOpenAIMessage(resp.Choices[0].Message), Usage: usage}, nil
}

// usage reads a completion's usage, with its cost only from a backend that
// reports one.
func (p *OpenAIAPILLMProvider) usage(u *openai.CompletionUsage) Usage {
	input, cacheRead := promptUsage(u.PromptTokens, u.PromptTokensDetails.CachedTokens)
	out := Usage{
		InputTokens:     input,
		CacheReadTokens: cacheRead,
		OutputTokens:    u.CompletionTokens,
		ReasoningTokens: reasoningUsage(u.CompletionTokens, u.CompletionTokensDetails.ReasoningTokens),
	}
	if p.reportsCost {
		if f, ok := u.JSON.ExtraFields["cost"]; ok {
			out.ReportedCostMicros = dollarsToMicros(f.Raw())
		}
	}
	return out
}

var microsPerDollar = big.NewRat(1_000_000, 1)

// dollarsToMicros converts a JSON number of dollars (OpenRouter reports
// credits, which are dollars) to whole micro-dollars, rounding half up. It
// parses the number's own text, exponent forms included, so no float
// rounding creeps in; anything that isn't a non-negative number is no
// cost at all rather than a wrong one.
func dollarsToMicros(raw string) *int64 {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok || r.Sign() < 0 {
		return nil
	}
	r.Mul(r, microsPerDollar)
	r.Add(r, big.NewRat(1, 2))
	micros := new(big.Int).Quo(r.Num(), r.Denom())
	if !micros.IsInt64() {
		return nil
	}
	v := micros.Int64()
	return &v
}

// failure classifies a failed request. Like ResponsesLLMProvider.failure it
// carries err's text rather than err, scrubbed of the key: the error ends up
// in a failed run's report, and an endpoint that echoes the credential back
// must not put it there.
func (p *OpenAIAPILLMProvider) failure(err error) error {
	detail := errorText(err, p.apiKey)
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("llm: %w: %s; check the provider's API key (OPENROUTER_API_KEY for openrouter)", ErrKeyRejected, detail)
		case http.StatusPaymentRequired:
			return fmt.Errorf("llm: %w: %s; top up the account (https://openrouter.ai/settings/credits for openrouter)", ErrCredits, detail)
		}
	}
	return fmt.Errorf("llm: %w: chat completion request: %s", ErrRequest, detail)
}

func toOpenAIMessages(msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		case RoleAssistant:
			out = append(out, toOpenAIAssistantMessage(m))
		}
	}
	return out
}

// toOpenAIAssistantMessage is split out of toOpenAIMessages because, unlike
// the other roles, an assistant turn that called tools needs its ToolCalls
// round-tripped back to the API alongside (or instead of) Content -- the
// openai.AssistantMessage helper only covers the content-only case.
func toOpenAIAssistantMessage(m Message) openai.ChatCompletionMessageParamUnion {
	if len(m.ToolCalls) == 0 {
		return openai.AssistantMessage(m.Content)
	}

	assistant := openai.ChatCompletionAssistantMessageParam{
		ToolCalls: make([]openai.ChatCompletionMessageToolCallUnionParam, len(m.ToolCalls)),
	}
	if m.Content != "" {
		assistant.Content.OfString = param.NewOpt(m.Content)
	}
	for i, tc := range m.ToolCalls {
		assistant.ToolCalls[i] = openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: tc.ArgumentsJSON,
				},
			},
		}
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}

func toOpenAITools(tools []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, len(tools))
	for i, t := range tools {
		// Strict mode is deliberately left unset: it requires every schema
		// property to be listed in "required" (optional fields modeled as a
		// type/null union instead), and isn't uniformly supported across
		// the backends OpenRouter can route "openrouter" model slugs to.
		out[i] = openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: param.NewOpt(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		})
	}
	return out
}

func fromOpenAIMessage(m openai.ChatCompletionMessage) Message {
	out := Message{Role: RoleAssistant, Content: m.Content}
	if len(m.ToolCalls) == 0 {
		return out
	}
	out.ToolCalls = make([]ToolCall, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		out.ToolCalls[i] = ToolCall{
			ID:            tc.ID,
			Name:          tc.Function.Name,
			ArgumentsJSON: tc.Function.Arguments,
		}
	}
	return out
}
