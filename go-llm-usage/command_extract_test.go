package llmusage_test

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

var (
	wordPattern       = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
	assignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	// The word before an array assignment's `(`: NAME= or NAME+=.
	arrayAssignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=$`)

	// Dropped from the front of a simple command.
	leadingTokens = map[string]bool{
		"(": true, "{": true, "!": true,
		"if": true, "then": true, "else": true, "elif": true, "while": true, "until": true, "do": true,
	}
	// A simple command that is only one of these is dropped.
	closingTokens = map[string]bool{")": true, "}": true, "fi": true, "done": true, "esac": true}
	// A simple command starting with one of these is dropped.
	compoundStarts = map[string]bool{"for": true, "case": true, "function": true}

	// Wrapper -> the options that take a separate value. Other dash words are plain flags.
	wrappers = map[string]map[string]bool{
		"sudo":    {"-u": true, "-g": true},
		"env":     {"-u": true},
		"timeout": {"-s": true, "-k": true},
		"nohup":   {},
		"time":    {},
		"exec":    {},
		"command": {},
	}
)

// terminator is what ended a simple command. Only a case clause's own
// terminator matters to what follows: it puts the case statement back to
// expecting a pattern.
type terminator int

const (
	endList   terminator = iota // newline, `;`, `&`, `&&`, `||`, `|` or the end of the line
	endClause                   // `;;`, `;&` or `;;&`
)

type simpleCommand struct {
	words []string
	end   terminator
}

// skipQuoted returns the index of the quote that closes the one at i, or
// the last index when there is none. Backslash escapes count only in
// double quotes, unless escapes is set (ANSI-C $'...', backticks).
func skipQuoted(line string, i int, escapes bool) int {
	quote := line[i]
	escapes = escapes || quote == '"'
	for i++; i < len(line) && line[i] != quote; i++ {
		if escapes && line[i] == '\\' {
			i++
		}
	}
	return min(i, len(line)-1)
}

// skipBalanced returns the index of the `)` that closes the `(` at i, or
// the last index when there is none. Quotes and backticks hide parentheses.
func skipBalanced(line string, i int) int {
	depth := 0
	for ; i < len(line); i++ {
		switch line[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		case '\'', '"':
			i = skipQuoted(line, i, false)
		case '`':
			i = skipQuoted(line, i, true)
		case '\\':
			i++
		}
	}
	return len(line) - 1
}

// skipTest returns the index of the last `]` of the `]]` word that closes
// the `[[` at i, or the last index when there is none.
func skipTest(line string, i int) int {
	for i += 2; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\'' || c == '"':
			i = skipQuoted(line, i, false)
		case c == '`':
			i = skipQuoted(line, i, true)
		case c == '\\':
			i++
		case c == ']' && strings.HasPrefix(line[i:], "]]") && isBlank(line[i-1]) &&
			(i+2 == len(line) || strings.IndexByte(" \t\n;&|)", line[i+2]) >= 0):
			return i + 1
		}
	}
	return len(line) - 1
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

// readHeredocDelimiter reads the word after `<<` (or `<<-`) starting at i,
// unquoted, and returns it with the index of its last character.
func readHeredocDelimiter(line string, i int) (string, int) {
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	var delimiter strings.Builder
	for ; i < len(line) && strings.IndexByte(" \t\n;|&<>()", line[i]) < 0; i++ {
		switch c := line[i]; c {
		case '\'', '"':
			end := strings.IndexByte(line[i+1:], c)
			if end < 0 {
				end = len(line) - i - 1
			}
			delimiter.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case '\\':
			if i+1 < len(line) {
				i++
				delimiter.WriteByte(line[i])
			}
		default:
			delimiter.WriteByte(c)
		}
	}
	return delimiter.String(), i - 1
}

type heredoc struct {
	delimiter string
	stripTabs bool // <<- strips leading tabs from body lines, delimiter included
}

// skipHeredocBodies returns the index of the newline that ends the last
// here-document body opened on the line ending at the newline at i.
func skipHeredocBodies(line string, i int, heredocs []heredoc) int {
	for _, h := range heredocs {
		for i < len(line) {
			end := strings.IndexByte(line[i+1:], '\n')
			if end < 0 {
				end = len(line) - i - 1
			}
			body := line[i+1 : i+1+end]
			i += end + 1
			if h.stripTabs {
				body = strings.TrimLeft(body, "\t")
			}
			if body == h.delimiter {
				break
			}
		}
	}
	return i
}

