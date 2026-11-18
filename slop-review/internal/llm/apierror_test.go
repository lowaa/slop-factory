package llm

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
)

// apiError is an error as openai-go builds one: Type, Code and Message from
// the body's error object when it has one, and the whole body kept on the
// response.
func apiError(status int, body string, fields openai.Error) error {
	fields.StatusCode = status
	fields.Response = &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
	return &fields
}

func TestErrorText(t *testing.T) {
	const secret = "sekrit-token"
	long := strings.Repeat("é", maxErrorBody) // two bytes a rune, so a byte cut splits one
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"not an API error":     {errors.New("dial tcp: refused by " + secret), "dial tcp: refused by [redacted]"},
		"empty body":           {apiError(502, "", openai.Error{}), "OpenAI API error: 502 Bad Gateway"},
		"the fields, in order": {apiError(400, `{"error":{}}`, openai.Error{Type: "invalid_request_error", Code: "bad_param", Message: "no " + secret}), "OpenAI API error: 400 Bad Request: invalid_request_error: bad_param: no [redacted]"},
		"only some fields":     {apiError(400, `{"error":{}}`, openai.Error{Code: "bad_param"}), "OpenAI API error: 400 Bad Request: bad_param"},
		"a body without them":  {apiError(400, ` {"detail":"Unsupported parameter"} `, openai.Error{}), `OpenAI API error: 400 Bad Request: {"detail":"Unsupported parameter"}`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, errorText(tc.err, secret))
		})
	}

	t.Run("a long body is cut, after redaction, on a rune", func(t *testing.T) {
		// The secret straddles the cut: redacted only after it, its first
		// part would survive.
		body := strings.Repeat("x", maxErrorBody-4) + secret + long
		got := errorText(apiError(502, body, openai.Error{}), secret)
		assert.NotContains(t, got, secret[:4])
		assert.True(t, strings.HasSuffix(got, "..."))
		assert.LessOrEqual(t, len(got), len("OpenAI API error: 502 Bad Gateway: ")+maxErrorBody+len("..."))
		assert.True(t, utf8.ValidString(got))

		got = errorText(apiError(502, "x"+long, openai.Error{}), secret)
		assert.True(t, utf8.ValidString(got), "a rune split by the cut is dropped")
	})

	t.Run("the body is still there for the next read", func(t *testing.T) {
		err := apiError(502, "proxy down", openai.Error{})
		assert.Equal(t, errorText(err, ""), errorText(err, ""))
	})
}
