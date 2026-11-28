package codex

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

// TranscriptReader reads Codex's rollout JSONL,
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl (else
// under ~/.codex). app-server, exec and the TUI all write it, and a resumed
// thread keeps its file.
type TranscriptReader struct {
	Environ func() []string
}

// Locate implements transcript.Reader.
func (r *TranscriptReader) Locate(id string) (string, error) {
	dir, err := transcript.HarnessDir(r.Environ(), "CODEX_HOME", ".codex")
	if err != nil {
		return "", err
	}
	return transcript.Find(id, func(id string) string {
		return filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl")
	})
}

type rolloutLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type string `json:"type"`
		// item_completed
		Item          json.RawMessage `json:"item"`
		StartedAtMs   int64           `json:"started_at_ms"`
		CompletedAtMs int64           `json:"completed_at_ms"`
		// token_count
		Info *struct {
			Last struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type rolloutItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	// CommandExecution
	Command          []string `json:"command"`
	AggregatedOutput string   `json:"aggregated_output"`
	ExitCode         *int     `json:"exit_code"`
	Status           string   `json:"status"`
}

// Parse implements transcript.Reader. It reads the event_msg records:
// item_completed is the conversation as the user saw it (their own
// messages, without the environment context Codex injects as user
// messages; and each shell command as run, where the model's
// response_items show only the script that ran them), and token_count is
// each model call's usage.
func (r *TranscriptReader) Parse(data []byte) ([]transcript.Entry, error) {
	var out []transcript.Entry
	for _, raw := range transcript.Lines(data) {
		var l rolloutLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "event_msg" {
			continue
		}
		at := transcript.ParseTime(l.Timestamp)
		switch l.Payload.Type {
		case "item_completed":
			out = append(out, itemEntries(l.Payload.Item, msTime(l.Payload.StartedAtMs, at), msTime(l.Payload.CompletedAtMs, at))...)
		case "token_count":
			if info := l.Payload.Info; info != nil {
				u := &info.Last
				// OpenAI's input count includes the cached parts, which the
				// model counts apart (as the codec does).
				out = append(out, transcript.Entry{At: at, Kind: transcript.UsageKind, Usage: &transcript.Usage{
					Input: max(u.InputTokens-u.CachedInputTokens-u.CacheWriteInputTokens, 0), Output: u.OutputTokens,
					CacheRead: u.CachedInputTokens, CacheWrite: u.CacheWriteInputTokens,
				}})
			}
		}
	}
	return out, nil
}

func itemEntries(raw json.RawMessage, started, completed time.Time) []transcript.Entry {
	var it rolloutItem
	if json.Unmarshal(raw, &it) != nil {
		return nil
	}
	switch it.Type {
	case "UserMessage", "AgentMessage":
		var parts []string
		for _, c := range it.Content {
			parts = append(parts, c.Text)
		}
		kind := transcript.Assistant
		if it.Type == "UserMessage" {
			kind = transcript.User
		}
		return []transcript.Entry{{At: completed, Kind: kind, Text: strings.Join(parts, "\n")}}
	case "CommandExecution":
		failed := it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
		return []transcript.Entry{
			{At: started, Kind: transcript.ToolCall, Tool: "shell", CallID: it.ID, Text: shellCommand(it.Command)},
			{At: completed, Kind: transcript.ToolResult, Tool: "shell", CallID: it.ID, Text: it.AggregatedOutput, IsError: failed},
		}
	case "FileChange", "McpToolCall", "DynamicToolCall", "WebSearch":
		// Not yet recorded from a real session, so shown whole rather than
		// parsed field by field.
		return []transcript.Entry{{At: completed, Kind: transcript.ToolCall, Tool: it.Type, CallID: it.ID, Text: string(raw)}}
	}
	// Reasoning, plans, and item types this reader doesn't know yet.
	return nil
}

// shellCommand is the command as the model wrote it: Codex runs each as
// `bash -lc <command>`.
func shellCommand(argv []string) string {
	if len(argv) == 3 && strings.HasSuffix(argv[0], "sh") && argv[1] == "-lc" {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

func msTime(ms int64, fallback time.Time) time.Time {
	if ms == 0 {
		return fallback
	}
	return time.UnixMilli(ms).UTC()
}
