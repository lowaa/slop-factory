// Package httpx holds small HTTP helpers so handlers write JSON responses
// and errors the same way rather than duplicating encode/error-shape logic.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON encodes before committing the status, so a value that cannot be
// encoded yields a 500 rather than a success status with an empty body.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// WriteError writes {"error": message}.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// Problem is an error answer with a machine-readable Code a client can
// branch on (e.g. "reauth_required") next to the human Detail. It has the
// shape of huma's own errors (RFC 9457 problem details), and satisfies
// huma.StatusError, so a huma operation can return one as its error and
// a plain handler can write one with WriteProblem: a client reads both
// the same way.
type Problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func NewProblem(status int, code, detail string) *Problem {
	return &Problem{Status: status, Title: http.StatusText(status), Detail: detail, Code: code}
}

func (p *Problem) Error() string { return p.Detail }

// GetStatus makes a Problem a huma.StatusError.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes huma send a Problem as problem+json, as it does its
// own errors.
func (p *Problem) ContentType(string) string { return "application/problem+json" }

func WriteProblem(w http.ResponseWriter, p *Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
