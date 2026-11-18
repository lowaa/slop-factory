package pricing

// aliases map the model names harnesses report to OpenRouter's ids. They
// are explicit rather than derived from a naming rule: a rule that guessed
// wrong would price usage as the wrong model, where a missing alias leaves
// it visibly unpriced. A test checks every target is in the generated
// table, so a regeneration that drops one fails.
var aliases = map[string]string{
	// Claude Code reports Anthropic's API ids.
	"claude-opus-5-5":           "anthropic/claude-opus-5.5",
	"claude-opus-5":             "anthropic/claude-opus-5",
	"claude-opus-4-8":           "anthropic/claude-opus-4.8",
	"claude-opus-4-7":           "anthropic/claude-opus-4.7",
	"claude-opus-4-6":           "anthropic/claude-opus-4.6",
	"claude-opus-4-5":           "anthropic/claude-opus-4.5",
	"claude-sonnet-5-5":         "anthropic/claude-sonnet-5.5",
	"claude-sonnet-5":           "anthropic/claude-sonnet-5",
	"claude-sonnet-4-6":         "anthropic/claude-sonnet-4.6",
	"claude-sonnet-4-5":         "anthropic/claude-sonnet-4.5",
	"claude-fable-5-1":          "anthropic/claude-fable-5.1",
	"claude-fable-5":            "anthropic/claude-fable-5",
	"claude-haiku-4-5":          "anthropic/claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "anthropic/claude-haiku-4.5",

	// a ChatGPT-subscription provider reports OpenAI's bare names.
	"gpt-6-luna":        "openai/gpt-6-luna",
	"gpt-6-luna-pro":    "openai/gpt-6-luna-pro",
	"gpt-5.6-luna":      "openai/gpt-5.6-luna",
	"gpt-5.6-luna-pro":  "openai/gpt-5.6-luna-pro",
	"gpt-5.6-terra":     "openai/gpt-5.6-terra",
	"gpt-5.6-terra-pro": "openai/gpt-5.6-terra-pro",
	"gpt-5.6-sol":       "openai/gpt-5.6-sol",
	"gpt-5.6-sol-pro":   "openai/gpt-5.6-sol-pro",
	"gpt-5.5":           "openai/gpt-5.5",
	"gpt-5.5-pro":       "openai/gpt-5.5-pro",
}
