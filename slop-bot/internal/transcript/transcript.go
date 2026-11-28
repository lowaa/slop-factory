// Package transcript is the common model of an agent's conversation, as
// every harness records it in its own transcript file: user and assistant
// turns, tool calls and their results, and usage. Each harness has a Reader
// that finds its file for a conversation and normalises it; `slop-bot
// transcript` is a view over the result, and the Archive keeps a normalised
// copy once the agent ends, because harnesses prune their own.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind classifies an Entry.
type Kind string

// Entry kinds.
const (
	User       Kind = "user"
	Assistant  Kind = "assistant"
	ToolCall   Kind = "tool_call"
	ToolResult Kind = "tool_result"
	UsageKind  Kind = "usage"
)

// Entry is one thing in a conversation.
type Entry struct {
	At   time.Time `json:"at,omitzero"`
	Kind Kind      `json:"kind"`
	// Text is the message on user and assistant entries, the input on a
	// tool call, and the output on a tool result.
	Text string `json:"text,omitempty"`
	// Tool and CallID are set on tool calls and results; CallID pairs them.
	Tool   string `json:"tool,omitempty"`
	CallID string `json:"call_id,omitempty"`
	// IsError marks a tool result that failed.
	IsError bool   `json:"is_error,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
	// Truncated is how many bytes of Text were cut for display.
	Truncated int `json:"truncated,omitempty"`
}

// Usage is the tokens one model call used. Input excludes the cached parts,
// which are counted apart, as harness.Usage does.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

// Reader finds and normalises one harness's transcripts.
type Reader interface {
	// Locate is the path of conversation id's transcript, or ErrNotFound.
	Locate(id string) (string, error)
	// Parse normalises a transcript's content. A last line without its
	// newline is still being written, and is left for the next read.
	Parse(data []byte) ([]Entry, error)
}

// Provider is a harness that can read its own transcripts. It is apart from
// harness.AIHarness, which stays about driving a conversation.
type Provider interface {
	Transcripts() Reader
}

// ErrNotFound is a conversation with no transcript to read.
var ErrNotFound = errors.New("transcript: not found")

// Lines splits data into its complete lines, dropping blank ones and a
// trailing line that has no newline yet.
func Lines(data []byte) [][]byte {
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	} else {
		return nil
	}
	var out [][]byte
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			out = append(out, line)
		}
	}
	return out
}

// Encode writes entries as JSON lines: the normalised format, which the
// Archive keeps and the daemon streams.
func Encode(w io.Writer, entries []Entry) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for i := range entries {
		if err := enc.Encode(&entries[i]); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// Decode reads the normalised format back.
func Decode(data []byte) ([]Entry, error) {
	lines := Lines(data)
	out := make([]Entry, 0, len(lines))
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("transcript: line %d: %w", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// DefaultTruncate is how much of a tool call's input or result is shown
// unless the whole transcript is asked for: long outputs are what waste a
// controller session's context.
const DefaultTruncate = 2000

// Truncate cuts tool inputs and outputs longer than limit bytes (at a rune
// boundary), recording how much was cut. Messages are left whole. It
// returns a copy.
func Truncate(entries []Entry, limit int) []Entry {
	out := slices.Clone(entries)
	for i := range out {
		e := &out[i]
		if e.Kind == ToolCall || e.Kind == ToolResult {
			e.Text, e.Truncated = Cut(e.Text, limit)
		}
	}
	return out
}

// Cut shortens s to at most limit bytes, at a rune boundary, and says how
// many bytes it dropped.
func Cut(s string, limit int) (string, int) {
	if len(s) <= limit {
		return s, 0
	}
	cut := max(limit, 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], len(s) - cut
}

// Since keeps the entries at or after t; a zero t keeps all. Entries with
// no time are kept, so a harness that doesn't stamp some never loses them.
func Since(entries []Entry, t time.Time) []Entry {
	if t.IsZero() {
		return entries
	}
	return slices.DeleteFunc(slices.Clone(entries), func(e Entry) bool { return !e.At.IsZero() && e.At.Before(t) })
}

// Tail keeps the last n entries; n <= 0 keeps all.
func Tail(entries []Entry, n int) []Entry {
	if n <= 0 || n >= len(entries) {
		return entries
	}
	return entries[len(entries)-n:]
}

// Render writes e as text for a person.
func Render(w io.Writer, e *Entry) error {
	var head, body string
	switch e.Kind {
	case User, Assistant:
		head, body = string(e.Kind), e.Text
	case ToolCall:
		head, body = "tool "+e.Tool, e.Text
	case ToolResult:
		head, body = "result "+e.Tool, e.Text
		if e.IsError {
			head += " (error)"
		}
	case UsageKind:
		head = "usage"
		if u := e.Usage; u != nil {
			body = fmt.Sprintf("input %d, output %d, cache read %d, cache write %d", u.Input, u.Output, u.CacheRead, u.CacheWrite)
		}
	default:
		head, body = string(e.Kind), e.Text
	}
	if e.Truncated > 0 {
		body += fmt.Sprintf("\n… %d more bytes (--full shows them)", e.Truncated)
	}
	stamp := "--:--:--"
	if !e.At.IsZero() {
		stamp = e.At.Local().Format("15:04:05")
	}
	body = strings.ReplaceAll(strings.TrimRight(body, "\n"), "\n", "\n    ")
	_, err := fmt.Fprintf(w, "[%s] %s: %s\n", stamp, head, body)
	return err
}

// validID is what a conversation id may look like before it goes into a
// path or glob: harness ids are UUIDs or similar, never paths.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Find is the newest file matching pattern, where id has been checked to
// be safe to substitute into it; ErrNotFound when none matches.
func Find(id string, pattern func(id string) string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("transcript: %q isn't a conversation id", id)
	}
	matches, err := filepath.Glob(pattern(id))
	if err != nil {
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if best == "" || fi.ModTime().After(bestTime) {
			best, bestTime = m, fi.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf("%w: conversation %s", ErrNotFound, id)
	}
	return best, nil
}

// HarnessDir is a harness's own directory as its process would see it under
// environ: the variable's value when set, else rel under $HOME (the
// daemon's home when environ names none).
func HarnessDir(environ []string, variable, rel string) (string, error) {
	if v := lookup(environ, variable); v != "" {
		return v, nil
	}
	home := lookup(environ, "HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, rel), nil
}

// lookup is the last value environ gives name, as exec would pass it.
func lookup(environ []string, name string) string {
	v := ""
	for _, kv := range environ {
		if rest, ok := strings.CutPrefix(kv, name+"="); ok {
			v = rest
		}
	}
	return v
}

// ParseTime reads a harness's RFC 3339 timestamp; a bad or missing one is
// zero rather than an error, as it only orders and filters entries.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
