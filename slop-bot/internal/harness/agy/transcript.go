package agy

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

// TranscriptReader reads agy's step log,
// ~/.gemini/antigravity-cli/brain/<conversation id>/.system_generated/logs/transcript_full.jsonl,
// the path every hook payload names as transcriptPath. The TUI has no flag
// or variable to move ~/.gemini (only `agy acp` takes --gemini-dir), so it
// is always under the home directory.
type TranscriptReader struct {
	Environ func() []string
}

// Locate implements transcript.Reader.
func (r *TranscriptReader) Locate(id string) (string, error) {
	dir, err := transcript.HarnessDir(r.Environ(), "", filepath.Join(".gemini", "antigravity-cli"))
	if err != nil {
		return "", err
	}
	return transcript.Find(id, func(id string) string {
		return filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript_full.jsonl")
	})
}

// step is one line of the step log.
type step struct {
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
}

// Parse implements transcript.Reader. Messages slop-bot's hooks injected
// are user entries, because they are the user's (or another principal's)
// messages delivered mid-turn; agy's own system notices are left out.
func (r *TranscriptReader) Parse(data []byte) ([]transcript.Entry, error) {
	var p stepParser
	for _, raw := range transcript.Lines(data) {
		var s step
		if json.Unmarshal(raw, &s) == nil {
			p.add(&s)
		}
	}
	return p.out, nil
}

// stepParser turns steps into entries, in order.
type stepParser struct {
	out []transcript.Entry
	// A tool's result follows its call without naming it: the last model
	// call's tool calls wait here for their results, in order. A cancelled
	// call never gets one, so the next model call starts afresh.
	calls []string
	// booting is the boot prompt's turn, the harness's start-up rather
	// than anything a person sent: its text is left out, its usage kept.
	booting bool
}

func (p *stepParser) add(s *step) {
	at := transcript.ParseTime(s.CreatedAt)
	switch s.Type {
	case "USER_INPUT":
		text := userRequest(s.Content)
		if p.booting = len(p.out) == 0 && text == bootPrompt; p.booting {
			return
		}
		p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.User, Text: text})
	case "SYSTEM_MESSAGE":
		if text, ok := injected(s.Content); ok {
			p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.User, Text: text})
		}
	case "PLANNER_RESPONSE":
		p.response(at, s)
	case "GENERIC":
		if s.Source != "MODEL" || len(p.calls) == 0 {
			return
		}
		tool := p.calls[0]
		p.calls = p.calls[1:]
		p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.ToolResult, Tool: tool, Text: s.Content, IsError: s.Status == "ERROR"})
	}
}

// response is one model call: its text, its tool calls and its usage.
func (p *stepParser) response(at time.Time, s *step) {
	p.calls = nil
	if strings.TrimSpace(s.Content) != "" && !p.booting {
		p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.Assistant, Text: s.Content})
	}
	for _, c := range s.ToolCalls {
		p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.ToolCall, Tool: c.Name, Text: toolArgs(c.Args)})
		p.calls = append(p.calls, c.Name)
	}
	if s.InputTokens+s.OutputTokens+s.CacheReadTokens > 0 {
		// agy's input already excludes the cached part.
		p.out = append(p.out, transcript.Entry{At: at, Kind: transcript.UsageKind, Usage: &transcript.Usage{
			Input: s.InputTokens, Output: s.OutputTokens, CacheRead: s.CacheReadTokens,
		}})
	}
}

// userRequest is the prompt inside the request agy wraps it in, with the
// metadata (local time, settings changes) it adds.
func userRequest(content string) string {
	const open, closing = "<USER_REQUEST>\n", "\n</USER_REQUEST>"
	_, rest, ok := strings.Cut(content, open)
	if !ok {
		return content
	}
	text, _, _ := strings.Cut(rest, closing)
	return text
}

// injected is the message a hook injected, from a system message's
// wrapping: a PreInvocation step, or a Stop hook's continue.
func injected(content string) (string, bool) {
	// The first and last tags: the message itself may hold anything.
	const open, closing = "\n\n<SYSTEM_MESSAGE>\n", "\n</SYSTEM_MESSAGE>"
	i := strings.Index(content, open)
	if i < 0 {
		return "", false
	}
	body := content[i+len(open):]
	if j := strings.LastIndex(body, closing); j >= 0 {
		body = body[:j]
	}
	if _, text, ok := strings.Cut(body, " hook:\n"); ok && strings.HasPrefix(body, "The following system message was injected by a ") {
		return text, true
	}
	if text, ok := strings.CutPrefix(body, "Stop hook blocked termination: "); ok {
		return text, true
	}
	return "", false
}

// toolArgs shows a shell command as its command line, anything else as its
// JSON, without agy's own summary of the call.
func toolArgs(raw json.RawMessage) string {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return string(raw)
	}
	var cmd string
	if json.Unmarshal(args["CommandLine"], &cmd) == nil && cmd != "" {
		return cmd
	}
	delete(args, "toolSummary")
	b, err := json.Marshal(args)
	if err != nil {
		return string(raw)
	}
	return string(b)
}
