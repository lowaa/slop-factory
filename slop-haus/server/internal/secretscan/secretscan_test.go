package secretscan_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretscan"
)

// The fakes are built at run time so no source line looks like a credential
// to a scanner of this repo.
const (
	b64url43 = "Ab3dEf6hIj9lMn2pQr5tUv8xYz1BcD4fGh7JkL0mNoP" // 43 chars, mixed
	alnum36  = "aB3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF3hJ6"
)

func jwt() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(`{"sub":"1234567890"}`)) + "." + enc([]byte("signaturebytes1234"))
}

func TestScan(t *testing.T) {
	require.Len(t, b64url43, 43)
	require.Len(t, alnum36, 36)

	tests := []struct {
		name string
		text string
		want []secretscan.Kind
	}{
		// true positives
		{"intake token", "use shi_" + b64url43 + " to post", []secretscan.Kind{secretscan.SlopHausToken}},
		{"bare opaque token", "Authorization: Bearer " + b64url43, []secretscan.Kind{secretscan.SlopHausToken}},
		{"bare opaque token at sentence end", "my token was " + b64url43 + ".", []secretscan.Kind{secretscan.SlopHausToken}},
		{"github ghp", "ghp_" + alnum36, []secretscan.Kind{secretscan.GitHubToken}},
		{"github gho", "x gho_" + alnum36 + " y", []secretscan.Kind{secretscan.GitHubToken}},
		{"github ghs", "ghs_" + alnum36, []secretscan.Kind{secretscan.GitHubToken}},
		{"github fine grained", "github_pat_" + strings.Repeat("aB3dE6gH9j", 8), []secretscan.Kind{secretscan.GitHubToken}},
		{"anthropic key", "key sk-ant-api03-" + b64url43, []secretscan.Kind{secretscan.AnthropicKey}},
		{"openai key", "OPENAI sk-" + alnum36 + "abcdEFGH12", []secretscan.Kind{secretscan.OpenAIKey}},
		{"openai project key", "sk-proj-" + b64url43, []secretscan.Kind{secretscan.OpenAIKey}},
		{"jwt", "token " + jwt(), []secretscan.Kind{secretscan.JWT}},
		{"pem key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt\n-----END RSA PRIVATE KEY-----", []secretscan.Kind{secretscan.PrivateKey}},
		{"pem openssh key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n", []secretscan.Kind{secretscan.PrivateKey}},
		{"aws key id", "id AKIAIOSFODNN7EXAMPLE here", []secretscan.Kind{secretscan.AWSAccessKeyID}},
		{"password assignment", `password = "Xk9v2LmQ8rTz4WnB7"`, []secretscan.Kind{secretscan.SecretAssign}},
		{"token assignment", "TOKEN=q8Zr3Lk0Vt7Yw2Nb5Hd9", []secretscan.Kind{secretscan.SecretAssign}},
		{"json api key", `{"api_key": "f3Kd9sL2mQ8xZ7vB1nC4"}`, []secretscan.Kind{secretscan.SecretAssign}},
		{"two kinds", "ghp_" + alnum36 + "\npassword=Xk9v2LmQ8rTz4WnB7", []secretscan.Kind{secretscan.GitHubToken, secretscan.SecretAssign}},

		// look-alikes that must pass
		{"empty", "", nil},
		{"prose", "The token is refreshed every hour; a password must be rotated.", nil},
		{"commit sha", "merged 0f0962c5c3d1b091fec0e8dc589b8b35cf5f28ee at f28ee459b6f112c642bbe0ba54beb1b2ec107ae1", nil},
		{"sha256", "sha256=" + strings.Repeat("ab12", 16), nil},
		{"uuid", "agent e29c8fdf-8ce4-454e-be84-c1a15d076278", nil},
		{"token equals sha", "token=f28ee459b6f112c642bbe0ba54beb1b2ec107ae1", nil},
		{"token equals uuid", "token: e29c8fdf-8ce4-454e-be84-c1a15d076278", nil},
		{"password prose", "password=<redacted> and token=${TOKEN} and secret=os.Getenv(x)", nil},
		{"code assigning a token", "token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])", nil},
		{"low entropy value", "password=aaaaaaaaaaaaaaaa1", nil},
		{"base64 in a snippet", `data := base64.StdEncoding.EncodeToString([]byte("hello world, this is a long string"))`, nil},
		{"path of 43 chars", "see internal/mcpserver/" + b64url43 + ".go", nil},
		{"43-char snake case identifier", "func test_a_very_long_identifier_name_indeed_ok", nil},
		{"44 chars", "x" + b64url43, nil},
		{"short sk", "the sk-learn package and a risk-assessment-and-mitigation-plan", nil},
		{"hyphenated sk words", "task-force-name: sk-this-is-a-very-long-hyphenated-words-only-phrase", nil},
		{"ghp prefix alone", "ghp_ tokens are rotated", nil},
		{"jwt header alone", "start with eyJhbGciOiJIUzI1NiJ9 then", nil},
		{"fake jwt shape", "eyJzdWJ.eyJzdWJ.abcdefghij", nil},
		{"pem header in prose", "refuse text with -----BEGIN PRIVATE KEY----- blocks", nil},
		{"public key", "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu1SU1LfVLPHCozMxH2Mo\n", nil},
		{"akia lowercase", "akiaiosfodnn7example", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, secretscan.Scan(tc.text))
		})
	}
}

type nested struct {
	Note string `json:"note"`
}

type input struct {
	TicketID int64    `json:"ticket_id"`
	Body     string   `json:"body"`
	Paths    []string `json:"paths,omitempty"`
	Inner    *nested  `json:"inner,omitempty"`
}

func TestCheck(t *testing.T) {
	secret := "ghp_" + alnum36

	assert.Nil(t, secretscan.Check(input{Body: "fine", Paths: []string{"a/b"}}))

	err := secretscan.Check(&input{Body: "fine", Paths: []string{"a", secret}})
	require.NotNil(t, err)
	assert.Equal(t, "paths", err.Field)
	assert.Equal(t, []secretscan.Kind{secretscan.GitHubToken}, err.Kinds)

	err = secretscan.Check(input{Inner: &nested{Note: secret}})
	require.NotNil(t, err)
	assert.Equal(t, "inner", err.Field)

	err = secretscan.Check(input{Body: "see " + secret})
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "a GitHub token")
	assert.Contains(t, err.Error(), `"body"`)
	assert.NotContains(t, err.Error(), secret)
}
