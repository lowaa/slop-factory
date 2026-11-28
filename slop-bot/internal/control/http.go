package control

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorBody is every error response's shape.
type ErrorBody struct {
	Error string `json:"error"`
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes err's message with status.
func WriteError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, ErrorBody{Error: err.Error()})
}

// ErrorMapping maps a sentinel error to an HTTP status, so each package
// declares its refusals once.
type ErrorMapping struct {
	Err    error
	Status int
}

// StatusFor returns the status of the first mapping err matches, or 500.
func StatusFor(err error, mappings []ErrorMapping) int {
	for _, m := range mappings {
		if errors.Is(err, m.Err) {
			return m.Status
		}
	}
	return http.StatusInternalServerError
}

// DecodeJSON reads r's body into v, refusing unknown fields: a client
// sending a field the daemon doesn't know is a version mismatch worth
// hearing about.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
