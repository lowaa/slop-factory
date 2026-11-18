package llm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
)

// maxErrorBody bounds how much of an error body without the usual fields is
// kept.
const maxErrorBody = 500

// errorText is err's text with secret redacted; for an API error, its
// status and what the backend said: the body is where a usage limit, a
// rejected key or a bad request says what it is. A backend may echo the
// credential back, so callers pass theirs.
func errorText(err error, secret string) string {
	redact := func(s string) string {
		if secret == "" {
			return s
		}
		return strings.ReplaceAll(s, secret, "[redacted]")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return redact(err.Error())
	}
	// Not apiErr.Error(), which carries the request URL and the body's
	// error object verbatim: what the backend said is added below, once.
	text := fmt.Sprintf("OpenAI API error: %d %s", apiErr.StatusCode, http.StatusText(apiErr.StatusCode))
	var said []string
	for _, s := range []string{apiErr.Type, apiErr.Code, apiErr.Message} {
		if s != "" {
			said = append(said, redact(s))
		}
	}
	if len(said) == 0 {
		// No {"error": {...}} to read the fields from -- a {"detail": ...}
		// or a proxy's HTML page -- so the body itself, redacted before
		// it is cut so no part of the secret survives the cut.
		body := strings.TrimSpace(redact(responseBody(apiErr)))
		if body == "" {
			return text
		}
		if len(body) > maxErrorBody {
			body = strings.ToValidUTF8(body[:maxErrorBody], "") + "..."
		}
		said = append(said, body)
	}
	return text + ": " + strings.Join(said, ": ")
}

// responseBody reads the body openai-go keeps on an API error, and puts it
// back for whoever reads it next (errorText runs for the classification
// and again for the message).
func responseBody(apiErr *openai.Error) string {
	if apiErr.Response == nil || apiErr.Response.Body == nil {
		return ""
	}
	b, err := io.ReadAll(apiErr.Response.Body)
	apiErr.Response.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	return string(b)
}