// onlyLeadingTokens reports whether a command so far is no more than
// keywords, so the next word is in command position.
func onlyLeadingTokens(words []string) bool {
	for _, w := range words {
		if !leadingTokens[w] {
			return false
		}
	}
	return true
}

// splitCommands tokenizes a command line into simple commands. Quotes are
// stripped; substitutions are kept raw inside their word, so their contents
// never become commands. Shell data -- comments, here-document bodies,
// arithmetic, [[ tests ]], array values -- is skipped whole: read as
// commands, it would be sent as command names, whatever it held.
func splitCommands(line string) []simpleCommand {
	var cmds []simpleCommand
	var words []string
	var word strings.Builder
	inWord := false // distinguishes an empty quoted word ('') from no word
	var heredocs []heredoc

	add := func(s string) { word.WriteString(s); inWord = true }
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func(end terminator) {
		endWord()
		// An empty clause still ends its clause.
		if len(words) > 0 || end == endClause {
			cmds = append(cmds, simpleCommand{words, end})
			words = nil
		}
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		next := func(n int) byte {
			if i+n < len(line) {
				return line[i+n]
			}
			return 0
		}
		switch {
		case c == ' ' || c == '\t':
			endWord()
		case c == '\n':
			endCmd(endList)
			i = skipHeredocBodies(line, i, heredocs)
			heredocs = nil
		case c == '#' && !inWord:
			if end := strings.IndexByte(line[i:], '\n'); end >= 0 {
				i += end - 1
			} else {
				i = len(line) - 1
			}
		case c == '<' && next(1) == '<' && next(2) == '<':
			// A here-string's word is an argument, like any other.
			add("<<<")
			i += 2
		case c == '<' && next(1) == '<':
			endWord()
			stripTabs := next(2) == '-'
			start := i + 2
			if stripTabs {
				start++
			}
			delimiter, end := readHeredocDelimiter(line, start)
			heredocs = append(heredocs, heredoc{delimiter, stripTabs})
			i = end
		case c == ';':
			switch {
			case next(1) == ';':
				i++
				if next(1) == '&' {
					i++
				}
				endCmd(endClause)
			case next(1) == '&':
				i++
				endCmd(endClause)
			default:
				endCmd(endList)
			}
		case c == '|':
			if next(1) == '|' {
				i++
			}
			endCmd(endList)
		case c == '&':
			// 2>&1, >&file and &>file are redirections, not separators.
			if (i > 0 && line[i-1] == '>') || next(1) == '>' {
				add("&")
			} else {
				endCmd(endList)
			}
		case c == '(' && !inWord && next(1) == '(':
			// Arithmetic, (( ... )) or for (( ... )), runs no program.
			i = skipBalanced(line, i)
		case c == '(' && inWord && arrayAssignmentPattern.MatchString(word.String()):
			end := skipBalanced(line, i)
			add(line[i : end+1])
			i = end
		case c == '(' || c == ')':
			// Own word so "(cd x && y)" leaves "cd" and "y" clean.
			endWord()
			words = append(words, string(c))
		case c == '[' && !inWord && next(1) == '[' && (i+2 == len(line) || isBlank(next(2))) && onlyLeadingTokens(words):
			// A [[ test ]] runs no program; its && and || are not separators.
			i = skipTest(line, i)
		case c == '\\':
			if i+1 < len(line) {
				i++
				if line[i] != '\n' {
					add(line[i : i+1])
				}
			}
		case c == '$' && next(1) == '\'':
			end := skipQuoted(line, i+1, true)
			add(line[i : end+1])
			i = end
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				end = len(line) - i - 1
			}
			add(line[i+1 : i+1+end])
			inWord = true
			i += end + 1
		case c == '"':
			inWord = true
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				word.WriteByte(line[i])
			}
		case c == '`':
			end := i + 1
			for ; end < len(line) && line[end] != '`'; end++ {
				if line[end] == '\\' {
					end++
				}
			}
			end = min(end, len(line)-1)
			add(line[i : end+1])
			i = end
		case c == '$' && next(1) == '(':
			end := skipBalanced(line, i+1)
			add(line[i : end+1])
			i = end
		default:
			add(line[i : i+1])
		}
	}
	endCmd(endList)
	return cmds
}

