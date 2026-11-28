package pi

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
	return &TranscriptReader{Environ: environ, AgentDir: h.AgentDir}
}

// TranscriptReader reads pi's session JSONL,
// <agent dir>/sessions/--<cwd>--/<time>_<session id>.jsonl, where the agent
// dir is $PI_CODING_AGENT_DIR, else ~/.pi/agent.
type TranscriptReader struct {
	Environ func() []string
	// AgentDir overrides the environment's, as Harness.AgentDir does.
	AgentDir string
}

// Locate implements transcript.Reader.
func (r *TranscriptReader) Locate(id string) (string, error) {
	dir := r.AgentDir
	if dir == "" {
		var err error
		if dir, err = transcript.HarnessDir(r.Environ(), "PI_CODING_AGENT_DIR", filepath.Join(".pi", "agent")); err != nil {
			return "", err
		}
	}
	return transcript.Find(id, func(id string) string { return filepath.Join(dir, "sessions", "*", "*_"+id+".jsonl") })
}

type sessionLine struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Message   sessionMessage `json:"message"`
}

type sessionMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	Usage      *struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
	} `json:"usage"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Parse implements transcript.Reader: the message lines, by role. The
// system prompt, model and thinking-level changes are settings, not
// conversation.
func (r *TranscriptReader) Parse(data []byte) ([]transcript.Entry, error) {
	var out []transcript.Entry
	for _, raw := range transcript.Lines(data) {
		var l sessionLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "message" {
			continue
		}
		at := transcript.ParseTime(l.Timestamp)
		m := &l.Message
		blocks := piBlocks(m.Content)
		switch m.Role {
		case "user":
			if text := joinText(blocks); text != "" {
				out = append(out, transcript.Entry{At: at, Kind: transcript.User, Text: text})
			}
		case "assistant":
			out = append(out, assistantEntries(at, m, blocks)...)
		case "toolResult":
			out = append(out, transcript.Entry{At: at, Kind: transcript.ToolResult, Tool: m.ToolName, CallID: m.ToolCallID, Text: joinText(blocks), IsError: m.IsError})
		}
	}
	return out, nil
}

func assistantEntries(at time.Time, m *sessionMessage, blocks []piBlock) []transcript.Entry {
	var out []transcript.Entry
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, transcript.Entry{At: at, Kind: transcript.Assistant, Text: b.Text})
			}
		case "toolCall":
			out = append(out, transcript.Entry{At: at, Kind: transcript.ToolCall, Tool: b.Name, CallID: b.ID, Text: toolArgs(b.Arguments)})
		}
	}
	if u := m.Usage; u != nil {
		// pi's input already excludes the cached parts.
		out = append(out, transcript.Entry{At: at, Kind: transcript.UsageKind, Usage: &transcript.Usage{
			Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
		}})
	}
	return out
}

func piBlocks(raw json.RawMessage) []piBlock {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []piBlock{{Type: "text", Text: s}}
	}
	var blocks []piBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

func joinText(blocks []piBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolArgs shows a bash call as its command, anything else as its JSON.
func toolArgs(raw json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) == nil && in.Command != "" {
		return in.Command
	}
	return string(raw)
}
