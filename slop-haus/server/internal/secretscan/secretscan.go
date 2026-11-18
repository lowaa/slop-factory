// Package secretscan recognises text shaped like a credential, so the server
// can refuse to store it when an agent wrote it.
//
// It is defence in depth against an agent that was steered, by text it read,
// into copying a secret into something it writes. Every rule is a known
// credential format; there is deliberately no bare entropy rule, because
// commit SHAs, UUIDs and base64 in code snippets are all high entropy and an
// agent writes them all day. Only the generic `password=` rule looks at
// entropy, and only for the value of such an assignment.
//
// A finding names a Kind, never the matched text: the whole point is that the
// value does not reach a response, a log line or an audit record.
package secretscan

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Kind is a family of credential. Its string is the metric label, so values
// are fixed and small.
type Kind string

const (
	SlopHausToken  Kind = "slop_haus_token"
	GitHubToken    Kind = "github_token"
	AnthropicKey   Kind = "anthropic_key"
	OpenAIKey      Kind = "openai_key"
	JWT            Kind = "jwt"
	PrivateKey     Kind = "private_key"
	AWSAccessKeyID Kind = "aws_access_key_id"
	SecretAssign   Kind = "secret_assignment"
)

// Kinds is every Kind, for pre-creating metric series.
var Kinds = []Kind{SlopHausToken, GitHubToken, AnthropicKey, OpenAIKey, JWT, PrivateKey, AWSAccessKeyID, SecretAssign}

var descriptions = map[Kind]string{
	SlopHausToken:  "a slop-haus token",
	GitHubToken:    "a GitHub token",
	AnthropicKey:   "an Anthropic API key",
	OpenAIKey:      "an OpenAI API key",
	JWT:            "a JSON Web Token",
	PrivateKey:     "a private key",
	AWSAccessKeyID: "an AWS access key id",
	SecretAssign:   "a password or token assignment",
}

// Description is the kind in a sentence: "a GitHub token".
func (k Kind) Description() string {
	if d, ok := descriptions[k]; ok {
		return d
	}
	return "a credential"
}

// A rule finds candidates with a regexp and may then confirm one, to keep
// false positives down.
type rule struct {
	kind    Kind
	re      *regexp.Regexp
	confirm func(match string) bool
}

var rules = []rule{
	// shi_ is the intake source token's prefix.
	{kind: SlopHausToken, re: regexp.MustCompile(`\bshi_[A-Za-z0-9_-]{43}\b`)},
	{kind: GitHubToken, re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`)},
	// Anthropic's keys also start with sk-, so they are tried first and the
	// OpenAI rule leaves them alone.
	{kind: AnthropicKey, re: regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(sk-ant-[A-Za-z0-9_-]{20,})`), confirm: hasLetterAndDigit},
	{kind: OpenAIKey, re: regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,})`), confirm: func(m string) bool {
		return !strings.HasPrefix(m, "sk-ant-") && hasLetterAndDigit(m)
	}},
	{kind: JWT, re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`), confirm: jsonHeader},
	{kind: PrivateKey, re: regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY(?: BLOCK)?-----\s+(?:[A-Za-z0-9-]+: [^\n]*\n\s*)*[A-Za-z0-9+/=]{32,}`)},
	{kind: AWSAccessKeyID, re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`)},
}

// assignment is `password = "value"`, `"token": "value"`, `API_KEY=value`.
var assignment = regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)["']?\s*[:=]\s*["']?([A-Za-z0-9+/=_.~!@#$%^&*-]{16,})`)

// runs are the stretches a bare opaque token could be: it is delimited by
// anything outside base64url, and a path or file name holding one has . or /
// in the run, which keeps it from being taken for one.
var runs = regexp.MustCompile(`[A-Za-z0-9_./+=-]+`)

// opaqueLen is the length of auth.NewOpaqueToken's 32 bytes in unpadded
// base64url: the shape of slop-haus's agent and OAuth access, refresh and
// device tokens, which carry no prefix.
const opaqueLen = 43

var (
	opaque = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	hexRun = regexp.MustCompile(`^[0-9a-fA-F-]+$`)
)

// Scan is the kinds of credential text looks like, each once, in the order
// the rules are listed. It returns nil for text with none.
func Scan(text string) []Kind {
	var found []Kind
	add := func(k Kind) {
		for _, f := range found {
			if f == k {
				return
			}
		}
		found = append(found, k)
	}
	for _, r := range rules {
		for _, m := range r.re.FindAllStringSubmatch(text, -1) {
			match := m[len(m)-1]
			if r.confirm == nil || r.confirm(match) {
				add(r.kind)
				break
			}
		}
	}
	for _, run := range runs.FindAllString(text, -1) {
		if isOpaqueToken(run) {
			add(SlopHausToken)
			break
		}
	}
	for _, m := range assignment.FindAllStringSubmatch(text, -1) {
		if looksLikeSecretValue(m[1]) {
			add(SecretAssign)
			break
		}
	}
	return found
}

func isOpaqueToken(run string) bool {
	run = strings.TrimRight(run, ".=")
	return len(run) == opaqueLen && opaque.MatchString(run) && hasUpperLowerDigit(run)
}

func looksLikeSecretValue(v string) bool {
	v = strings.TrimRight(v, ".,;")
	// A commit SHA or a UUID after "token=" is an identifier, not a secret.
	if hexRun.MatchString(v) && (len(v) == 40 || len(v) == 64 || len(v) == 36) {
		return false
	}
	return len(v) >= 16 && hasLetterAndDigit(v) && entropy(v) >= 3.5
}

func hasLetterAndDigit(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letter = true
		}
	}
	return letter && digit
}

func hasUpperLowerDigit(s string) bool {
	var upper, lower, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		}
	}
	return upper && lower && digit
}

// jsonHeader confirms a JWT: its first segment is base64url JSON.
func jsonHeader(token string) bool {
	header, _, _ := strings.Cut(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(header)
	return err == nil && strings.HasPrefix(string(raw), "{")
}

// entropy is the Shannon entropy of s in bits per character.
func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// Error is a refusal: what kind of credential text looked like, and in which
// input. It never holds the text.
type Error struct {
	Kinds []Kind
	// Field is the input the text was in, as its caller named it; empty when
	// there is one input.
	Field string
}

func (e *Error) Error() string {
	descs := make([]string, len(e.Kinds))
	for i, k := range e.Kinds {
		descs[i] = k.Description()
	}
	where := "text"
	if e.Field != "" {
		where = fmt.Sprintf("%q", e.Field)
	}
	return fmt.Sprintf("this %s contains what looks like %s; remove it and try again without it. slop-haus does not store credentials, so do not retry with the value", where, strings.Join(descs, " and "))
}
