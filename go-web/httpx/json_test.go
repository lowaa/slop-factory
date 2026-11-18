package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lowaa/slop-factory/go-web/httpx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteJSON(rec, http.StatusCreated, map[string]int{"a": 1})
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"a":1}`, rec.Body.String())
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteError(rec, http.StatusBadRequest, "nope")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":"nope"}`, rec.Body.String())
}

func TestWriteProblem(t *testing.T) {
	p := httpx.NewProblem(http.StatusUnauthorized, "reauth_required", "log in again")
	rec := httptest.NewRecorder()
	httpx.WriteProblem(rec, p)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"status":401,"title":"Unauthorized","detail":"log in again","code":"reauth_required"}`, rec.Body.String())
}

func TestProblemIsStatusError(t *testing.T) {
	var err error = httpx.NewProblem(http.StatusConflict, "c", "d")
	assert.Equal(t, "d", err.Error())
	p := httpx.NewProblem(http.StatusConflict, "c", "d")
	assert.Equal(t, http.StatusConflict, p.GetStatus())
	assert.Equal(t, "application/problem+json", p.ContentType(""))
}

func TestWriteJSONUnencodableIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteJSON(rec, http.StatusOK, make(chan int))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"internal server error"}`, rec.Body.String())
}
