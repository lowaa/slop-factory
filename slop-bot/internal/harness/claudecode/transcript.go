package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// Transcripts implements transcript.Provider.
func (h *Harness) Transcripts() transcript.Reader {
	environ := h.Environ
	if environ == nil {
		environ = os.Environ
	}
	return &TranscriptReader{Environ: environ}
}

// TranscriptReader reads Claude Code's session JSONL,
// $CLAUDE_CONFIG_DIR/projects/<cwd with / and . as ->/<session id>.jsonl
// (else under ~/.claude). Programmatic and TUI sessions write the same
// file, so one transcript covers a conversation across attaches.
type TranscriptReader struct {
	Environ func() []string
}

// Locate implements transcript.Reader. It finds the session by id in any
// project directory, rather than by re-deriving the cwd's encoding.
func (r *TranscriptReader) Locate(id string) (string, error) {
	dir, err := transcript.HarnessDir(r.Environ(), "CLAUDE_CONFIG_DIR", ".claude")
	if err != nil {
		return "", err
	}
	return transcript.Find(id, func(id string) string { return filepath.Join(dir, "projects", "*", id+".jsonl") })
}

type transcriptLine struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Parse implements transcript.Reader. Only user and assistant lines are the
// conversation; the rest (attachments, API request records, cost state)
// are Claude Code's bookkeeping. One API message is split over several
// assistant lines, one per content block, each repeating the message's
// usage, so usage is counted once per message id.
func (r *TranscriptReader) Parse(data []byte) ([]transcript.Entry, error) {
	var out []transcript.Entry
	tools := map[string]string{}
	counted := map[string]bool{}
	for _, raw := range transcript.Lines(data) {
		var l transcriptLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		// Sub-agents' lines and Claude Code's own injected notes aren't
		// the agent's conversation.
		if l.IsSidechain || l.IsMeta || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		at := transcript.ParseTime(l.Timestamp)
		blocks := contentBlocks(l.Message.Content)
		for i := range blocks {
			if e, ok := claudeEntry(l.Type, at, &blocks[i], tools); ok {
				out = append(out, e)
			}
		}
		if u := l.Message.Usage; l.Type == "assistant" && u != nil && l.Message.ID != "" && !counted[l.Message.ID] {
			counted[l.Message.ID] = true
			out = append(out, transcript.Entry{At: at, Kind: transcript.UsageKind, Usage: &transcript.Usage{
				Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens,
			}})
		}
	}
	return out, nil
}

// contentBlocks reads a message's content, which is a plain string for a
// typed prompt and a list of blocks otherwise.
func contentBlocks(raw json.RawMessage) []contentBlock {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}
	var blocks []contentBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

func claudeEntry(role string, at time.Time, b *contentBlock, tools map[string]string) (transcript.Entry, bool) {
	switch b.Type {
	case "text":
		if strings.TrimSpace(b.Text) == "" {
			return transcript.Entry{}, false
		}
		kind := transcript.Assistant
		if role == "user" {
			kind = transcript.User
		}
		return transcript.Entry{At: at, Kind: kind, Text: b.Text}, true
	case "tool_use":
		tools[b.ID] = b.Name
		return transcript.Entry{At: at, Kind: transcript.ToolCall, Tool: b.Name, CallID: b.ID, Text: toolInput(b.Input)}, true
	case "tool_result":
		return transcript.Entry{At: at, Kind: transcript.ToolResult, Tool: tools[b.ToolUseID], CallID: b.ToolUseID,
			Text: resultText(b.Content), IsError: b.IsError}, true
	}
	// thinking (its text is not kept), images, and anything newer.
	return transcript.Entry{}, false
}

// toolInput shows a tool call's input: a shell command as itself, anything
// else as its JSON.
func toolInput(raw json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) == nil && in.Command != "" {
		return in.Command
	}
	return string(raw)
}

// resultText is a tool result's text, which is a string or a list of text
// blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