// skipWrapper drops the wrapper at words[0] together with its options and,
// for timeout, its duration argument.
func skipWrapper(words []string) []string {
	valueOptions := wrappers[words[0]]
	isTimeout := words[0] == "timeout"
	words = words[1:]
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if valueOptions[words[0]] && len(words) > 1 {
			words = words[2:]
		} else {
			words = words[1:]
		}
	}
	if isTimeout && len(words) > 0 {
		words = words[1:]
	}
	return words
}

func subcommand(words []string, program llmusage.SubcommandProgram) (string, bool) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case slices.Contains(program.ValueOptions, w):
			i++
		case strings.HasPrefix(w, "-"):
		default:
			return w, wordPattern.MatchString(w)
		}
	}
	return "", false
}

func commandOf(words []string, programs map[string]llmusage.SubcommandProgram) (string, bool) {
	for len(words) > 0 {
		head := words[0]
		if _, ok := wrappers[head]; ok {
			words = skipWrapper(words)
		} else if leadingTokens[head] || assignmentPattern.MatchString(head) {
			words = words[1:]
		} else {
			break
		}
	}
	if len(words) == 0 || compoundStarts[words[0]] || (len(words) == 1 && closingTokens[words[0]]) {
		return "", false
	}

	program := path.Base(words[0])
	if !wordPattern.MatchString(program) {
		return llmusage.CommandOther, true
	}
	if sub, ok := programs[program]; ok {
		if name, valid := subcommand(words[1:], sub); valid {
			return program + " " + name, true
		}
	}
	return program, true
}

// caseState is where a case statement's parsing is: its patterns are data,
// and only its clauses' bodies run programs.
type caseState int

const (
	caseIn      caseState = iota // after `case WORD`, before `in`
	casePattern                  // expecting a pattern, up to its `)`, or esac
	caseBody                     // in a clause's body, until ;; ;& ;;& or esac
)

// extractCommands is the reference implementation of the fixture's rules: the
// fixture is only useful to the Python and TypeScript clients if its expected
// values follow from them.
func extractCommands(line string, programs map[string]llmusage.SubcommandProgram) []string {
	var out []string
	var cases []caseState // innermost last
	for _, cmd := range splitCommands(line) {
		words := cmd.words
		for len(words) > 0 {
			top := len(cases) - 1
			if top >= 0 && cases[top] == caseIn {
				if in := slices.Index(words, "in"); in >= 0 {
					cases[top] = casePattern
					words = words[in+1:]
				} else {
					words = nil
				}
				continue
			}
			if top >= 0 && cases[top] == casePattern {
				if words[0] == "esac" {
					cases = cases[:top]
					words = words[1:]
				} else if closing := slices.Index(words, ")"); closing >= 0 {
					cases[top] = caseBody
					words = words[closing+1:]
				} else {
					// An alternative ending in `|`, as in `a|b)`, is a pattern too.
					words = nil
				}
				continue
			}
			first := slices.IndexFunc(words, func(w string) bool { return !leadingTokens[w] })
			if first >= 0 && words[first] == "case" {
				cases = append(cases, caseIn)
				// The word after `case` is its subject, not `in`.
				words = words[min(first+2, len(words)):]
				continue
			}
			if top >= 0 && words[0] == "esac" {
				// The last clause needs no ;;.
				cases = cases[:top]
				words = words[1:]
				continue
			}
			if c, ok := commandOf(words, programs); ok {
				out = append(out, c)
			}
			break
		}
		if top := len(cases) - 1; cmd.end == endClause && top >= 0 && cases[top] == caseBody {
			cases[top] = casePattern
		}
	}
	return out
}

func TestBashCommandsFixture_ReferenceExtractor(t *testing.T) {
	data, err := os.ReadFile("fixtures/bash_commands.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name        string   `json:"name"`
			CommandLine string   `json:"command_line"`
			Expected    []string `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := extractCommands(c.CommandLine, llmusage.SubcommandPrograms)
			assert.Equal(t, c.Expected, got)
			for _, command := range got {
				assert.True(t, llmusage.ValidCommand(command), command)
			}
		})
	}
}

// Every fixture case runs a program (TestBashCommandsFixture requires it), so
// a line that runs none is checked here.
func TestBashCommandsFixture_EmptyLine(t *testing.T) {
	assert.Empty(t, extractCommands("", llmusage.SubcommandPrograms))
}
